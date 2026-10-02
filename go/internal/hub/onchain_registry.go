package hub

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// On-chain agent identity for the Hub owner.
//
// The CapabilityRegistry keys every agent by its operator (msg.sender), and updatePrice(uint256)
// reverts with NotRegistered unless the caller is a registered operator. The seed agents are
// owned by the Hub key (the deployer), so for the owner to change a price from MetaMask the
// deployer address itself must exist in the registry. EnsureOwnerOnChain registers it once
// (idempotently) at startup, which is what makes the price-set transaction succeed.

// isRegisteredSelector is keccak256("isRegistered(address)")[:4].
var isRegisteredSelector = []byte{0xc3, 0xc5, 0xa5, 0x47}

// registerAgentSelector is keccak256("registerAgent(address,int256[8],uint256,string,uint256,bytes)")[:4].
var registerAgentSelector = []byte{0xc2, 0x88, 0xab, 0x42}

// ethCall performs a read-only contract call against the Hub RPC.
func (oc *OnChain) ethCall(ctx context.Context, to common.Address, data []byte) ([]byte, error) {
	if oc.eth == nil {
		return nil, fmt.Errorf("no chain connection")
	}
	msg := ethereum.CallMsg{From: common.Address{}, To: &to, Data: data}
	return oc.eth.CallContract(ctx, msg, nil)
}

// word32 left-pads b to 32 bytes.
func word32(b []byte) []byte { return common.LeftPadBytes(b, 32) }

// registryAddr is the configured CapabilityRegistry address (zero if unset).
func (h *Hub) registryAddr() common.Address {
	return common.HexToAddress(h.Cfg.RegistryAddress)
}

// encodeString abi-encodes a string value: a length word followed by the UTF-8 bytes padded to
// a 32-byte boundary (empty string encodes as a single zero length word).
func encodeString(s string) []byte {
	b := []byte(s)
	out := word32(big.NewInt(int64(len(b))).Bytes())
	if len(b) == 0 {
		return out
	}
	pad := (32 - len(b)%32) % 32
	return append(out, append(b, make([]byte, pad)...)...)
}

// registerAgentCalldata builds registerAgent(signer, int256[8]{0}, price, endpoint, deadline, "").
// The operator signs its own registration, so the signer equals msg.sender and the EIP-712
// signature may be empty. The capability vector is all-zero (it is refined off-chain) and the
// price is given in wei.
func registerAgentCalldata(operator common.Address, price *big.Int, endpoint string) []byte {
	if price == nil {
		price = big.NewInt(0)
	}
	head := make([]byte, 0, 4+13*32)
	head = append(head, registerAgentSelector...)
	head = append(head, word32(operator.Bytes())...)
	for i := 0; i < 8; i++ {
		head = append(head, word32(nil)...)
	}
	head = append(head, word32(price.Bytes())...)
	// 13 head words => the dynamic tail starts at byte 416; the string is first, then bytes.
	endpointOff := big.NewInt(13 * 32)
	head = append(head, word32(endpointOff.Bytes())...)
	head = append(head, word32(big.NewInt(time.Now().Add(time.Hour).Unix()).Bytes())...)
	endpointEnc := encodeString(endpoint)
	sigOff := int64(13*32) + int64(len(endpointEnc))
	head = append(head, word32(big.NewInt(sigOff).Bytes())...)
	body := append(endpointEnc, encodeString("")...)
	return append(head, body...)
}

// isRegisteredOnChain reports whether addr is a registered CapabilityRegistry operator.
func (h *Hub) isRegisteredOnChain(ctx context.Context, oc *OnChain, addr common.Address) (bool, error) {
	data := append(append([]byte{}, isRegisteredSelector...), word32(addr.Bytes())...)
	res, err := oc.ethCall(ctx, h.registryAddr(), data)
	if err != nil {
		return false, err
	}
	if len(res) < 32 {
		return false, nil
	}
	return new(big.Int).SetBytes(res).Sign() != 0, nil
}

// EnsureOwnerOnChain registers the Hub/deployer address in the CapabilityRegistry if it is not
// already present, so the owner can change agent prices from MetaMask (updatePrice(uint256)
// requires the caller to be a registered operator). It is idempotent and best-effort: a failure
// is returned to the caller, never fatal to the Hub.
func (h *Hub) EnsureOwnerOnChain(ctx context.Context) error {
	if strings.TrimSpace(h.Cfg.RegistryAddress) == "" {
		return fmt.Errorf("no CapabilityRegistry address configured")
	}
	key, err := h.hubKey()
	if err != nil {
		return err
	}
	operator := crypto.PubkeyToAddress(key.PublicKey)
	oc, err := h.chainFor()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ok, err := h.isRegisteredOnChain(cctx, oc, operator)
	if err != nil {
		return fmt.Errorf("registry read failed: %w", err)
	}
	if ok {
		return nil
	}
	price := big.NewInt(0)
	if h.Cfg.PricePerAgentWei != nil {
		price = h.Cfg.PricePerAgentWei
	}
	data := registerAgentCalldata(operator, price, "http://127.0.0.1:"+fmt.Sprintf("%d", h.Cfg.Port))
	sctx, scancel := context.WithTimeout(ctx, 90*time.Second)
	defer scancel()
	if _, err := oc.sendCalldataTx(sctx, key, h.registryAddr(), data); err != nil {
		return fmt.Errorf("registerAgent failed: %w", err)
	}
	return nil
}

// hiveToWei converts a HIVE amount given as a float to 18-decimal base units without the int64
// overflow of the obvious int64(v*1e18) form. Negative and sub-wei amounts clamp to 0.
func hiveToWei(v float64) *big.Int {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return big.NewInt(0)
	}
	r := new(big.Rat).SetFloat64(v)
	if r == nil {
		return big.NewInt(0)
	}
	unit := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	r.Mul(r, unit)
	return new(big.Int).Quo(r.Num(), r.Denom())
}
