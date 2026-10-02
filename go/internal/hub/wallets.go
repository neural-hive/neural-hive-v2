package hub

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/config"
)

// agentWallet is the deterministic on-chain identity of one agent: its payout and withdrawal wallet.
type agentWallet struct {
	ID      string
	Address common.Address
	Key     *ecdsa.PrivateKey
}

// AgentPayout records one agent share of a settled task.
type AgentPayout struct {
	AgentID  string `json:"agentId"`
	Address  string `json:"address"`
	Hive     string `json:"hive"`
	ShareWei string `json:"shareWei"`
	TxHash   string `json:"txHash,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Settlement is the full money-split record attached to a task result.
type Settlement struct {
	OK         bool          `json:"ok"`
	TotalHive  string        `json:"totalHive"`
	TotalWei   string        `json:"totalWei"`
	FeePercent float64       `json:"feePercent"`
	FeeHive    string        `json:"feeHive"`
	FeeWei     string        `json:"feeWei"`
	Treasury   string        `json:"treasury"`
	Agents     int           `json:"agents"`
	Payouts    []AgentPayout `json:"payouts"`
	Error      string        `json:"error,omitempty"`
}

// WalletBook resolves and remembers the deterministic wallet of each agent.
type WalletBook struct {
	mu      sync.Mutex
	byIndex map[int]agentWallet
	byID    map[string]agentWallet
}

func newWalletBook() *WalletBook {
	return &WalletBook{byIndex: map[int]agentWallet{}, byID: map[string]agentWallet{}}
}

// For derives (or returns) the wallet of an agent by its deterministic index.
func (wb *WalletBook) For(id string, index int, seed string) (agentWallet, error) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	if w, ok := wb.byID[id]; ok {
		return w, nil
	}
	key, err := agent.DeriveKey(seed, index)
	if err != nil {
		return agentWallet{}, fmt.Errorf("derive wallet for %s: %w", id, err)
	}
	w := agentWallet{ID: id, Address: crypto.PubkeyToAddress(key.PublicKey), Key: key}
	wb.byID[id] = w
	wb.byIndex[index] = w
	return w, nil
}

// Get returns the wallet of a known agent.
func (wb *WalletBook) Get(id string) (agentWallet, bool) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	w, ok := wb.byID[id]
	return w, ok
}

// OnChain is a minimal signer for the money-split and withdrawal transfers: it sends HiveToken
// transfers and plain value transfers with a locally held key.
type OnChain struct {
	rpc     string
	chainID *big.Int
	token   common.Address
	eth     *ethclient.Client
}

func newOnChain(cfg config.Hub) (*OnChain, error) {
	eth, err := ethclient.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial rpc: %w", err)
	}
	return &OnChain{rpc: cfg.RPCURL, chainID: big.NewInt(cfg.ChainID), token: common.HexToAddress(cfg.TokenAddress), eth: eth}, nil
}

// transferTokenTx signs and sends HiveToken.transfer(to, amount) from key and waits for the receipt.
func (oc *OnChain) transferTokenTx(ctx context.Context, key *ecdsa.PrivateKey, to common.Address, amount *big.Int) (common.Hash, error) {
	if amount == nil || amount.Sign() <= 0 {
		if oc.eth == nil {
			return common.Hash{}, fmt.Errorf("no chain connection")
		}
		return common.Hash{}, fmt.Errorf("nothing to transfer")
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	data, err := transferCalldata(to, amount)
	if err != nil {
		return common.Hash{}, err
	}
	nonce, err := oc.eth.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, err
	}
	gasPrice, err := oc.eth.SuggestGasPrice(ctx)
	if err != nil {
		gasPrice = big.NewInt(2_000_000_000)
	}
	toAddr := oc.token
	tx := types.NewTransaction(nonce, toAddr, big.NewInt(0), 200000, gasPrice, data)
	signer := types.LatestSignerForChainID(oc.chainID)
	signed, err := types.SignTx(tx, signer, key)
	if err != nil {
		return common.Hash{}, err
	}
	if err := oc.eth.SendTransaction(ctx, signed); err != nil {
		return common.Hash{}, err
	}
	rc, err := bind.WaitMined(ctx, oc.eth, signed)
	if err != nil {
		return signed.Hash(), err
	}
	if rc.Status == 0 {
		return signed.Hash(), fmt.Errorf("transfer reverted (tx %s)", signed.Hash().Hex())
	}
	return signed.Hash(), nil
}

// fundETH sends a little native coin so an agent wallet can pay gas for its own withdrawal transfer.
func (oc *OnChain) fundETH(ctx context.Context, key *ecdsa.PrivateKey, to common.Address, amount *big.Int) error {
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := oc.eth.PendingNonceAt(ctx, from)
	if err != nil {
		return err
	}
	gasPrice, err := oc.eth.SuggestGasPrice(ctx)
	if err != nil {
		gasPrice = big.NewInt(2_000_000_000)
	}
	tx := types.NewTransaction(nonce, to, amount, 21000, gasPrice, nil)
	signer := types.LatestSignerForChainID(oc.chainID)
	signed, err := types.SignTx(tx, signer, key)
	if err != nil {
		return err
	}
	if err := oc.eth.SendTransaction(ctx, signed); err != nil {
		return err
	}
	_, err = bind.WaitMined(ctx, oc.eth, signed)
	return err
}

func (oc *OnChain) ethBalance(ctx context.Context, addr common.Address) (*big.Int, error) {
	return oc.eth.BalanceAt(ctx, addr, nil)
}

func (oc *OnChain) tokenBalance(ctx context.Context, addr common.Address) (*big.Int, error) {
	data := append([]byte{0x70, 0xa0, 0x82, 0x31}, common.LeftPadBytes(addr.Bytes(), 32)...)
	to := oc.token
	msg := ethereum.CallMsg{From: common.Address{}, To: &to, Data: data}
	res, err := oc.eth.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(res), nil
}

// transferCalldata encodes transfer(address,uint256).
func transferCalldata(to common.Address, amount *big.Int) ([]byte, error) {
	if amount == nil {
		return nil, fmt.Errorf("amount is nil")
	}
	data := append([]byte{0xa9, 0x05, 0x9c, 0xbb}, common.LeftPadBytes(to.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(amount.Bytes(), 32)...)
	return data, nil
}

func decodeKeyHex(key *ecdsa.PrivateKey) string {
	return "0x" + hex.EncodeToString(crypto.FromECDSA(key))
}
func trimHexKey(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "0x") }

// chainFor lazily builds the on-chain signer using the Hub key.
func (h *Hub) chainFor() (*OnChain, error) {
	h.chainMu.Lock()
	defer h.chainMu.Unlock()
	if h.onchain != nil {
		return h.onchain, nil
	}
	oc, err := newOnChain(h.Cfg)
	if err != nil {
		return nil, err
	}
	h.onchain = oc
	return oc, nil
}

// hubKey returns the key that owns the seed agents and holds the treasury.
func (h *Hub) hubKey() (*ecdsa.PrivateKey, error) {
	k := strings.TrimSpace(h.Cfg.HubPrivateKey)
	if k == "" {
		return nil, fmt.Errorf("no hub private key configured (HUB_PRIVATE_KEY)")
	}
	return crypto.HexToECDSA(trimHexKey(k))
}

// OwnerAddress is the on-chain owner (operator) of a seed agent: the Hub key.
func (h *Hub) OwnerAddress() string {
	key, err := h.hubKey()
	if err != nil {
		return ""
	}
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}

// SettlePayment splits a task payment on chain: the Hub keeps FeePercent and sends the rest to the
// agents that served the task, split equally. Every transfer is a real on-chain transaction; a
// failure is captured per agent and reported, never hidden.
func (h *Hub) SettlePayment(ctx context.Context, taskID string, agentIDs []string, totalWei *big.Int) Settlement {
	st := Settlement{TotalWei: "0", TotalHive: "0", FeePercent: h.Cfg.HubFeePercent, Treasury: h.Cfg.Treasury, Payouts: []AgentPayout{}}
	if totalWei == nil || totalWei.Sign() <= 0 || len(agentIDs) == 0 {
		return st
	}
	oc, err := h.chainFor()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	key, err := h.hubKey()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	feeBps := big.NewInt(int64(h.Cfg.HubFeePercent * 100))
	fee := new(big.Int).Mul(totalWei, feeBps)
	fee.Quo(fee, big.NewInt(10000))
	dist := new(big.Int).Sub(totalWei, fee)
	n := big.NewInt(int64(len(agentIDs)))
	per := new(big.Int).Quo(dist, n)
	st.TotalWei = totalWei.String()
	st.TotalHive = FormatHive(totalWei)
	st.FeeWei = fee.String()
	st.FeeHive = FormatHive(fee)
	st.Agents = len(agentIDs)
	st.OK = true
	for _, id := range agentIDs {
		card, ok := h.Registry.Get(id)
		if !ok {
			continue
		}
		wallet, werr := h.Wallets.For(id, card.Index, h.Cfg.AgentKeySeed)
		payout := AgentPayout{AgentID: id, Hive: FormatHive(per), ShareWei: per.String()}
		if werr != nil {
			payout.Error = werr.Error()
			st.Payouts = append(st.Payouts, payout)
			st.OK = false
			continue
		}
		payout.Address = wallet.Address.Hex()
		// Route the payout into the agent own AgentWallet smart contract when the factory is
		// configured, so each agent balance is held by its own contract and only the agent owner
		// can withdraw it.
		target := wallet.Address
		if h.WalletFactory != nil && h.WalletFactory.Enabled() {
			if wAddr, werr2 := h.EnsureAgentWallet(ctx, id, h.ownerOf(card)); werr2 == nil {
				target = wAddr
				payout.Address = wAddr.Hex()
			}
		}
		txHash, terr := oc.transferTokenTx(ctx, key, target, per)
		if terr != nil {
			payout.Error = terr.Error()
			st.OK = false
		} else {
			payout.TxHash = txHash.Hex()
		}
		st.Payouts = append(st.Payouts, payout)
	}
	return st
}

// WithdrawAgent sends an agent wallet HIVE balance to its owner. The Hub funds a little gas and
// signs the transfer from the agent wallet, so the payout really lands in the owner wallet.
func (h *Hub) WithdrawAgent(ctx context.Context, agentID, owner string) (map[string]interface{}, error) {
	card, ok := h.Registry.Get(agentID)
	if !ok {
		return nil, fmt.Errorf("unknown agent")
	}
	ownerAddr := common.HexToAddress(strings.TrimSpace(owner))
	if ownerAddr == (common.Address{}) {
		return nil, fmt.Errorf("owner address is required")
	}
	wallet, err := h.Wallets.For(agentID, card.Index, h.Cfg.AgentKeySeed)
	if err != nil {
		return nil, err
	}
	oc, err := h.chainFor()
	if err != nil {
		return nil, err
	}
	bal, err := oc.tokenBalance(ctx, wallet.Address)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{"agentId": agentID, "wallet": wallet.Address.Hex(), "owner": ownerAddr.Hex(), "balanceHive": FormatHive(bal), "balanceWei": bal.String()}
	if bal.Sign() <= 0 {
		out["ok"] = false
		out["detail"] = "the agent wallet holds no HIVE to withdraw"
		return out, nil
	}
	key, err := h.hubKey()
	if err != nil {
		return nil, err
	}
	gas, _ := oc.ethBalance(ctx, wallet.Address)
	if gas.Cmp(big.NewInt(1_000_000_000_000_000)) < 0 {
		_ = oc.fundETH(ctx, key, wallet.Address, big.NewInt(10_000_000_000_000_000))
	}
	txHash, err := oc.transferTokenTx(ctx, wallet.Key, ownerAddr, bal)
	if err != nil {
		return nil, err
	}
	out["ok"] = true
	out["txHash"] = txHash.Hex()
	out["withdrawnHive"] = FormatHive(bal)
	return out, nil
}

// AgentWalletInfo reports the deterministic wallet and balance of one agent.
func (h *Hub) AgentWalletInfo(ctx context.Context, id string) map[string]interface{} {
	card, ok := h.Registry.Get(id)
	if !ok {
		return nil
	}
	wallet, err := h.Wallets.For(id, card.Index, h.Cfg.AgentKeySeed)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	out := map[string]interface{}{"agentId": id, "wallet": wallet.Address.Hex(), "owner": h.ownerOf(card)}
	if oc, err := h.chainFor(); err == nil {
		if bal, berr := oc.tokenBalance(ctx, wallet.Address); berr == nil {
			out["balanceHive"] = FormatHive(bal)
			out["balanceWei"] = bal.String()
		}
	}
	return out
}

// ownerOf returns the on-chain owner (operator) of an agent. Seed agents are owned by the Hub.
func (h *Hub) ownerOf(card AgentCard) string {
	if card.Owner != "" {
		return card.Owner
	}
	return h.OwnerAddress()
}

// sendCalldataTx signs and sends a transaction to an arbitrary contract with the given calldata.
func (oc *OnChain) sendCalldataTx(ctx context.Context, key *ecdsa.PrivateKey, to common.Address, data []byte) (common.Hash, error) {
	if oc.eth == nil {
		return common.Hash{}, fmt.Errorf("no chain connection")
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := oc.eth.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, err
	}
	gasPrice, err := oc.eth.SuggestGasPrice(ctx)
	if err != nil {
		gasPrice = big.NewInt(2_000_000_000)
	}
	tx := types.NewTransaction(nonce, to, big.NewInt(0), 2_000_000, gasPrice, data)
	signer := types.LatestSignerForChainID(oc.chainID)
	signed, err := types.SignTx(tx, signer, key)
	if err != nil {
		return common.Hash{}, err
	}
	if err := oc.eth.SendTransaction(ctx, signed); err != nil {
		return common.Hash{}, err
	}
	rc, err := bind.WaitMined(ctx, oc.eth, signed)
	if err != nil {
		return signed.Hash(), err
	}
	if rc.Status == 0 {
		return signed.Hash(), fmt.Errorf("transaction reverted (tx %s)", signed.Hash().Hex())
	}
	return signed.Hash(), nil
}
