package hub

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type AgentWalletBook struct {
	mu      sync.Mutex
	byID    map[string]common.Address
	factory common.Address
	hive    common.Address
}

func newAgentWalletBook(factory, hive string) *AgentWalletBook {
	return &AgentWalletBook{byID: map[string]common.Address{}, factory: common.HexToAddress(factory), hive: common.HexToAddress(hive)}
}

func (b *AgentWalletBook) Enabled() bool {
	return b != nil && b.factory != (common.Address{})
}

func (b *AgentWalletBook) cacheGet(id string) (common.Address, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.byID[id]
	return a, ok
}

func (b *AgentWalletBook) cacheSet(id string, a common.Address) {
	b.mu.Lock()
	b.byID[id] = a
	b.mu.Unlock()
}

// walletOfCalldata encodes AgentWalletFactory.walletOf(bytes32).
func walletOfCalldata(agentID string) []byte {
	data := append([]byte{0x09, 0x52, 0x14, 0x58}, keccak32(agentID)...)
	return data
}

// createWalletCalldata encodes AgentWalletFactory.createWallet(address,string).
func createWalletCalldata(owner common.Address, agentID string) []byte {
	data := []byte{0x3f, 0x8f, 0x7e, 0x5a}
	data = append(data, common.LeftPadBytes(owner.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(64).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(int64(len(agentID))).Bytes(), 32)...)
	b := []byte(agentID)
	for len(b)%32 != 0 {
		b = append(b, 0)
	}
	data = append(data, b...)
	return data
}

// keccak32 returns the 32-byte keccak-256 hash of s (used as the mapping key).
func keccak32(s string) []byte {
	h := crypto.Keccak256([]byte(s))
	return h
}

// withdrawCalldata encodes AgentWallet.withdraw().
func withdrawCalldata() []byte { return []byte{0x3c, 0xcf, 0xd6, 0x0b} }

// withdrawToCalldata encodes AgentWallet.withdrawTo(address).
func withdrawToCalldata(to common.Address) []byte {
	return append([]byte{0x72, 0xb0, 0xd9, 0x0c}, common.LeftPadBytes(to.Bytes(), 32)...)
}

// readWalletOf reads AgentWalletFactory.walletOf(agentId) from the chain.
func (b *AgentWalletBook) readWalletOf(ctx context.Context, oc *OnChain, agentID string) (common.Address, error) {
	if oc == nil || oc.eth == nil {
		return common.Address{}, fmt.Errorf("no chain connection")
	}
	to := b.factory
	msg := ethereum.CallMsg{From: common.Address{}, To: &to, Data: walletOfCalldata(agentID)}
	res, err := oc.eth.CallContract(ctx, msg, nil)
	if err != nil {
		return common.Address{}, err
	}
	if len(res) < 32 {
		return common.Address{}, nil
	}
	return common.BytesToAddress(res[12:32]), nil
}

// EnsureAgentWallet returns the AgentWallet smart-contract address of an agent, deploying it
// through the factory with owner as the on-chain owner when it does not exist yet. The deploy is a
// real transaction signed by the Hub key; the address is deterministic per agent id.
func (h *Hub) EnsureAgentWallet(ctx context.Context, agentID, owner string) (common.Address, error) {
	b := h.WalletFactory
	if !b.Enabled() {
		return common.Address{}, fmt.Errorf("no AgentWalletFactory configured")
	}
	if a, ok := b.cacheGet(agentID); ok {
		return a, nil
	}
	oc, err := h.chainFor()
	if err != nil {
		return common.Address{}, err
	}
	existing, err := b.readWalletOf(ctx, oc, agentID)
	if err != nil {
		return common.Address{}, err
	}
	if existing != (common.Address{}) {
		b.cacheSet(agentID, existing)
		return existing, nil
	}
	key, err := h.hubKey()
	if err != nil {
		return common.Address{}, err
	}
	ownerAddr := common.HexToAddress(strings.TrimSpace(owner))
	if ownerAddr == (common.Address{}) {
		ownerAddr = crypto.PubkeyToAddress(key.PublicKey)
	}
	txHash, err := oc.sendCalldataTx(ctx, key, b.factory, createWalletCalldata(ownerAddr, agentID))
	if err != nil {
		return common.Address{}, err
	}
	_ = txHash
	created, err := b.readWalletOf(ctx, oc, agentID)
	if err != nil {
		return common.Address{}, err
	}
	if created == (common.Address{}) {
		return common.Address{}, fmt.Errorf("factory did not record a wallet for %s", agentID)
	}
	b.cacheSet(agentID, created)
	return created, nil
}

