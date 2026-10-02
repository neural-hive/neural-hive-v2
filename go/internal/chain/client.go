// Package chain is the Go binding layer between Neural Hive services and the on-chain protocol.
//
// It loads the compiled ABIs straight from the Truffle artifacts (single source of truth) and
// wraps the transactions the coordinator, agents and relayer need. Nothing is hard-coded: every
// address comes from configuration, so the same binaries work on Ganache or Avalanche C-Chain.
package chain

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/neural-hive/hive-node/internal/config"
)

// Client is an authenticated JSON-RPC client bound to the deployed protocol.
type Client struct {
	cfg       *config.Config
	Eth       *ethclient.Client
	Key       *ecdsa.PrivateKey
	From      common.Address
	ChainID   *big.Int
	abis      map[string]*abi.ABI
	addresses map[string]common.Address
	mu        sync.Mutex
	nonce     uint64
	lastTx    common.Hash
}

// New connects to the configured RPC endpoint and binds the deployed contracts.
func New(cfg *config.Config, privateKeyHex string) (*Client, error) {
	eth, err := ethclient.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial rpc: %w", err)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	c := &Client{
		cfg:       cfg,
		Eth:       eth,
		Key:       key,
		From:      crypto.PubkeyToAddress(key.PublicKey),
		ChainID:   big.NewInt(cfg.ChainID),
		abis:      map[string]*abi.ABI{},
		addresses: map[string]common.Address{},
	}
	bindings := map[string]string{
		"HiveToken":               cfg.Contracts.HiveToken,
		"CapabilityRegistry":      cfg.Contracts.CapabilityRegistry,
		"ReputationRegistry":      cfg.Contracts.ReputationRegistry,
		"StakingSettlement":       cfg.Contracts.StakingSettlement,
		"TaskCoordinator":         cfg.Contracts.TaskCoordinator,
		"HiveSnowball":            cfg.Contracts.HiveSnowball,
		"MockTeleporterMessenger": cfg.Contracts.MockTeleporterMessenger,
	}
	for name, addr := range bindings {
		if addr == "" {
			return nil, fmt.Errorf("missing configured address for %s", name)
		}
		a, err := c.loadABI(name)
		if err != nil {
			return nil, err
		}
		c.abis[name] = a
		c.addresses[name] = common.HexToAddress(addr)
	}
	n, err := eth.PendingNonceAt(context.Background(), c.From)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	c.nonce = n
	return c, nil
}

// loadABI reads the ABI out of the Truffle artifact for a contract.
func (c *Client) loadABI(name string) (*abi.ABI, error) {
	p := filepath.Join(c.cfg.Root, "contracts", "build", "contracts", name+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s: %w", name, err)
	}
	var art struct {
		ABI json.RawMessage `json:"abi"`
	}
	if err := json.Unmarshal(raw, &art); err != nil {
		return nil, fmt.Errorf("parse artifact %s: %w", name, err)
	}
	parsed, err := abi.JSON(strings.NewReader(string(art.ABI)))
	if err != nil {
		return nil, fmt.Errorf("parse abi %s: %w", name, err)
	}
	return &parsed, nil
}

// Address returns the bound address of a protocol contract.
func (c *Client) Address(name string) common.Address { return c.addresses[name] }

// ABI returns the parsed ABI of a protocol contract.
func (c *Client) ABI(name string) *abi.ABI { return c.abis[name] }

func (c *Client) bound(name string) *bind.BoundContract {
	return bind.NewBoundContract(c.addresses[name], *c.abis[name], c.Eth, c.Eth, c.Eth)
}

// Call performs a read-only (eth_call) contract call and decodes the result into out.
func (c *Client) Call(contract, method string, out interface{}, args ...interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a := c.abis[contract]
	m, ok := a.Methods[method]
	if !ok {
		return fmt.Errorf("unknown method %s.%s", contract, method)
	}
	data, err := a.Pack(method, args...)
	if err != nil {
		return fmt.Errorf("pack %s.%s: %w", contract, method, err)
	}
	to := c.addresses[contract]
	ret, err := c.Eth.CallContract(ctx, ethereum.CallMsg{From: c.From, To: &to, Data: data}, nil)
	if err != nil {
		return fmt.Errorf("call %s.%s: %w", contract, method, err)
	}
	return unpackCallResult(a, m, out, ret)
}

// nextNonce returns the next nonce for the signing account. It always adopts the fresh pending
// nonce from the node and never goes backwards, so a stale local cache can never make this
// client submit a transaction with a nonce the account has already consumed - the
// tx-does-not-have-the-correct-nonce / nonce-too-low error. Several Neural Hive processes
// (coordinator, relayer, hub) share the deployer key, so the on-chain pending nonce is the
// only source of truth.
func (c *Client) nextNonce() (uint64, error) {
	n, err := c.Eth.PendingNonceAt(context.Background(), c.From)
	if err != nil {
		return c.nonce, nil
	}
	if n > c.nonce {
		c.nonce = n
	}
	return c.nonce, nil
}

