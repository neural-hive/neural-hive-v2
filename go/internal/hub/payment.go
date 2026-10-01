package hub

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/neural-hive/hive-node/internal/config"
)

// Payment model
//
// A task is paid for in HIVE, the project's ERC-20 token, from the user's MetaMask wallet:
//
//  1. POST /quote classifies the task and prices it: cost = HIVE (agents required) x price per agent.
//  2. The browser asks MetaMask to send an ERC-20 transfer(treasury, cost) on the HiveToken.
//  3. POST /task carries the quote id and the transaction hash. The Hub reads the receipt from the
//     chain and only runs the agents if the HiveToken emitted a Transfer of at least the quoted
//     cost to the treasury. Each transaction hash and each quote can be used exactly once.
//
// The Hub never holds a user key and never trusts the browser's claim that it paid.

const hiveSymbol = "HIVE"

var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// Quote is a price offer for one specific task text.
type Quote struct {
	ID         string
	Task       string
	HIVE       int
	Complexity string
	CostWei    *big.Int
	ExpiresAt  time.Time
	Used       bool
}

// Payments holds the quotes, the replay ledger and the chain connection used to verify payments.
type Payments struct {
	cfg config.Hub

	mu     sync.Mutex
	quotes map[string]*Quote
	used   map[string]bool
	ledger string

	ethMu sync.Mutex
	eth   *ethclient.Client
}

func newPayments(cfg config.Hub) *Payments {
	return &Payments{cfg: cfg, quotes: map[string]*Quote{}, used: map[string]bool{}}
}

// LoadLedger sets the replay-ledger file and loads the transaction hashes already spent, so a
// payment can never be reused after a Hub restart.
func (p *Payments) LoadLedger(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ledger = path
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var rec struct {
			TxHash string `json:"txHash"`
		}
		if json.Unmarshal([]byte(sc.Text()), &rec) == nil && rec.TxHash != "" {
			p.used[strings.ToLower(rec.TxHash)] = true
		}
	}
}

func (p *Payments) appendLedger(rec map[string]interface{}) {
	if p.ledger == "" {
		return
	}
	f, err := os.OpenFile(p.ledger, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rec)
	_, _ = f.Write(append(b, '\n'))
}

func newQuoteID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "q-" + hex.EncodeToString(b)
}

// Cost is the price of a task that needs the given number of agents.
func (h *Hub) Cost(agents int) *big.Int {
	price := h.Cfg.PricePerAgentWei
	if price == nil {
		price = big.NewInt(0)
	}
	return new(big.Int).Mul(price, big.NewInt(int64(agents)))
}

// Quote classifies the task, decides how many agents it needs and prices it.
func (h *Hub) Quote(text string, hiveOverride int) *Quote {
	text = strings.TrimSpace(text)
	cx := Classify(text)
	hive := h.decideHIVE(cx, SplitObjectives(text), hiveOverride)
	ttl := h.Cfg.QuoteTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	q := &Quote{ID: newQuoteID(), Task: text, HIVE: hive, Complexity: cx.Level, CostWei: h.Cost(hive), ExpiresAt: time.Now().Add(ttl)}
	h.Pay.mu.Lock()
	for id, old := range h.Pay.quotes {
		if time.Now().After(old.ExpiresAt) || old.Used {
			delete(h.Pay.quotes, id)
		}
	}
	h.Pay.quotes[q.ID] = q
	h.Pay.mu.Unlock()
	return q
}

// FormatHive renders a base-unit amount as a decimal HIVE string without trailing zeros.
func FormatHive(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	whole, rem := new(big.Int).QuoRem(wei, unit, new(big.Int))
	if rem.Sign() == 0 {
		return whole.String()
	}
	frac := rem.String()
	frac = strings.Repeat("0", 18-len(frac)) + frac
	frac = strings.TrimRight(frac, "0")
	return whole.String() + "." + frac
}

// paymentRecord builds the informational payment record used when no payment is taken.
func (h *Hub) paymentRecord(mode string, agents int) *Payment {
	cost := h.Cost(agents)
	return &Payment{
		Mode: mode, Agents: agents, PricePerAgent: h.Cfg.PricePerAgent,
		PricePerAgentWei: h.priceWeiString(),
		CostHive:         FormatHive(cost), CostWei: cost.String(),
		PaidHive: "0", PaidWei: "0", Symbol: hiveSymbol,
		Treasury: h.Cfg.Treasury, Token: h.Cfg.TokenAddress, ChainID: h.Cfg.ChainID,
	}
}

// PaymentError carries the HTTP status the handler should answer with.
type PaymentError struct {
	Status int
	Msg    string
}

func (e *PaymentError) Error() string { return e.Msg }