// AgentWalletInfo reports the per-agent AgentWallet smart-contract address, its on-chain owner and
// its HIVE balance. This is the contract that holds the agent earnings.
func (h *Hub) AgentWalletContractInfo(ctx context.Context, id string) map[string]interface{} {
	card, ok := h.Registry.Get(id)
	if !ok {
		return nil
	}
	owner := h.ownerOf(card)
	w, err := h.EnsureAgentWallet(ctx, id, owner)
	if err != nil {
		return map[string]interface{}{"agentId": id, "owner": owner, "error": err.Error()}
	}
	out := map[string]interface{}{"agentId": id, "wallet": w.Hex(), "contract": w.Hex(), "owner": owner}
	if oc, oerr := h.chainFor(); oerr == nil {
		if bal, berr := oc.tokenBalance(ctx, w); berr == nil {
			out["balanceHive"] = FormatHive(bal)
			out["balanceWei"] = bal.String()
		}
		out["owner"] = h.walletOwner(ctx, oc, w, owner)
	}
	return out
}

// EnrichAgentIdentity fills the unique on-chain identity (AgentWallet smart contract address) and
// the HIVE balance of one agent into the registry card. It is cheap: the contract address is read
// once (and cached), then only a single token balance call is made. Used by the agents board so the
// UI can show each agent unique contract id and its on-chain balance.
func (h *Hub) EnrichAgentIdentity(ctx context.Context, id string) {
	card, ok := h.Registry.Get(id)
	if !ok {
		return
	}
	owner := h.ownerOf(card)
	w, err := h.EnsureAgentWallet(ctx, id, owner)
	if err != nil {
		return
	}
	h.Registry.SetContractIdentity(id, w.Hex())
	if oc, oerr := h.chainFor(); oerr == nil {
		if bal, berr := oc.tokenBalance(ctx, w); berr == nil {
			h.Registry.SetBalance(id, FormatHive(bal), bal.String())
		}
	}
}

// EnrichAllAgentIdentities refreshes the on-chain contract identity and HIVE balance of every
// registered agent. It is bounded so a slow chain cannot stall the request.
func (h *Hub) EnrichAllAgentIdentities(ctx context.Context) {
	for _, card := range h.Registry.Snapshot() {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		h.EnrichAgentIdentity(cctx, card.ID)
		cancel()
	}
}

// walletOwner reads AgentWallet.owner() from the chain, falling back to the registry owner.
func (h *Hub) walletOwner(ctx context.Context, oc *OnChain, w common.Address, fallback string) string {
	if oc == nil || oc.eth == nil {
		return fallback
	}
	to := w
	msg := ethereum.CallMsg{From: common.Address{}, To: &to, Data: []byte{0x8d, 0xa5, 0xcb, 0x5b}}
	res, err := oc.eth.CallContract(ctx, msg, nil)
	if err != nil || len(res) < 32 {
		return fallback
	}
	a := common.BytesToAddress(res[12:32])
	if a == (common.Address{}) {
		return fallback
	}
	return a.Hex()
}

