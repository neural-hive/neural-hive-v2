// Package hivecrypto implements the EIP-712 typed-data attestations used to authenticate
// external AI-agent responses to the on-chain protocol.
//
// A blockchain cannot trust an arbitrary HTTP reply from an agent. Neural Hive therefore
// requires every agent to bind a secp256k1 signing key to its on-chain identity at
// registration time (CapabilityRegistry), and to sign each task response over a
// domain-separated digest. The TaskCoordinator recovers the signer and rejects the response
// unless it matches the registered key. Nonces and deadlines give replay protection.
package hivecrypto

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
)

const (
	domainTypeName   = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	responseType     = "ResponseAttestation(uint256 requestId,uint256 stepId,address agent,bytes32 outputHash,int256 outputValue,uint256 nonce,uint256 deadline)"
	registrationType = "AgentRegistration(address operator,address signer,uint256 nonce,uint256 deadline)"
	trustType        = "TrustSignal(address source,address subject,uint256 epoch,uint256 rating)"
	protocolName     = "NeuralHive"
	protocolVersion  = "1"
)

// Attestation is the typed message an agent signs for a task step.
type Attestation struct {
	RequestID   *big.Int
	StepID      *big.Int
	Agent       common.Address
	OutputHash  [32]byte
	OutputValue *big.Int // signed 256-bit integer
	Nonce       *big.Int
	Deadline    *big.Int
}

func padUint256(x *big.Int) []byte { return common.LeftPadBytes(x.Bytes(), 32) }

// padInt256 returns the 32-byte two-s complement encoding of a signed integer.
func padInt256(x *big.Int) []byte {
	if x.Sign() >= 0 {
		return common.LeftPadBytes(x.Bytes(), 32)
	}
	mod := new(big.Int).Lsh(big.NewInt(1), 256)
	twos := new(big.Int).Add(x, mod)
	return common.LeftPadBytes(twos.Bytes(), 32)
}

func padAddress(a common.Address) []byte { return common.LeftPadBytes(a.Bytes(), 32) }

// DomainSeparator mirrors AttestationLib.domainSeparator on-chain.
func DomainSeparator(chainID *big.Int, verifyingContract common.Address) []byte {
	var buf []byte
	buf = append(buf, crypto.Keccak256([]byte(domainTypeName))...)
	buf = append(buf, crypto.Keccak256([]byte(protocolName))...)
	buf = append(buf, crypto.Keccak256([]byte(protocolVersion))...)
	buf = append(buf, padUint256(chainID)...)
	buf = append(buf, padAddress(verifyingContract)...)
	return crypto.Keccak256(buf)
}

// ResponseDigest computes the EIP-712 digest for a response attestation.
func ResponseDigest(chainID *big.Int, verifyingContract common.Address, a Attestation) []byte {
	var buf []byte
	buf = append(buf, crypto.Keccak256([]byte(responseType))...)
	buf = append(buf, padUint256(a.RequestID)...)
	buf = append(buf, padUint256(a.StepID)...)
	buf = append(buf, padAddress(a.Agent)...)
	buf = append(buf, a.OutputHash[:]...)
	buf = append(buf, padInt256(a.OutputValue)...)
	buf = append(buf, padUint256(a.Nonce)...)
	buf = append(buf, padUint256(a.Deadline)...)
	structHash := crypto.Keccak256(buf)
	return eip712(chainID, verifyingContract, structHash)
}

func eip712(chainID *big.Int, verifyingContract common.Address, structHash []byte) []byte {
	prefix := []byte{0x19, 0x01}
	out := append(prefix, DomainSeparator(chainID, verifyingContract)...)
	out = append(out, structHash...)
	return crypto.Keccak256(out)
}

// SignResponse signs a response attestation with the agent signing key.
// It returns a 65-byte [R || S || V] signature with V in {27, 28}, matching OpenZeppelin ECDSA.
func SignResponse(key *ecdsa.PrivateKey, chainID *big.Int, verifyingContract common.Address, a Attestation) ([]byte, error) {
	digest := ResponseDigest(chainID, verifyingContract, a)
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}

// RecoverResponse returns the address that produced a response signature.
func RecoverResponse(chainID *big.Int, verifyingContract common.Address, a Attestation, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("bad signature length %d", len(sig))
	}
	s := make([]byte, 65)
	copy(s, sig)
	if s[64] >= 27 {
		s[64] -= 27
	}
	pub, err := secp256k1.RecoverPubkey(ResponseDigest(chainID, verifyingContract, a), s)
	if err != nil {
		return common.Address{}, err
	}
	pk, err := crypto.UnmarshalPubkey(pub)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pk), nil
}

// RegistrationDigest computes the EIP-712 digest binding an operator to a signing key.
func RegistrationDigest(chainID *big.Int, verifyingContract, operator, signer common.Address, nonce, deadline *big.Int) []byte {
	var buf []byte
	buf = append(buf, crypto.Keccak256([]byte(registrationType))...)
	buf = append(buf, padAddress(operator)...)
	buf = append(buf, padAddress(signer)...)
	buf = append(buf, padUint256(nonce)...)
	buf = append(buf, padUint256(deadline)...)
	return eip712(chainID, verifyingContract, crypto.Keccak256(buf))
}

// SignRegistration signs the operator/signer binding with the signer key.
func SignRegistration(key *ecdsa.PrivateKey, chainID *big.Int, verifyingContract, operator, signer common.Address, nonce, deadline *big.Int) ([]byte, error) {
	digest := RegistrationDigest(chainID, verifyingContract, operator, signer, nonce, deadline)
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		return nil, err
	}
	sig[64] += 27
	return sig, nil
}

// RecoverRegistration returns the address that signed a registration binding.
func RecoverRegistration(chainID *big.Int, verifyingContract, operator, signer common.Address, nonce, deadline *big.Int, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("bad signature length %d", len(sig))
	}
	s := make([]byte, 65)
	copy(s, sig)
	if s[64] >= 27 {
		s[64] -= 27
	}
	pub, err := secp256k1.RecoverPubkey(RegistrationDigest(chainID, verifyingContract, operator, signer, nonce, deadline), s)
	if err != nil {
		return common.Address{}, err
	}
	pk, err := crypto.UnmarshalPubkey(pub)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pk), nil
}

// TrustDigest computes the EIP-712 digest for an off-chain trust signal.
func TrustDigest(chainID *big.Int, verifyingContract, source, subject common.Address, epoch, rating *big.Int) []byte {
	var buf []byte
	buf = append(buf, crypto.Keccak256([]byte(trustType))...)
	buf = append(buf, padAddress(source)...)
	buf = append(buf, padAddress(subject)...)
	buf = append(buf, padUint256(epoch)...)
	buf = append(buf, padUint256(rating)...)
	return eip712(chainID, verifyingContract, crypto.Keccak256(buf))
}

// OutputHashHasher canonicalises a numeric output for on-chain hashing, mirroring
// keccak256(abi.encode(value)) in the coordinator.
func OutputHashHasher(value *big.Int) [32]byte {
	var out [32]byte
	copy(out[:], crypto.Keccak256(padInt256(value)))
	return out
}

// Bytes32FromHash converts a hex string to a 32-byte array.
func Bytes32FromHash(h string) [32]byte {
	var out [32]byte
	b := common.FromHex(h)
	copy(out[:], b)
	return out
}
