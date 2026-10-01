// Package agent implements a Neural Hive worker agent: an external process that advertises a
// capability, receives a step, computes an answer and signs an EIP-712 attestation that the
// on-chain TaskCoordinator can verify.
//
// The prototype mock agents do not run an LLM. They implement a deterministic compute function so
// that honest agents agree on the same answer (which is what Krum aggregates), while a
// misbehaving agent can be told to lie. That keeps integration tests reproducible while
// exercising the real byzantine-fault-tolerance path.
//
// An agent may additionally ship an Answerer. When the router asks for a natural-language
// answer (TaskRequest.Prompt is set) the agent delegates to the Answerer and returns the prose
// in TaskResponse.Answer. The Ollama agent is the first such agent; the numeric attestation is
// unchanged, so the on-chain consensus path keeps working exactly as before.
package agent

import (
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	hivecrypto "github.com/neural-hive/hive-node/internal/crypto"
)

// Mode selects the agent behaviour on a task.
type Mode string

const (
	// ModeHonest computes the canonical answer.
	ModeHonest Mode = "honest"
	// ModeLying returns a corrupted answer (used to test Krum tolerance).
	ModeLying Mode = "lying"
	// ModeError refuses to answer.
	ModeError Mode = "error"
)

// Answerer turns a natural-language prompt into a real answer. An agent that carries one is
// model-backed; an agent without one stays a deterministic mock worker.
type Answerer interface {
	// Answer returns the generated text together with the model and provider that produced it.
	Answer(ctx context.Context, prompt string) (text, model, provider string, err error)
}

// Agent is a worker identity.
type Agent struct {
	Index    int
	Key      *ecdsa.PrivateKey
	Address  common.Address
	Skills   []string
	Price    *big.Int
	Endpoint string
	Mode     Mode
	Nonce    uint64
	// Answerer is optional. When set, the agent answers natural-language prompts.
	Answerer Answerer
}

// DeriveKey returns a deterministic devnet key for agent index i.
func DeriveKey(seed string, i int) (*ecdsa.PrivateKey, error) {
	h := crypto.Keccak256([]byte("neural-hive/agent/" + seed + "/" + strconv.Itoa(i)))
	return crypto.ToECDSA(h)
}

// TaskRequest is what a router sends to an agent.
type TaskRequest struct {
	RequestID uint64  `json:"requestId"`
	StepID    uint64  `json:"stepId"`
	TaskKey   string  `json:"taskKey"`
	TaskVec   []int64 `json:"taskVec"`
	Nonce     uint64  `json:"nonce"`
	Deadline  uint64  `json:"deadline"`
	Mode      string  `json:"mode,omitempty"` // optional test override
	// Prompt, when non-empty, asks the agent for a natural-language answer to this text.
	Prompt string `json:"prompt,omitempty"`
}

// TaskResponse is a signed answer. Answer, Model and Provider are only set by model-backed
// agents (the Ollama agent) when the request carried a Prompt.
type TaskResponse struct {
	Agent      common.Address `json:"agent"`
	StepID     uint64         `json:"stepId"`
	Value      *big.Int       `json:"value"`
	ValueInt64 int64          `json:"valueInt64"`
	OutputHash [32]byte       `json:"outputHash"`
	Nonce      uint64         `json:"nonce"`
	Deadline   uint64         `json:"deadline"`
	Signature  []byte         `json:"signature"`
	Mode       string         `json:"mode"`
	Answer     string         `json:"answer,omitempty"`
	Model      string         `json:"model,omitempty"`
	Provider   string         `json:"provider,omitempty"`
}

// DeterministicValue is the canonical answer for a task key. Every honest agent computes the
// same number, which is exactly the property Krum relies on to isolate liars.
func DeterministicValue(taskKey string) int64 {
	h := crypto.Keccak256([]byte("neural-hive/output/" + taskKey))
	v := int64(binary.BigEndian.Uint64(h[:8]) % 1_000_000)
	return v * 1000
}

// ByzantineValue shifts an honest answer far enough that Krum scores it as an outlier.
func ByzantineValue(taskKey string) int64 {
	return DeterministicValue(taskKey) + 500_000_000
}

// Compute produces the answer for a task according to the agent mode.
func (a *Agent) Compute(taskKey string) (*big.Int, [32]byte) {
	var v int64
	switch a.Mode {
	case ModeLying:
		v = ByzantineValue(taskKey)
	default:
		v = DeterministicValue(taskKey)
	}
	value := big.NewInt(v)
	return value, hivecrypto.OutputHashHasher(value)
}

// NextNonce returns a fresh, monotonically increasing nonce for this agent.
func (a *Agent) NextNonce() uint64 {
	a.Nonce++
	return a.Nonce
}

// SignResponse signs an EIP-712 response attestation for the given chain and coordinator.
func (a *Agent) SignResponse(chainID *big.Int, coordinator common.Address, requestID, stepID uint64, value *big.Int, outputHash [32]byte, nonce, deadline uint64) ([]byte, error) {
	att := hivecrypto.Attestation{
		RequestID:   new(big.Int).SetUint64(requestID),
		StepID:      new(big.Int).SetUint64(stepID),
		Agent:       a.Address,
		OutputHash:  outputHash,
		OutputValue: value,
		Nonce:       new(big.Int).SetUint64(nonce),
		Deadline:    new(big.Int).SetUint64(deadline),
	}
	return hivecrypto.SignResponse(a.Key, chainID, coordinator, att)
}

// Handle executes a task request and returns a signed response. When the request carries a
// Prompt and the agent has an Answerer, the generated prose is attached to the response.
func (a *Agent) Handle(chainID *big.Int, coordinator common.Address, req TaskRequest) (*TaskResponse, error) {
	if req.Mode != "" {
		a.Mode = Mode(req.Mode)
	}
	if a.Mode == ModeError {
		return nil, ErrRefused
	}
	nonce := req.Nonce
	if nonce == 0 {
		nonce = a.NextNonce()
	}
	value, hash := a.Compute(req.TaskKey)
	sig, err := a.SignResponse(chainID, coordinator, req.RequestID, req.StepID, value, hash, nonce, req.Deadline)
	if err != nil {
		return nil, err
	}
	out := &TaskResponse{
		Agent:      a.Address,
		StepID:     req.StepID,
		Value:      value,
		ValueInt64: value.Int64(),
		OutputHash: hash,
		Nonce:      nonce,
		Deadline:   req.Deadline,
		Signature:  sig,
		Mode:       string(a.Mode),
	}
	if req.Mode != "" {
		a.Mode = ModeHonest
	}
	if strings.TrimSpace(req.Prompt) != "" {
		if a.Answerer == nil {
			return nil, ErrNoAnswerer
		}
		text, model, provider, aerr := a.Answerer.Answer(context.Background(), req.Prompt)
		if aerr != nil {
			return nil, aerr
		}
		out.Answer = text
		out.Model = model
		out.Provider = provider
	}
	return out, nil
}

// refusedError is returned by an agent configured to fail.
type refusedError struct{}

func (refusedError) Error() string { return "agent refused the task" }

// ErrRefused is the sentinel error for ModeError agents.
var ErrRefused error = refusedError{}

// noAnswererError is returned when a prompt is sent to an agent that cannot answer in prose.
type noAnswererError struct{}

func (noAnswererError) Error() string {
	return "agent has no answerer configured and cannot produce a natural-language answer"
}

// ErrNoAnswerer is the sentinel error for prompt requests to non-LLM agents.
var ErrNoAnswerer error = noAnswererError{}
