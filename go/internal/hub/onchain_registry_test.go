package hub

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestSelectors verifies the hard-coded selectors match keccak256 of the signatures (a wrong
// selector silently breaks every on-chain call, which is exactly the price-set failure).
func TestSelectors(t *testing.T) {
	cases := []struct {
		sig string
		got []byte
	}{
		{"updatePrice(uint256)", updatePriceSelector},
		{"isRegistered(address)", isRegisteredSelector},
		{"registerAgent(address,int256[8],uint256,string,uint256,bytes)", registerAgentSelector},
	}
	for _, c := range cases {
		want := crypto.Keccak256([]byte(c.sig))[:4]
		if !bytes.Equal(want, c.got) {
			t.Fatalf("selector for %s = 0x%s, want 0x%s", c.sig, hex.EncodeToString(c.got), hex.EncodeToString(want))
		}
	}
}

// TestRegisterAgentCalldata decodes the calldata to confirm the ABI layout: selector, signer,
// zero capability, price, endpoint string, deadline and an empty signature blob.
func TestRegisterAgentCalldata(t *testing.T) {
	op := common.HexToAddress("0x732784b937C58efEb0914831c088d2c02dD6Db7c")
	price := big.NewInt(123456789)
	ep := "http://127.0.0.1:9500"
	data := registerAgentCalldata(op, price, ep)
	if len(data) < 4 || !bytes.Equal(data[:4], registerAgentSelector) {
		t.Fatalf("missing registerAgent selector")
	}
	if len(data)%32 != 4 {
		t.Fatalf("calldata length %d is not 4 + n*32", len(data))
	}
	word := func(i int) []byte { return data[4+i*32 : 4+(i+1)*32] }
	if got := common.BytesToAddress(word(0)); got != op {
		t.Fatalf("signer = %s, want %s", got.Hex(), op.Hex())
	}
	for i := 1; i <= 8; i++ {
		if new(big.Int).SetBytes(word(i)).Sign() != 0 {
			t.Fatalf("capability word %d not zero", i)
		}
	}
	if got := new(big.Int).SetBytes(word(9)); got.Cmp(price) != 0 {
		t.Fatalf("price = %s, want %s", got, price)
	}
	endpointOff := int(new(big.Int).SetBytes(word(10)).Int64())
	if 4+endpointOff+32 > len(data) {
		t.Fatalf("endpoint offset %d out of range", endpointOff)
	}
	elen := int(new(big.Int).SetBytes(data[4+endpointOff : 4+endpointOff+32]).Int64())
	gotEp := string(data[4+endpointOff+32 : 4+endpointOff+32+elen])
	if gotEp != ep {
		t.Fatalf("endpoint = %q, want %q", gotEp, ep)
	}
	if new(big.Int).SetBytes(word(11)).Sign() == 0 {
		t.Fatalf("deadline must be set in the future")
	}
}
