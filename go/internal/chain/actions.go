package chain

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// RoleHash mirrors keccak256(roleName) used by OpenZeppelin AccessControl.
func RoleHash(name string) [32]byte { return crypto.Keccak256Hash([]byte(name)) }

// Step mirrors TaskCoordinator.Step for decoding stepInfo.
type Step struct {
	RequestId      *big.Int
	TaskVec        [8]*big.Int
	Replication    uint32
	F              uint32
	Assigned       uint32
	Responded      uint32
	MaxPrice       *big.Int
	Deadline       uint64
	Status         uint8
	Winner         common.Address
	RunnerUp       common.Address
	ConsensusValue *big.Int
	ConsensusHash  [32]byte
	Payout         *big.Int
	Numeric        bool
	Settled        bool
}

// Step status constants mirrored from the contract.
const (
	StepPending    uint8 = 0
	StepCollecting uint8 = 1
	StepFinalized  uint8 = 2
	StepFailed     uint8 = 3
)

// Request status constants mirrored from the contract.
const (
	RequestOpen      uint8 = 0
	RequestSettled   uint8 = 1
	RequestCancelled uint8 = 2
)

// Verification tiers.
const (
	TierOptimistic uint8 = 0
	TierKrum       uint8 = 1
	TierZKML       uint8 = 2
)

// Request mirrors the tuple returned by requestInfo.
type Request struct {
	Requester      common.Address
	MetaHash       [32]byte
	Budget         *big.Int
	Spent          *big.Int
	Status         uint8
	Tier           uint8
	StepCount      *big.Int
	FinalizedCount *big.Int
}

// ---------------- token / admin helpers ----------------

// TransferEth sends native ETH from the client account (used to fund fresh agent keys).
// TransferEth sends native ETH (gas) to an address, retrying once on a stale nonce so it never
// fails because another Neural Hive process consumed a nonce first.
func (c *Client) TransferEth(to common.Address, wei *big.Int) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = c.transferEthOnce(to, wei); lastErr == nil || !isNonceError(lastErr) {
			return lastErr
		}
		c.mu.Lock()
		c.nonce = 0
		c.mu.Unlock()
		time.Sleep(150 * time.Millisecond)
	}
	return lastErr
}

func (c *Client) transferEthOnce(to common.Address, wei *big.Int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nonce, err := c.nextNonce()
	if err != nil {
		return err
	}
	tx := types.NewTransaction(nonce, to, wei, 21_000, big.NewInt(2_000_000_000), nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(c.ChainID), c.Key)
	if err != nil {
		return err
	}
	if err := c.Eth.SendTransaction(ctx, signed); err != nil {
		return err
	}
	c.nonce = nonce + 1
	return nil
}

// MintHive mints HIVE to an address (caller must hold MINTER_ROLE).
func (c *Client) MintHive(to common.Address, amount *big.Int) error {
	_, err := c.Send("HiveToken", "mint", to, amount)
	return err
}

// TransferHive sends HIVE from the client account.
func (c *Client) TransferHive(to common.Address, amount *big.Int) error {
	_, err := c.Send("HiveToken", "transfer", to, amount)
	return err
}

// HiveBalance reads an HIVE balance.
func (c *Client) HiveBalance(addr common.Address) (*big.Int, error) {
	var out *big.Int
	err := c.Call("HiveToken", "balanceOf", &out, addr)
	return out, err
}

// ApproveHive approves a spender (the settlement contract) to pull HIVE.
func (c *Client) ApproveHive(spender common.Address, amount *big.Int) error {
	_, err := c.Send("HiveToken", "approve", spender, amount)
	return err
}

// ---------------- registry ----------------

// GrantRole grants an OpenZeppelin role on one of the protocol contracts.
func (c *Client) GrantRole(contract, role string, account common.Address) error {
	_, err := c.Send(contract, "grantRole", RoleHash(role), account)
	return err
}

// RegisterAgent registers the caller as an agent with a bound signing key.
func (c *Client) RegisterAgent(capability [8]*big.Int, price *big.Int, endpoint string, deadline *big.Int, sig []byte) error {
	_, err := c.Send("CapabilityRegistry", "registerAgent", c.From, capability, price, endpoint, deadline, sig)
	return err
}

// IsRegistered reports whether an address is a registered agent.
func (c *Client) IsRegistered(agent common.Address) (bool, error) {
	var out bool
	err := c.Call("CapabilityRegistry", "isRegistered", &out, agent)
	return out, err
}