func payErr(status int, format string, a ...interface{}) *PaymentError {
	return &PaymentError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

func (p *Payments) client() (*ethclient.Client, error) {
	p.ethMu.Lock()
	defer p.ethMu.Unlock()
	if p.eth != nil {
		return p.eth, nil
	}
	c, err := ethclient.Dial(p.cfg.RPCURL)
	if err != nil {
		return nil, err
	}
	p.eth = c
	return c, nil
}

// Settle verifies the on-chain payment that backs a quote and consumes both the quote and the
// transaction hash. It returns the Payment record to attach to the task and the agent count that
// was paid for.
func (h *Hub) Settle(ctx context.Context, quoteID, txHash, task string) (*Payment, int, error) {
	p := h.Pay
	quoteID = strings.TrimSpace(quoteID)
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if quoteID == "" || txHash == "" {
		return nil, 0, payErr(402, "payment required: send %s HIVE per agent from MetaMask, then submit the quote id and transaction hash", h.Cfg.PricePerAgent)
	}
	if len(txHash) != 66 || !strings.HasPrefix(txHash, "0x") {
		return nil, 0, payErr(400, "txHash must be a 32-byte 0x-prefixed transaction hash")
	}
	if _, err := hex.DecodeString(txHash[2:]); err != nil {
		return nil, 0, payErr(400, "txHash is not valid hex")
	}

	p.mu.Lock()
	q, ok := p.quotes[quoteID]
	switch {
	case !ok:
		p.mu.Unlock()
		return nil, 0, payErr(402, "unknown or expired quote; request a new quote and pay again")
	case q.Used:
		p.mu.Unlock()
		return nil, 0, payErr(402, "quote already used")
	case time.Now().After(q.ExpiresAt):
		p.mu.Unlock()
		return nil, 0, payErr(402, "quote expired; request a new quote")
	case strings.TrimSpace(task) != q.Task:
		p.mu.Unlock()
		return nil, 0, payErr(400, "task text does not match the quoted task")
	case p.used[txHash]:
		p.mu.Unlock()
		return nil, 0, payErr(402, "this transaction has already paid for a task")
	}
	p.mu.Unlock()

	payer, paid, block, err := p.verify(ctx, txHash, common.HexToAddress(h.Cfg.Treasury), common.HexToAddress(h.Cfg.TokenAddress), q.CostWei)
	if err != nil {
		return nil, 0, err
	}

	p.mu.Lock()
	if q.Used || p.used[txHash] {
		p.mu.Unlock()
		return nil, 0, payErr(402, "payment already consumed")
	}
	q.Used = true
	p.used[txHash] = true
	p.appendLedger(map[string]interface{}{
		"txHash": txHash, "quoteId": q.ID, "payer": payer.Hex(), "paidWei": paid.String(),
		"agents": q.HIVE, "at": time.Now().UTC().Format(time.RFC3339),
	})
	p.mu.Unlock()

	pay := h.paymentRecord("onchain", q.HIVE)
	pay.Verified = true
	pay.Payer = payer.Hex()
	pay.TxHash = txHash
	pay.BlockNumber = block
	pay.PaidWei = paid.String()
	pay.PaidHive = FormatHive(paid)
	pay.QuoteID = q.ID
	return pay, q.HIVE, nil
}

// verify reads the receipt and requires a HiveToken Transfer of at least cost to the treasury.
func (p *Payments) verify(ctx context.Context, txHash string, treasury, token common.Address, cost *big.Int) (common.Address, *big.Int, uint64, error) {
	eth, err := p.client()
	if err != nil {
		return common.Address{}, nil, 0, payErr(503, "cannot reach the chain RPC to verify the payment: %v", err)
	}
	hash := common.HexToHash(txHash)
	var found bool
	var status uint64
	var block uint64
	var payer common.Address
	var paid *big.Int
	deadline := time.Now().Add(45 * time.Second)
	for {
		receipt, rerr := eth.TransactionReceipt(ctx, hash)
		if rerr == nil {
			found = true
			status = receipt.Status
			if receipt.BlockNumber != nil {
				block = receipt.BlockNumber.Uint64()
			}
			for _, lg := range receipt.Logs {
				if lg.Address != token || len(lg.Topics) != 3 || lg.Topics[0] != transferTopic {
					continue
				}
				if common.BytesToAddress(lg.Topics[2].Bytes()) != treasury {
					continue
				}
				val := new(big.Int).SetBytes(lg.Data)
				if val.Cmp(cost) >= 0 {
					payer = common.BytesToAddress(lg.Topics[1].Bytes())
					paid = val
					break
				}
			}
			break
		}
		if !errors.Is(rerr, ethereum.NotFound) {
			return common.Address{}, nil, 0, payErr(503, "could not read the payment receipt: %v", rerr)
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return common.Address{}, nil, 0, payErr(503, "payment verification cancelled")
		case <-time.After(1500 * time.Millisecond):
		}
	}
	if !found {
		return common.Address{}, nil, 0, payErr(402, "transaction %s was not found on chain yet; wait for it to be mined and retry", txHash)
	}
	if status != 1 {
		return common.Address{}, nil, 0, payErr(402, "the payment transaction failed on chain")
	}
	if paid == nil {
		return common.Address{}, nil, 0, payErr(402, "the transaction did not transfer at least %s HIVE to the treasury %s", FormatHive(cost), treasury.Hex())
	}
	return payer, paid, block, nil
}