// isNonceError reports whether an error is a stale or duplicate nonce rejection from the node.
func isNonceError(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "nonce too low") ||
		strings.Contains(m, "correct nonce") ||
		strings.Contains(m, "already known") ||
		strings.Contains(m, "replacement transaction underpriced")
}

// Send signs and submits a state-changing transaction and waits for it to be mined. A stale-nonce
// rejection (several processes share the deployer key) is retried once with a freshly read on-chain
// nonce, so transient nonce races self-heal instead of failing the caller.
func (c *Client) Send(contract, method string, args ...interface{}) (*types.Receipt, error) {
	var rc *types.Receipt
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		rc, err = c.sendOnce(contract, method, args...)
		if err == nil || !isNonceError(err) {
			return rc, err
		}
		c.mu.Lock()
		c.nonce = 0
		c.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
	}
	return rc, err
}

func (c *Client) sendOnce(contract, method string, args ...interface{}) (*types.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	opts, err := bind.NewKeyedTransactorWithChainID(c.Key, c.ChainID)
	if err != nil {
		return nil, err
	}
	nonce, err := c.nextNonce()
	if err != nil {
		return nil, err
	}
	opts.Context = ctx
	opts.Nonce = big.NewInt(int64(nonce))
	opts.GasLimit = 7_500_000
	opts.GasPrice = big.NewInt(2_000_000_000)
	tx, err := c.bound(contract).Transact(opts, method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", contract, method, err)
	}
	c.nonce = nonce + 1
	rc, err := bind.WaitMined(ctx, c.Eth, tx)
	if err != nil {
		c.lastTx = tx.Hash()
		return nil, fmt.Errorf("wait mined %s.%s: %w", contract, method, err)
	}
	c.lastTx = tx.Hash()
	if rc.Status == 0 {
		return rc, fmt.Errorf("%s.%s reverted (tx %s)", contract, method, tx.Hash().Hex())
	}
	return rc, nil
}

// SendTxOnly submits a state-changing transaction and returns its hash, retrying once on a stale
// nonce just like Send.
func (c *Client) SendTxOnly(contract, method string, args ...interface{}) (common.Hash, error) {
	var h common.Hash
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		h, err = c.sendTxOnlyOnce(contract, method, args...)
		if err == nil || !isNonceError(err) {
			return h, err
		}
		c.mu.Lock()
		c.nonce = 0
		c.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
	}
	return h, err
}

func (c *Client) sendTxOnlyOnce(contract, method string, args ...interface{}) (common.Hash, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	opts, err := bind.NewKeyedTransactorWithChainID(c.Key, c.ChainID)
	if err != nil {
		return common.Hash{}, err
	}
	nonce, err := c.nextNonce()
	if err != nil {
		return common.Hash{}, err
	}
	opts.Context = ctx
	opts.Nonce = big.NewInt(int64(nonce))
	opts.GasLimit = 7_500_000
	opts.GasPrice = big.NewInt(2_000_000_000)
	tx, err := c.bound(contract).Transact(opts, method, args...)
	if err != nil {
		return common.Hash{}, fmt.Errorf("%s.%s: %w", contract, method, err)
	}
	c.nonce = nonce + 1
	rc, err := bind.WaitMined(ctx, c.Eth, tx)
	if err != nil {
		return tx.Hash(), err
	}
	if rc.Status == 0 {
		return tx.Hash(), fmt.Errorf("%s.%s reverted (tx %s)", contract, method, tx.Hash().Hex())
	}
	return tx.Hash(), nil
}

// BalanceWei returns the native ETH balance of an address.
func (c *Client) BalanceWei(addr common.Address) (*big.Int, error) {
	return c.Eth.BalanceAt(context.Background(), addr, nil)
}

// unpackCallResult decodes a contract-call return value into out.
//
// go-ethereum routes a single-tuple result through copyAtomic, which assigns the whole decoded
// tuple to the destination first field (abi: cannot unmarshal struct in to *big.Int). We wrap
// struct destinations in a one-field struct so the tuple lands in that field and is then copied
// field-by-field, matching abigen-generated bindings.
func unpackCallResult(a *abi.ABI, m abi.Method, out interface{}, ret []byte) error {
	if len(m.Outputs) == 1 && m.Outputs[0].Type.T == abi.TupleTy {
		if rv := reflect.ValueOf(out); rv.Kind() == reflect.Ptr && !rv.IsNil() && rv.Elem().Kind() == reflect.Struct {
			field := reflect.StructField{Name: `V`, Type: rv.Elem().Type()}
			wrapper := reflect.New(reflect.StructOf([]reflect.StructField{field}))
			if err := a.UnpackIntoInterface(wrapper.Interface(), m.Name, ret); err != nil {
				return err
			}
			rv.Elem().Set(wrapper.Elem().Field(0))
			return nil
		}
	}
	return a.UnpackIntoInterface(out, m.Name, ret)
}

// LastTxHash returns the hash of the most recent state-changing transaction this client mined.
// The coordinator uses it to attach real on-chain evidence to a request for the UI.
func (c *Client) LastTxHash() common.Hash {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastTx
}