// SignerOf returns the response-signing key bound to an agent.
func (c *Client) SignerOf(agent common.Address) (common.Address, error) {
	var out common.Address
	err := c.Call("CapabilityRegistry", "signerOf", &out, agent)
	return out, err
}

// CapabilityOf returns the agent capability vector.
func (c *Client) CapabilityOf(agent common.Address) ([8]*big.Int, error) {
	var out [8]*big.Int
	err := c.Call("CapabilityRegistry", "capabilityOf", &out, agent)
	return out, err
}

// PriceOf returns the agent advertised price.
func (c *Client) PriceOf(agent common.Address) (*big.Int, error) {
	var out *big.Int
	err := c.Call("CapabilityRegistry", "priceOf", &out, agent)
	return out, err
}

// EndpointOf returns the agent HTTP endpoint.
func (c *Client) EndpointOf(agent common.Address) (string, error) {
	var out string
	err := c.Call("CapabilityRegistry", "endpointOf", &out, agent)
	return out, err
}

// AgentCount returns the number of registered agents.
func (c *Client) AgentCount() (uint64, error) {
	var out *big.Int
	err := c.Call("CapabilityRegistry", "agentCount", &out)
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// AllAgents returns every registered agent address.
func (c *Client) AllAgents() ([]common.Address, error) {
	var out []common.Address
	err := c.Call("CapabilityRegistry", "allAgents", &out)
	return out, err
}

// ---------------- staking / settlement ----------------

// Stake deposits HIVE as slashable stake.
func (c *Client) Stake(amount *big.Int) error {
	_, err := c.Send("StakingSettlement", "stake", amount)
	return err
}

// AvailableStake returns stake minus locked stake.
func (c *Client) AvailableStake(agent common.Address) (*big.Int, error) {
	var out *big.Int
	err := c.Call("StakingSettlement", "availableStake", &out, agent)
	return out, err
}

// StakeOf returns total stake of an agent.
func (c *Client) StakeOf(agent common.Address) (*big.Int, error) {
	var out *big.Int
	err := c.Call("StakingSettlement", "stakeOf", &out, agent)
	return out, err
}

// EscrowAvailable returns the remaining escrow of a request.
func (c *Client) EscrowAvailable(requestID uint64) (*big.Int, error) {
	var out *big.Int
	err := c.Call("StakingSettlement", "escrowAvailable", &out, big.NewInt(int64(requestID)))
	return out, err
}

// SettlementAddress returns the settlement contract address.
func (c *Client) SettlementAddress() common.Address { return c.addresses["StakingSettlement"] }

// ---------------- task lifecycle ----------------

// NextRequestID reads the id the next createRequest will use.
func (c *Client) NextRequestID() (uint64, error) {
	var out *big.Int
	err := c.Call("TaskCoordinator", "nextRequestId", &out)
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// NextStepID reads the id the next addStep will use.
func (c *Client) NextStepID() (uint64, error) {
	var out *big.Int
	err := c.Call("TaskCoordinator", "nextStepId", &out)
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// CreateRequest escrows a budget and returns the new request id.
func (c *Client) CreateRequest(metaHash [32]byte, budget *big.Int, tier uint8) (uint64, error) {
	id, err := c.NextRequestID()
	if err != nil {
		return 0, err
	}
	if _, err := c.Send("TaskCoordinator", "createRequest", metaHash, budget, tier); err != nil {
		return 0, err
	}
	return id, nil
}

// AddStep registers a step node and returns the new step id.
func (c *Client) AddStep(requestID uint64, taskVec [8]*big.Int, replication, f uint32, maxPrice *big.Int, deadline uint64, numeric bool) (uint64, error) {
	id, err := c.NextStepID()
	if err != nil {
		return 0, err
	}
	if _, err := c.Send("TaskCoordinator", "addStep", big.NewInt(int64(requestID)), taskVec, replication, f, maxPrice, deadline, numeric); err != nil {
		return 0, err
	}
	return id, nil
}

// AssignAgent assigns a step to a candidate agent at an agreed bid.
func (c *Client) AssignAgent(stepID uint64, agent common.Address, bid *big.Int) error {
	_, err := c.Send("TaskCoordinator", "assignAgent", big.NewInt(int64(stepID)), agent, bid)
	return err
}

// SubmitResponse relays a signed agent response on-chain. Permissionless by design.
func (c *Client) SubmitResponse(stepID uint64, agent common.Address, outputHash [32]byte, value *big.Int, nonce, deadline *big.Int, sig []byte) error {
	_, err := c.Send("TaskCoordinator", "submitResponse", big.NewInt(int64(stepID)), agent, outputHash, value, nonce, deadline, sig)
	return err
}

// FinalizeStep runs on-chain Krum over the collected responses.
func (c *Client) FinalizeStep(stepID uint64) error {
	_, err := c.Send("TaskCoordinator", "finalizeStep", big.NewInt(int64(stepID)))
	return err
}

// SettleRequest pays the winners by VCG and refunds unused escrow.
func (c *Client) SettleRequest(requestID uint64) error {
	_, err := c.Send("TaskCoordinator", "settleRequest", big.NewInt(int64(requestID)))
	return err
}

// StepInfo reads a step.
func (c *Client) StepInfo(stepID uint64) (Step, error) {
	var out Step
	err := c.Call("TaskCoordinator", "stepInfo", &out, big.NewInt(int64(stepID)))
	return out, err
}

// RequestInfo reads a request.
func (c *Client) RequestInfo(requestID uint64) (Request, error) {
	var out Request
	err := c.Call("TaskCoordinator", "requestInfo", &out, big.NewInt(int64(requestID)))
	return out, err
}

// ResponseCount returns how many signed responses a step has accepted.
func (c *Client) ResponseCount(stepID uint64) (uint64, error) {
	var out *big.Int
	err := c.Call("TaskCoordinator", "responseCount", &out, big.NewInt(int64(stepID)))
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// IsPaid reports whether an agent was paid for a step.
func (c *Client) IsPaid(stepID uint64, agent common.Address) (bool, error) {
	var out bool
	err := c.Call("TaskCoordinator", "isPaid", &out, big.NewInt(int64(stepID)), agent)
	return out, err
}

// IsNonceUsed reports whether an agent nonce has been consumed (replay protection check).
func (c *Client) IsNonceUsed(agent common.Address, nonce *big.Int) (bool, error) {
	var out bool
	err := c.Call("TaskCoordinator", "isNonceUsed", &out, agent, nonce)
	return out, err
}

// AnswerHash returns the aggregated answer hash of a request.
func (c *Client) AnswerHash(requestID uint64) ([32]byte, error) {
	var out [32]byte
	err := c.Call("TaskCoordinator", "answerHash", &out, big.NewInt(int64(requestID)))
	return out, err
}

// ---------------- reputation ----------------

// SubmitTrustSignal records a local trust observation on-chain.
func (c *Client) SubmitTrustSignal(subject common.Address, rating *big.Int) error {
	_, err := c.Send("ReputationRegistry", "submitTrustSignal", subject, rating)
	return err
}

// ProposeScores submits an EigenTrust score vector as an optimistically-verified batch.
func (c *Client) ProposeScores(agents []common.Address, scores []*big.Int) (uint64, error) {
	var next *big.Int
	if err := c.Call("ReputationRegistry", "nextBatchId", &next); err != nil {
		return 0, err
	}
	if _, err := c.Send("ReputationRegistry", "proposeScores", agents, scores); err != nil {
		return 0, err
	}
	return next.Uint64(), nil
}

// FinalizeScores applies a batch after its challenge window elapsed.
func (c *Client) FinalizeScores(batchID uint64) error {
	_, err := c.Send("ReputationRegistry", "finalizeScores", big.NewInt(int64(batchID)))
	return err
}

// EffectiveScore reads the routing-relevant reputation of an agent.
func (c *Client) EffectiveScore(agent common.Address) (*big.Int, error) {
	var out *big.Int
	err := c.Call("ReputationRegistry", "effectiveScore", &out, agent)
	return out, err
}

// ---------------- disputes (Hive Snowball) ----------------

// Dispute mirrors the tuple returned by disputeInfo.
type Dispute struct {
	StepId             *big.Int
	Challenger         common.Address
	ChallengedAgent    common.Address
	OriginalPreference [32]byte
	CurrentPreference  [32]byte
	Confidence         *big.Int
	Round              *big.Int
	Status             uint8
	Resolved           bool
}

// OpenDispute contests a finalised step with a preference hash.
func (c *Client) OpenDispute(stepID uint64, preference [32]byte) (uint64, error) {
	var next *big.Int
	if err := c.Call("HiveSnowball", "nextDisputeId", &next); err != nil {
		return 0, err
	}
	if _, err := c.Send("HiveSnowball", "openDispute", big.NewInt(int64(stepID)), preference); err != nil {
		return 0, err
	}
	return next.Uint64(), nil
}

// CastVote records a verifier opinion in a dispute.
func (c *Client) CastVote(disputeID uint64, preference [32]byte) error {
	_, err := c.Send("HiveSnowball", "castVote", big.NewInt(int64(disputeID)), preference)
	return err
}

// SnowballRound advances one snowball polling round.
func (c *Client) SnowballRound(disputeID uint64) error {
	_, err := c.Send("HiveSnowball", "snowballRound", big.NewInt(int64(disputeID)))
	return err
}

// ResolveDispute finalises a dispute once beta confidence is reached.
func (c *Client) ResolveDispute(disputeID uint64) error {
	_, err := c.Send("HiveSnowball", "resolveDispute", big.NewInt(int64(disputeID)))
	return err
}

// DisputeInfo reads dispute state.
func (c *Client) DisputeInfo(disputeID uint64) (Dispute, error) {
	var out Dispute
	err := c.Call("HiveSnowball", "disputeInfo", &out, big.NewInt(int64(disputeID)))
	return out, err
}

// ---------------- ICM ----------------

// DeliverICM simulates Warp-verified delivery of an inbound interchain message.
func (c *Client) DeliverICM(sourceChain [32]byte, origin, receiver common.Address, message []byte) error {
	_, err := c.Send("MockTeleporterMessenger", "deliver", sourceChain, origin, receiver, message)
	return err
}

// SendInterchainAnswer asks the coordinator to answer back over ICM.
func (c *Client) SendInterchainAnswer(destChain [32]byte, destination common.Address, requestID uint64) error {
	_, err := c.Send("TaskCoordinator", "sendInterchainAnswer", destChain, destination, big.NewInt(int64(requestID)))
	return err
}

// OutboundICMCount returns how many interchain answers have been emitted.
func (c *Client) OutboundICMCount() (uint64, error) {
	var out *big.Int
	err := c.Call("MockTeleporterMessenger", "outboundCount", &out)
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// IsActive reports whether an agent is currently accepting work.
func (c *Client) IsActive(agent common.Address) (bool, error) {
	var out bool
	err := c.Call("CapabilityRegistry", "isActive", &out, agent)
	return out, err
}

// ConsensusScoreCount returns how many Krum scores the coordinator stored for a step.
func (c *Client) ConsensusScoreCount(stepID uint64) (uint64, error) {
	var out *big.Int
	err := c.Call("TaskCoordinator", "consensusScoreCount", &out, big.NewInt(int64(stepID)))
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// ConsensusScoreAt returns the i-th stored Krum score for a step (for off-chain auditing).
func (c *Client) ConsensusScoreAt(stepID, i uint64) (*big.Int, error) {
	var out *big.Int
	err := c.Call("TaskCoordinator", "consensusScoreAt", &out, big.NewInt(int64(stepID)), big.NewInt(int64(i)))
	return out, err
}

// RegisterAgentAs registers the caller as an agent binding a distinct signing key, which requires an
// EIP-712 AgentRegistration signature from that key. Used to exercise the binding check.
func (c *Client) RegisterAgentAs(signer common.Address, capability [8]*big.Int, price *big.Int, endpoint string, deadline *big.Int, sig []byte) error {
	_, err := c.Send("CapabilityRegistry", "registerAgent", signer, capability, price, endpoint, deadline, sig)
	return err
}

// SnowballVoteCount returns the number of verifier votes recorded for a dispute.
func (c *Client) SnowballVoteCount(disputeID uint64) (uint64, error) {
	var out *big.Int
	err := c.Call("HiveSnowball", "voteCount", &out, big.NewInt(int64(disputeID)))
	if out == nil {
		return 0, err
	}
	return out.Uint64(), err
}

// MinStakeOf returns the agent-declared minimum stake enforced by the capability registry.
func (c *Client) MinStakeOf(agent common.Address) (*big.Int, error) {
	var out *big.Int
	err := c.Call(`CapabilityRegistry`, `minStakeOf`, &out, agent)
	return out, err
}

// SettlementMinStake returns the protocol-wide stake floor enforced by the settlement contract.
func (c *Client) SettlementMinStake() (*big.Int, error) {
	var out *big.Int
	err := c.Call(`StakingSettlement`, `minStake`, &out)
	return out, err
}
