package hivecrypto

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestSignAndRecoverResponse(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	coord := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	value := big.NewInt(-1234567)
	att := Attestation{
		RequestID:   big.NewInt(9),
		StepID:      big.NewInt(3),
		Agent:       addr,
		OutputHash:  OutputHashHasher(value),
		OutputValue: value,
		Nonce:       big.NewInt(1),
		Deadline:    big.NewInt(9999999999),
	}
	sig, err := SignResponse(key, big.NewInt(1337), coord, att)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("expected a 65 byte signature, got %d", len(sig))
	}
	got, err := RecoverResponse(big.NewInt(1337), coord, att, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got != addr {
		t.Fatalf("recovered %s, want %s", got.Hex(), addr.Hex())
	}
}

func TestResponseDoesNotRecoverOnAnotherChain(t *testing.T) {
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	coord := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	value := big.NewInt(5)
	att := Attestation{RequestID: big.NewInt(1), StepID: big.NewInt(2), Agent: addr, OutputHash: OutputHashHasher(value), OutputValue: value, Nonce: big.NewInt(1), Deadline: big.NewInt(99)}
	sig, err := SignResponse(key, big.NewInt(1337), coord, att)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := RecoverResponse(big.NewInt(1), coord, att, sig)
	if err == nil && got == addr {
		t.Fatalf("a chain 1337 attestation must not recover on chain 1")
	}
}

func TestMalformedSignatureIsRejected(t *testing.T) {
	att := Attestation{RequestID: big.NewInt(1)}
	if _, err := RecoverResponse(big.NewInt(1337), common.Address{}, att, []byte{1, 2, 3}); err == nil {
		t.Fatalf("a short signature must be rejected")
	}
}

func TestDomainSeparatorDependsOnChainAndContract(t *testing.T) {
	a := DomainSeparator(big.NewInt(1337), common.Address{})
	b := DomainSeparator(big.NewInt(1), common.Address{})
	if bytes.Equal(a, b) {
		t.Fatalf("the domain separator must depend on the chain id")
	}
	c := DomainSeparator(big.NewInt(1337), common.HexToAddress("0x0000000000000000000000000000000000000001"))
	if bytes.Equal(a, c) {
		t.Fatalf("the domain separator must depend on the verifying contract")
	}
}

func TestOutputHashMatchesAbiEncoding(t *testing.T) {
	var want [32]byte
	copy(want[:], crypto.Keccak256(common.LeftPadBytes(big.NewInt(7).Bytes(), 32)))
	if OutputHashHasher(big.NewInt(7)) != want {
		t.Fatalf("the output hash must equal keccak256(abi.encode(value))")
	}
}

func TestSignAndRecoverRegistration(t *testing.T) {
	signer, _ := crypto.GenerateKey()
	signerAddr := crypto.PubkeyToAddress(signer.PublicKey)
	op := common.HexToAddress("0x0000000000000000000000000000000000000111")
	coord := common.HexToAddress("0x0000000000000000000000000000000000000222")
	sig, err := SignRegistration(signer, big.NewInt(1337), coord, op, signerAddr, big.NewInt(0), big.NewInt(500))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := RecoverRegistration(big.NewInt(1337), coord, op, signerAddr, big.NewInt(0), big.NewInt(500), sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got != signerAddr {
		t.Fatalf("recovered %s, want %s", got.Hex(), signerAddr.Hex())
	}
}