// WithdrawTxData returns the AgentWallet.withdraw() call the agent owner signs from MetaMask. Only
// the on-chain owner of the AgentWallet can execute it; a different sender reverts on chain.
func (h *Hub) WithdrawTxData(ctx context.Context, id, owner string) (map[string]interface{}, error) {
	card, ok := h.Registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown agent")
	}
	ownerAddr := common.HexToAddress(strings.TrimSpace(owner))
	if ownerAddr == (common.Address{}) {
		return nil, fmt.Errorf("owner address is required")
	}
	w, err := h.EnsureAgentWallet(ctx, id, h.ownerOf(card))
	if err != nil {
		return nil, err
	}
	bal := "0"
	if oc, oerr := h.chainFor(); oerr == nil {
		if b, berr := oc.tokenBalance(ctx, w); berr == nil {
			bal = FormatHive(b)
		}
	}
	data := withdrawCalldata()
	return map[string]interface{}{
		"ok": true, "agentId": id, "to": w.Hex(), "contract": w.Hex(),
		"owner": h.ownerOf(card), "from": ownerAddr.Hex(),
		"balanceHive": bal,
		"method":      "withdraw()", "selector": "withdraw()",
		"chainId": h.Cfg.ChainID, "chainIdHex": fmt.Sprintf("0x%x", h.Cfg.ChainID), "rpcUrl": h.Cfg.RPCURL,
		"data": "0x" + hexOf(data),
	}, nil
}

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}

// VerifyOwnerWithdraw confirms that a withdraw transaction really called the agent AgentWallet from
// its owner and succeeded, then reads the resulting (empty) balance.
func (h *Hub) VerifyOwnerWithdraw(ctx context.Context, id, owner, txHash string) (map[string]interface{}, error) {
	card, ok := h.Registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown agent")
	}
	oc, err := h.chainFor()
	if err != nil {
		return nil, err
	}
	w, err := h.EnsureAgentWallet(ctx, id, h.ownerOf(card))
	if err != nil {
		return nil, err
	}
	txHash = strings.TrimSpace(txHash)
	if txHash == "" {
		return nil, fmt.Errorf("txHash is required")
	}
	tx, _, err := oc.eth.TransactionByHash(ctx, common.HexToHash(txHash))
	if err != nil {
		return nil, fmt.Errorf("cannot read the withdraw transaction: %w", err)
	}
	if tx.To() == nil || *tx.To() != w {
		return nil, fmt.Errorf("the transaction does not target the agent wallet %s", w.Hex())
	}
	_ = owner
	rc, err := oc.eth.TransactionReceipt(ctx, common.HexToHash(txHash))
	if err != nil {
		return nil, fmt.Errorf("the withdraw transaction is not mined yet")
	}
	if rc.Status == 0 {
		return nil, fmt.Errorf("the withdraw transaction reverted on chain (only the agent owner can withdraw)")
	}
	bal := big.NewInt(0)
	if b, berr := oc.tokenBalance(ctx, w); berr == nil {
		bal = b
	}
	return map[string]interface{}{
		"ok": true, "agentId": id, "txHash": txHash, "wallet": w.Hex(),
		"contract": w.Hex(), "from": strings.ToLower(chainIDSender(tx).Hex()),
		"owner": h.ownerOf(card), "balanceHive": FormatHive(bal), "blockNumber": rc.BlockNumber.Uint64(),
	}, nil
}

// chainIDSender recovers the sender of a transaction signed for the configured chain id.
func chainIDSender(tx *types.Transaction) common.Address {
	if tx == nil {
		return common.Address{}
	}
	signer := types.LatestSignerForChainID(tx.ChainId())
	from, err := types.Sender(signer, tx)
	if err != nil {
		return common.Address{}
	}
	return from
}

// EnsureAllAgentWallets deploys the AgentWallet smart contract of every registered agent that does
// not have one yet, so each agent has its own on-chain wallet holding its HIVE. It logs and skips
// an agent whose deployment fails rather than aborting startup.
func (h *Hub) EnsureAllAgentWallets(ctx context.Context) {
	if h.WalletFactory == nil || !h.WalletFactory.Enabled() {
		return
	}
	for _, card := range h.Registry.Snapshot() {
		aid := card.ID
		owner := h.ownerOf(card)
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		if _, err := h.EnsureAgentWallet(cctx, aid, owner); err != nil {
			logf("agent-wallet %s: %v", aid, err)
		}
		cancel()
	}
}

// logf writes a hub log line without pulling in the log package name collision.
func logf(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
}
