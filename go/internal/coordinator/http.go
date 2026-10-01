package coordinator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/neural-hive/hive-node/internal/agent"
)

// RelayRequest is the relayer wire format for a signed agent response.
type RelayRequest struct {
	StepID      uint64   `json:"stepId"`
	Agent       string   `json:"agent"`
	OutputHash  [32]byte `json:"outputHash"`
	OutputValue string   `json:"outputValue"`
	Nonce       uint64   `json:"nonce"`
	Deadline    uint64   `json:"deadline"`
	Signature   []byte   `json:"signature"`
}

// RelayResponse is what the relayer returns.
type RelayResponse struct {
	OK     bool   `json:"ok"`
	TxHash string `json:"txHash,omitempty"`
	Error  string `json:"error,omitempty"`
}

// defaultAgentTimeout bounds an ordinary (deterministic) step dispatch.
const defaultAgentTimeout = 20 * time.Second

// callAgent performs the HTTP task dispatch to an external agent process. timeout bounds the
// round trip: a model-backed agent needs a much larger budget than the deterministic mocks.
func callAgent(endpoint string, req agent.TaskRequest, timeout time.Duration) (*agent.TaskResponse, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("agent has no endpoint")
	}
	if timeout <= 0 {
		timeout = defaultAgentTimeout
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cl := &http.Client{Timeout: timeout}
	resp, err := cl.Post(strings.TrimRight(endpoint, "/")+"/task", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(raw))
	}
	var out agent.TaskResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// submitSignedResponse delivers a signed response on-chain, either through the relayer service or
// directly. The contract does not care who submits, only who signed.
func (c *Coordinator) submitSignedResponse(stepID uint64, r *agent.TaskResponse, opts RunOptions) error {
	nonce := new(big.Int).SetUint64(r.Nonce)
	deadline := new(big.Int).SetUint64(r.Deadline)
	if opts.RelayerURL != "" {
		rr := RelayRequest{
			StepID:      stepID,
			Agent:       r.Agent.Hex(),
			OutputHash:  r.OutputHash,
			OutputValue: r.Value.String(),
			Nonce:       r.Nonce,
			Deadline:    r.Deadline,
			Signature:   r.Signature,
		}
		body, _ := json.Marshal(rr)
		cl := &http.Client{Timeout: 120 * time.Second}
		resp, err := cl.Post(strings.TrimRight(opts.RelayerURL, "/")+"/submit", "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out RelayResponse
		_ = json.Unmarshal(raw, &out)
		if !out.OK {
			return fmt.Errorf("relayer rejected: %s", out.Error)
		}
		return nil
	}
	return c.Admin.SubmitResponse(stepID, r.Agent, r.OutputHash, r.Value, nonce, deadline, r.Signature)
}

// CallAgent performs the HTTP task dispatch to an external agent process (exported for tools and tests).
func CallAgent(endpoint string, req agent.TaskRequest) (*agent.TaskResponse, error) {
	return callAgent(endpoint, req, defaultAgentTimeout)
}
