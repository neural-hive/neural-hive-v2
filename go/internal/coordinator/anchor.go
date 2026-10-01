package coordinator

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// AnchorResult is the on-chain evidence produced when the Hub anchors a final answer hash.
type AnchorResult struct {
	OK          bool   `json:"ok"`
	RequestID   uint64 `json:"requestId"`
	RequestTx   string `json:"requestTx,omitempty"`
	SettleTx    string `json:"settleTx,omitempty"`
	Network     string `json:"network"`
	ChainID     int64  `json:"chainId"`
	Coordinator string `json:"coordinator"`
	Error       string `json:"error,omitempty"`
}

// Anchor records an externally produced answer hash on-chain, as the metaHash of a funded and
// immediately settled request. This is how the Neural Hive Hub keeps the protocol hash and
// blockchain evidence working for its multi-agent final answer, without re-running the
// deterministic protocol or fabricating any transaction.
func (c *Coordinator) Anchor(hexHash, label string) (*AnchorResult, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(hexHash), "0x")
	b, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("bad hash: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("bad hash length %d, want 32", len(b))
	}
	var meta [32]byte
	copy(meta[:], b)
	budget := hiveToWei(0.01)
	if err := c.Admin.ApproveHive(c.Admin.SettlementAddress(), budget); err != nil {
		return nil, fmt.Errorf("approve escrow: %w", err)
	}
	reqID, err := c.Admin.CreateRequest(meta, budget, 0)
	if err != nil {
		return nil, fmt.Errorf("create anchor request: %w", err)
	}
	res := &AnchorResult{OK: true, RequestID: reqID}
	res.RequestTx = c.Admin.LastTxHash().Hex()
	if err := c.Admin.SettleRequest(reqID); err != nil {
		res.OK = false
		res.Error = err.Error()
	} else {
		res.SettleTx = c.Admin.LastTxHash().Hex()
	}
	res.Network = c.Cfg.Network
	res.ChainID = c.Cfg.ChainID
	res.Coordinator = c.Admin.Address("TaskCoordinator").Hex()
	return res, nil
}
