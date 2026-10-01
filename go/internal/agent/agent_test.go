package agent

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	hivecrypto "github.com/neural-hive/hive-node/internal/crypto"
)

func TestDeriveKeyIsDeterministicAndDistinct(t *testing.T) {
	a, err := DeriveKey("seed", 0)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	b, _ := DeriveKey("seed", 0)
	c, _ := DeriveKey("seed", 1)
	if crypto.PubkeyToAddress(a.PublicKey) != crypto.PubkeyToAddress(b.PublicKey) {
		t.Fatalf("the same seed and index must derive the same key")
	}
	if crypto.PubkeyToAddress(a.PublicKey) == crypto.PubkeyToAddress(c.PublicKey) {
		t.Fatalf("different indices must derive different keys")
	}
}

func TestDeterministicAnswersAndByzantineShift(t *testing.T) {
	if DeterministicValue("risk-scoring") != DeterministicValue("risk-scoring") {
		t.Fatalf("the canonical answer must be deterministic")
	}
	if DeterministicValue("risk-scoring") == DeterministicValue("moderation") {
		t.Fatalf("different task keys should map to different answers")
	}
	if got := ByzantineValue("risk-scoring") - DeterministicValue("risk-scoring"); got != 500000000 {
		t.Fatalf("the byzantine answer must be shifted far from the honest one, got %d", got)
	}
}

func TestHandleProducesAVerifiableSignedResponse(t *testing.T) {
	key, err := DeriveKey("neural-hive-devnet", 0)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	a := &Agent{Index: 0, Key: key, Address: addr, Skills: []string{"risk-scoring"}, Mode: ModeHonest}
	coord := common.HexToAddress("0x00000000000000000000000000000000000000cc")
	resp, err := a.Handle(big.NewInt(1337), coord, TaskRequest{RequestID: 1, StepID: 2, TaskKey: "risk-scoring", Deadline: 9999999999})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.Agent != addr {
		t.Fatalf("the response must be attributed to the agent")
	}
	if resp.Nonce == 0 {
		t.Fatalf("a fresh nonce must be assigned")
	}
	if resp.ValueInt64 != DeterministicValue("risk-scoring") {
		t.Fatalf("honest agents must return the canonical answer")
	}
	att := hivecrypto.Attestation{
		RequestID:   big.NewInt(1),
		StepID:      big.NewInt(2),
		Agent:       addr,
		OutputHash:  resp.OutputHash,
		OutputValue: resp.Value,
		Nonce:       new(big.Int).SetUint64(resp.Nonce),
		Deadline:    new(big.Int).SetUint64(resp.Deadline),
	}
	got, err := hivecrypto.RecoverResponse(big.NewInt(1337), coord, att, resp.Signature)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got != addr {
		t.Fatalf("the attestation must recover to the agent address, got %s", got.Hex())
	}
}

func TestHandleHonoursModeOverrideAndRefusal(t *testing.T) {
	key, _ := DeriveKey("neural-hive-devnet", 1)
	a := &Agent{Index: 1, Key: key, Address: crypto.PubkeyToAddress(key.PublicKey), Mode: ModeHonest}
	lying, err := a.Handle(big.NewInt(1337), common.Address{}, TaskRequest{RequestID: 1, StepID: 1, TaskKey: "explain", Deadline: 10, Mode: string(ModeLying)})
	if err != nil {
		t.Fatalf("lying handle: %v", err)
	}
	if lying.ValueInt64 != ByzantineValue("explain") {
		t.Fatalf("the per request mode override must make the agent lie")
	}
	if a.Mode != ModeHonest {
		t.Fatalf("a per request override must not persist on the agent")
	}
	a.Mode = ModeError
	if _, err := a.Handle(big.NewInt(1337), common.Address{}, TaskRequest{RequestID: 1, StepID: 1, TaskKey: "explain", Deadline: 10}); err != ErrRefused {
		t.Fatalf("an error mode agent must refuse the task, got %v", err)
	}
}

// stubAnswerer is a deterministic Answerer used to prove the agent attaches generated prose to
// a signed response when the request carries a prompt, and that it propagates answerer errors.
type stubAnswerer struct {
	err error
}

func (s *stubAnswerer) Answer(ctx context.Context, prompt string) (string, string, string, error) {
	if s.err != nil {
		return "", "", "", s.err
	}
	return "generated: " + prompt, "stub-model", "stub-provider", nil
}

func TestHandleAttachesGeneratedAnswer(t *testing.T) {
	key, _ := DeriveKey("neural-hive-devnet", 6)
	a := &Agent{
		Index:    6,
		Key:      key,
		Address:  crypto.PubkeyToAddress(key.PublicKey),
		Mode:     ModeHonest,
		Skills:   []string{"summarization"},
		Answerer: &stubAnswerer{},
	}
	resp, err := a.Handle(big.NewInt(1337), common.Address{}, TaskRequest{RequestID: 1, StepID: 1, TaskKey: "summarization", Prompt: "explain consensus", Deadline: 9999999999})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if resp.Answer != "generated: explain consensus" || resp.Model != "stub-model" || resp.Provider != "stub-provider" {
		t.Fatalf("answer not attached: %+v", resp)
	}
	if resp.ValueInt64 != DeterministicValue("summarization") {
		t.Fatalf("the numeric attestation must still be canonical: %d", resp.ValueInt64)
	}
}

func TestHandlePromptWithoutAnswererFails(t *testing.T) {
	key, _ := DeriveKey("neural-hive-devnet", 3)
	a := &Agent{Index: 3, Key: key, Address: crypto.PubkeyToAddress(key.PublicKey), Mode: ModeHonest}
	if _, err := a.Handle(big.NewInt(1337), common.Address{}, TaskRequest{RequestID: 1, StepID: 1, TaskKey: "summarization", Prompt: "x", Deadline: 10}); err != ErrNoAnswerer {
		t.Fatalf("err = %v, want ErrNoAnswerer", err)
	}
}

func TestHandlePropagatesAnswererError(t *testing.T) {
	key, _ := DeriveKey("neural-hive-devnet", 4)
	sentinel := errors.New("provider down")
	a := &Agent{Index: 4, Key: key, Address: crypto.PubkeyToAddress(key.PublicKey), Mode: ModeHonest, Answerer: &stubAnswerer{err: sentinel}}
	if _, err := a.Handle(big.NewInt(1337), common.Address{}, TaskRequest{RequestID: 1, StepID: 1, TaskKey: "summarization", Prompt: "x", Deadline: 10}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the answerer error", err)
	}
}
