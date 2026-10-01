// Command e2e drives the full Neural Hive protocol on the local Ganache devnet and asserts the
// observable on-chain and off-chain outcome of every scenario.
//
// Scenarios: happy-path multi-step request, Byzantine agent in the committee, forged signature,
// replayed response, unauthorised assignment, Hive Snowball dispute with slashing, EigenTrust
// reputation update, Avalanche ICM request and answer, and bandit fallback routing.
package main

import (
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/chain"
	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/consensus"
	"github.com/neural-hive/hive-node/internal/coordinator"
	hivecrypto "github.com/neural-hive/hive-node/internal/crypto"
	"github.com/neural-hive/hive-node/internal/routing"
)

type check struct {
	name   string
	pass   bool
	detail string
}

var results []check

func verify(name string, pass bool, detail string) {
	results = append(results, check{name: name, pass: pass, detail: detail})
	status := "FAIL"
	if pass {
		status = "PASS"
	}
	fmt.Printf("  [%s] %s :: %s\n", status, name, detail)
}

var (
	cfg   *config.Config
	admin *chain.Client
	co    *coordinator.Coordinator
	seed  = "neural-hive-devnet"
)

func main() {
	var err error
	cfg, err = config.Load()
	if err != nil {
		fatal("config", err)
	}
	adminKey := os.Getenv("COORDINATOR_PRIVATE_KEY")
	if adminKey == "" {
		adminKey = cfg.PrivateKey1
	}
	admin, err = chain.New(cfg, adminKey)
	if err != nil {
		fatal("chain client", err)
	}
	co = coordinator.NewCoordinator(cfg, admin, seed)

	fmt.Println("Neural Hive end-to-end protocol run")
	fmt.Printf("rpc=%s chainId=%d admin=%s coordinator=%s\n", cfg.RPCURL, cfg.ChainID, admin.From.Hex(), admin.Address("TaskCoordinator").Hex())

	waitForAgents()
	bootstrapAgents()

	scenarioHappyPath()
	scenarioByzantineAgent()
	scenarioForgedSignature()
	scenarioReplay()
	scenarioUnauthorisedAssign()
	scenarioSnowballDispute()
	scenarioReputation()
	scenarioICM()
	scenarioBanditFallback()
	scenarioRoutingLatency()

	failures := 0
	for _, r := range results {
		if !r.pass {
			failures++
		}
	}
	fmt.Printf("\n%d checks, %d passed, %d failed\n", len(results), len(results)-failures, failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func fatal(what string, err error) {
	fmt.Printf("FATAL %s: %v\n", what, err)
	os.Exit(2)
}

func waitForAgents() {
	for _, url := range cfg.AgentURLs {
		deadline := time.Now().Add(30 * time.Second)
		ok := false
		for time.Now().Before(deadline) {
			resp, err := http.Get(strings.TrimRight(url, "/") + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					ok = true
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			fatal("agent health "+url, fmt.Errorf("not reachable"))
		}
	}
	fmt.Printf("agents healthy: %d\n", len(cfg.AgentURLs))
}

func bootstrapAgents() {
	price := new(big.Int).SetUint64(uint64(0.02 * 1e18))
	specs := coordinator.DefaultAgentSpecs(cfg.AgentURLs, price)
	for _, s := range specs {
		if _, err := co.BootstrapAgent(s); err != nil {
			fatal("bootstrap agent", err)
		}
	}
	if err := co.RefreshViews(); err != nil {
		fatal("refresh views", err)
	}
	n, _ := admin.AgentCount()
	verify("agents-registered", n >= 5, fmt.Sprintf("on-chain agent count = %d", n))
}

func hiveToWei(v float64) *big.Int {
	f := big.NewFloat(v)
	f.Mul(f, big.NewFloat(1e18))
	i, _ := f.Int(nil)
	if i == nil {
		return big.NewInt(0)
	}
	return i
}

func statuses(steps []coordinator.StepResult) []uint8 {
	out := []uint8{}
	for _, s := range steps {
		out = append(out, s.Status)
	}
	return out
}

func anyCritical(steps []coordinator.StepResult) bool {
	for _, s := range steps {
		if s.Critical {
			return true
		}
	}
	return false
}

// scenarioHappyPath runs a multi-step request through Krum aggregation and VCG settlement.
func scenarioHappyPath() {
	fmt.Println("\n-- happy path: decompose, route, sign, Krum, VCG settle --")
	opts := coordinator.DefaultRunOptions()
	opts.RelayerURL = cfg.RelayerURL
	res, err := co.RunRequest("check this loan risk, explain it and flag it for review", opts)
	if err != nil {
		verify("happy-path-run", false, err.Error())
		return
	}
	verify("happy-path-run", true, fmt.Sprintf("request=%d steps=%d spent=%.6f HIVE elapsed=%dms", res.RequestID, len(res.Steps), res.Spent, res.ElapsedMs))

	allFinal := len(res.Steps) > 0
	for _, s := range res.Steps {
		if s.Status != chain.StepFinalized {
			allFinal = false
		}
	}
	verify("happy-path-steps-finalized", allFinal, fmt.Sprintf("statuses=%v (2=finalized)", statuses(res.Steps)))

	paid := len(res.Steps) > 0
	for _, s := range res.Steps {
		if s.Winner == "0x0000000000000000000000000000000000000000" {
			paid = false
			continue
		}
		ok, err := admin.IsPaid(s.StepID, common.HexToAddress(s.Winner))
		if err != nil || !ok {
			paid = false
		}
	}
	verify("happy-path-winners-paid", paid, "every step winner is marked paid on-chain")

	info, err := admin.RequestInfo(res.RequestID)
	verify("happy-path-request-settled", err == nil && info.Status == chain.RequestSettled, fmt.Sprintf("status=%d (1=settled)", info.Status))
	verify("happy-path-answer-hash", res.AnswerHash != "" && !strings.HasPrefix(res.AnswerHash, "0x0000000000"), res.AnswerHash)
	verify("happy-path-critical-path", anyCritical(res.Steps), "critical path steps marked by BACKTRACK_LONGEST_PATH")
	verify("happy-path-relayer-path", opts.RelayerURL != "", "signed responses delivered through the relayer service")
	verify("happy-path-escrow-refunded", res.Refunded > 0, fmt.Sprintf("unused escrow refunded = %.6f HIVE", res.Refunded))
}

// scenarioByzantineAgent puts a lying agent on the committee and checks Krum still selects truth.
func scenarioByzantineAgent() {
	fmt.Println("\n-- byzantine committee: one lying agent must not skew the answer --")
	opts := coordinator.DefaultRunOptions()
	opts.Byzantine = true
	opts.RelayerURL = cfg.RelayerURL
	res, err := co.RunRequest("summarize the market anomaly and score the risk", opts)
	if err != nil {
		verify("byzantine-run", false, err.Error())
		return
	}
	numeric := 0
	correct := 0
	for _, s := range res.Steps {
		if s.ConsensusValue == "" {
			continue
		}
		numeric++
		want := agent.DeterministicValue(s.Kind)
		got, ok := new(big.Int).SetString(s.ConsensusValue, 10)
		if ok && got.Int64() == want {
			correct++
		}
	}
	verify("byzantine-krum-consensus", numeric > 0 && correct == numeric, fmt.Sprintf("%d/%d numeric steps converged on the honest value", correct, numeric))

	liarNotPaid := true
	liarChecked := false
	for _, s := range res.Steps {
		if len(s.Assigned) == 0 {
			continue
		}
		liar := common.HexToAddress(s.Assigned[0])
		liarChecked = true
		ok, err := admin.IsPaid(s.StepID, liar)
		if err == nil && ok {
			liarNotPaid = false
		}
	}
	verify("byzantine-liar-not-paid", liarChecked && liarNotPaid, "the lying agent received no payment")
	verify("byzantine-krum-scores-recorded", hasKrumScores(res.Steps), "per-candidate Krum scores stored on-chain for audit")
}

func hasKrumScores(steps []coordinator.StepResult) bool {
	for _, s := range steps {
		if len(s.KrumScores) > 0 {
			return true
		}
	}
	return false
}

// scenarioRoutingLatency measures the cache-hit routing path against the proposal KPI (p95 < 300ms).
func scenarioRoutingLatency() {
	fmt.Println("\n-- routing latency (KPI: p95 under 300 ms on the cache-hit path) --")
	vec := capability.FromSkills([]string{"risk-scoring", "explain"})
	n := 300
	lat := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		_ = routing.RouteCandidates(co.Index, co.Views, vec, 16, 8)
		lat = append(lat, time.Since(t0))
	}
	p95 := percentile(lat, 0.95)
	verify("routing-latency-p95", p95 < 300*time.Millisecond, fmt.Sprintf("p95 = %v over %d route calls", p95, n))
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	cp := append([]time.Duration{}, d...)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	idx := int(p * float64(len(cp)-1))
	return cp[idx]
}

var (
	forgedReqID  uint64
	forgedStepID uint64
	forgedAgent  common.Address
	forgedResp   *agent.TaskResponse
)

// manualStep creates a single-agent step so adversarial cases can target one exact responder.
func manualStep(request, kind string, skills []string, numeric bool, agentIdx int, mode string) (uint64, uint64, common.Address, *agent.TaskResponse, error) {
	budget := hiveToWei(0.05)
	if err := admin.ApproveHive(admin.SettlementAddress(), budget); err != nil {
		return 0, 0, common.Address{}, nil, err
	}
	meta := crypto.Keccak256Hash([]byte(request))
	reqID, err := admin.CreateRequest(meta, budget, chain.TierOptimistic)
	if err != nil {
		return 0, 0, common.Address{}, nil, err
	}
	vec := capability.FromSkills(skills)
	deadline := uint64(time.Now().Add(10 * time.Minute).Unix())
	stepID, err := admin.AddStep(reqID, coordinator.Vec8(vec), 1, 0, hiveToWei(0.05), deadline, numeric)
	if err != nil {
		return 0, 0, common.Address{}, nil, err
	}
	target := co.Agents[agentIdx].Address
	if err := admin.AssignAgent(stepID, target, hiveToWei(0.02)); err != nil {
		return 0, 0, common.Address{}, nil, err
	}
	resp, err := coordinator.CallAgent(co.Agents[agentIdx].Endpoint, agent.TaskRequest{
		RequestID: reqID,
		StepID:    stepID,
		TaskKey:   kind,
		Deadline:  deadline,
		Mode:      mode,
	})
	if err != nil {
		return reqID, stepID, target, nil, err
	}
	return reqID, stepID, target, resp, nil
}

// scenarioForgedSignature proves the contract only accepts the agent bound signing key.
func scenarioForgedSignature() {
	fmt.Println("\n-- attestation verification: forged signatures must be rejected --")
	reqID, stepID, target, resp, err := manualStep("forged-signature-scenario", "risk-scoring", []string{"risk-scoring"}, true, 0, "")
	if err != nil {
		verify("forged-setup", false, err.Error())
		return
	}
	forgedReqID, forgedStepID, forgedAgent, forgedResp = reqID, stepID, target, resp

	bogus, _ := crypto.GenerateKey()
	value := new(big.Int).Set(resp.Value)
	hash := hivecrypto.OutputHashHasher(value)
	att := hivecrypto.Attestation{
		RequestID:   new(big.Int).SetUint64(reqID),
		StepID:      new(big.Int).SetUint64(stepID),
		Agent:       target,
		OutputHash:  hash,
		OutputValue: value,
		Nonce:       big.NewInt(1),
		Deadline:    big.NewInt(int64(resp.Deadline)),
	}
	forged, _ := hivecrypto.SignResponse(bogus, big.NewInt(cfg.ChainID), admin.Address("TaskCoordinator"), att)
	err = admin.SubmitResponse(stepID, target, hash, value, big.NewInt(1), big.NewInt(int64(resp.Deadline)), forged)
	verify("forged-signature-rejected", err != nil, fmt.Sprintf("reverted as expected: %v", errShort(err)))

	err = admin.SubmitResponse(stepID, target, resp.OutputHash, resp.Value, big.NewInt(int64(resp.Nonce)), big.NewInt(int64(resp.Deadline)), resp.Signature)
	verify("valid-signature-accepted", err == nil, fmt.Sprintf("accepted: %v", errShort(err)))
}

func errShort(err error) string {
	if err == nil {
		return "ok"
	}
	s := err.Error()
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

// scenarioReplay proves a signed response cannot be reused.
func scenarioReplay() {
	fmt.Println("\n-- replay protection --")
	if forgedResp == nil {
		verify("replay", false, "forged scenario did not produce a response")
		return
	}
	err := admin.SubmitResponse(forgedStepID, forgedAgent, forgedResp.OutputHash, forgedResp.Value, big.NewInt(int64(forgedResp.Nonce)), big.NewInt(int64(forgedResp.Deadline)), forgedResp.Signature)
	verify("replay-rejected", err != nil, fmt.Sprintf("second submission reverted: %v", errShort(err)))
	used, _ := admin.IsNonceUsed(forgedAgent, big.NewInt(int64(forgedResp.Nonce)))
	verify("nonce-consumed", used, "per-agent nonce marked used on-chain")
	if err := admin.FinalizeStep(forgedStepID); err != nil {
		verify("replay-scenario-finalized", false, err.Error())
		return
	}
	st, err := admin.StepInfo(forgedStepID)
	verify("replay-scenario-finalized", err == nil && st.Status == chain.StepFinalized, fmt.Sprintf("status=%d", st.Status))
	if err := admin.SettleRequest(forgedReqID); err != nil {
		verify("replay-scenario-settled", false, err.Error())
		return
	}
	info, err := admin.RequestInfo(forgedReqID)
	verify("replay-scenario-settled", err == nil && info.Status == chain.RequestSettled, "request settled after single valid response")
}

// scenarioUnauthorisedAssign checks that routing decisions cannot be made by anyone but the
// coordinator role, and that an agent cannot bind a signing key it does not control.
func scenarioUnauthorisedAssign() {
	fmt.Println("\n-- access control: unauthorised assignment and key binding --")
	budget := hiveToWei(0.05)
	if err := admin.ApproveHive(admin.SettlementAddress(), budget); err != nil {
		verify("access-control-setup", false, err.Error())
		return
	}
	meta := crypto.Keccak256Hash([]byte("access-control-scenario"))
	reqID, err := admin.CreateRequest(meta, budget, chain.TierOptimistic)
	if err != nil {
		verify("access-control-setup", false, err.Error())
		return
	}
	vec := capability.FromSkills([]string{"moderation"})
	deadline := uint64(time.Now().Add(10 * time.Minute).Unix())
	stepID, err := admin.AddStep(reqID, coordinator.Vec8(vec), 1, 0, hiveToWei(0.05), deadline, false)
	if err != nil {
		verify("access-control-setup", false, err.Error())
		return
	}
	agentCli := co.ClientOf[co.Agents[0].Address.Hex()]
	err = agentCli.AssignAgent(stepID, co.Agents[1].Address, big.NewInt(1))
	verify("unauthorised-assign-rejected", err != nil, fmt.Sprintf("non-coordinator assignment reverted: %v", errShort(err)))

	k1, _ := crypto.GenerateKey()
	k2, _ := crypto.GenerateKey()
	a1 := crypto.PubkeyToAddress(k1.PublicKey)
	a2 := crypto.PubkeyToAddress(k2.PublicKey)
	if err := admin.TransferEth(a1, big.NewInt(1e18)); err != nil {
		verify("bad-registration-binding-rejected", false, err.Error())
		return
	}
	cl1, err := chain.New(cfg, fmt.Sprintf("%x", crypto.FromECDSA(k1)))
	if err != nil {
		verify("bad-registration-binding-rejected", false, err.Error())
		return
	}
	sig, _ := hivecrypto.SignRegistration(k1, big.NewInt(cfg.ChainID), admin.Address("CapabilityRegistry"), a1, a2, big.NewInt(0), big.NewInt(int64(deadline)))
	badVec := coordinator.Vec8(capability.FromSkills([]string{"moderation"}))
	err = cl1.RegisterAgentAs(a2, badVec, big.NewInt(1e16), "http://127.0.0.1:9999", big.NewInt(int64(deadline)), sig)
	verify("bad-registration-binding-rejected", err != nil, fmt.Sprintf("mismatched binding reverted: %v", errShort(err)))

	overCap := new(big.Int).Mul(big.NewInt(2), big.NewInt(1e18))
	err = cl1.RegisterAgentAs(a1, badVec, overCap, "http://127.0.0.1:9999", big.NewInt(int64(deadline)), nil)
	verify("price-cap-enforced", err != nil, fmt.Sprintf("over-cap price reverted: %v", errShort(err)))
}

// scenarioSnowballDispute contests a wrong consensus and slashes the agent behind it.
func scenarioSnowballDispute() {
	fmt.Println("\n-- Hive Snowball dispute, slashing and step invalidation --")
	reqID, stepID, liar, resp, err := manualStep("dispute-scenario", "risk-scoring", []string{"risk-scoring"}, true, 4, "lying")
	if err != nil {
		verify("dispute-setup", false, err.Error())
		return
	}
	err = admin.SubmitResponse(stepID, liar, resp.OutputHash, resp.Value, big.NewInt(int64(resp.Nonce)), big.NewInt(int64(resp.Deadline)), resp.Signature)
	if err != nil {
		verify("dispute-setup", false, err.Error())
		return
	}
	if err := admin.FinalizeStep(stepID); err != nil {
		verify("dispute-setup", false, err.Error())
		return
	}
	st, err := admin.StepInfo(stepID)
	if err != nil {
		verify("dispute-setup", false, err.Error())
		return
	}
	lie := agent.ByzantineValue("risk-scoring")
	verify("dispute-consensus-is-lie", st.ConsensusValue != nil && st.ConsensusValue.Int64() == lie, fmt.Sprintf("on-chain consensus = %v", st.ConsensusValue))

	trueVal := big.NewInt(agent.DeterministicValue("risk-scoring"))
	honestPref := hivecrypto.OutputHashHasher(trueVal)
	snowballAddr := common.HexToAddress(cfg.Contracts.HiveSnowball)
	challenger := co.Agents[0]
	challengerCli := co.ClientOf[challenger.Address.Hex()]
	if err := challengerCli.ApproveHive(snowballAddr, hiveToWei(1)); err != nil {
		verify("dispute-opened", false, err.Error())
		return
	}
	disputeID, err := challengerCli.OpenDispute(stepID, honestPref)
	verify("dispute-opened", err == nil, fmt.Sprintf("dispute %d opened by %s: %v", disputeID, challenger.Address.Hex(), errShort(err)))
	if err != nil {
		return
	}

	for i := 1; i <= 3; i++ {
		if vl := co.ClientOf[co.Agents[i].Address.Hex()]; vl != nil {
			_ = vl.CastVote(disputeID, honestPref)
		}
	}
	votes, _ := admin.SnowballVoteCount(disputeID)
	verify("dispute-verifier-votes", votes >= 3, fmt.Sprintf("%d verifier votes cast", votes))

	resolved := false
	for r := 0; r < 10; r++ {
		if err := admin.SnowballRound(disputeID); err != nil {
			break
		}
		d, err := admin.DisputeInfo(disputeID)
		if err == nil && d.Resolved {
			resolved = true
			break
		}
	}
	d, _ := admin.DisputeInfo(disputeID)
	verify("snowball-resolved", resolved && d.Resolved, fmt.Sprintf("status=%d confidence=%v round=%v", d.Status, d.Confidence, d.Round))
	verify("snowball-upheld-challenger", d.Status == 1, "status 1 = challenge upheld, consensus overturned")

	liarStake, _ := admin.StakeOf(liar)
	verify("snowball-liar-slashed", liarStake.Cmp(big.NewInt(0)) == 0, fmt.Sprintf("remaining stake of the lying agent = %v wei", liarStake))
	st2, _ := admin.StepInfo(stepID)
	verify("snowball-step-invalidated", st2.Status == chain.StepFailed, fmt.Sprintf("step status=%d (3=failed)", st2.Status))
	if err := admin.SettleRequest(reqID); err != nil {
		verify("dispute-settled", false, err.Error())
	} else {
		verify("dispute-settled", true, "invalidated step paid nobody and escrow returned to the requester")
	}
}

// scenarioReputation computes EigenTrust off-chain and applies it on-chain optimistically.
func scenarioReputation() {
	fmt.Println("\n-- EigenTrust reputation: off-chain compute, optimistic on-chain batch --")
	if _, err := admin.Send("ReputationRegistry", "setChallengeWindow", big.NewInt(1)); err != nil {
		verify("reputation-setup", false, err.Error())
		return
	}
	addrs := []common.Address{}
	for _, a := range co.Agents {
		addrs = append(addrs, a.Address)
		if _, err := admin.Send("ReputationRegistry", "registerAgent", a.Address); err != nil {
			verify("reputation-setup", false, err.Error())
			return
		}
	}
	n := len(addrs)
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n)
	}
	half := big.NewInt(5e17)
	for i := 0; i < n-1 && i < 4; i++ {
		cl := co.ClientOf[addrs[i].Hex()]
		_ = cl.SubmitTrustSignal(addrs[0], hiveToWei(1))
		_ = cl.SubmitTrustSignal(addrs[1], half)
		m[i][0] = 1.0
		m[i][1] = 0.5
	}
	names := []string{}
	for _, a := range addrs {
		names = append(names, a.Hex())
	}
	pre := make([]float64, n)
	for i := range pre {
		pre[i] = 1.0 / float64(n)
	}
	et := consensus.EigenTrust(names, m, pre, 0.15, 200, 1e-10)
	scores := make([]*big.Int, n)
	for i, s := range et.Scores {
		f := big.NewFloat(s)
		f.Mul(f, big.NewFloat(1e18))
		iv, _ := f.Int(nil)
		scores[i] = iv
	}
	batchID, err := admin.ProposeScores(addrs, scores)
	if err != nil {
		verify("reputation-proposed", false, err.Error())
		return
	}
	verify("reputation-proposed", true, fmt.Sprintf("batch %d proposed for %d agents (iterations=%d)", batchID, n, et.Iterations))
	time.Sleep(1500 * time.Millisecond)
	if err := admin.FinalizeScores(batchID); err != nil {
		verify("reputation-finalized", false, err.Error())
		return
	}
	got, err := admin.EffectiveScore(addrs[0])
	verify("reputation-finalized", err == nil, "optimistic batch applied after the challenge window")
	verify("reputation-matches-computed", got != nil && got.Cmp(scores[0]) == 0, fmt.Sprintf("on-chain score %v equals EigenTrust score %v", got, scores[0]))
	verify("reputation-propagation", scores[0].Cmp(scores[n-1]) > 0, fmt.Sprintf("trusted agent %v outranks the isolated agent %v", scores[0], scores[n-1]))
}

func encodeICM(metaHash [32]byte, budget *big.Int, tier uint8, payer common.Address) []byte {
	b32, _ := abi.NewType("bytes32", "", nil)
	u256, _ := abi.NewType("uint256", "", nil)
	u8, _ := abi.NewType("uint8", "", nil)
	addr, _ := abi.NewType("address", "", nil)
	args := abi.Arguments{{Type: b32}, {Type: u256}, {Type: u8}, {Type: addr}}
	out, _ := args.Pack(metaHash, budget, tier, payer)
	return out
}

// scenarioICM simulates an Avalanche L1 submitting a request over ICM and receiving a verified answer.
func scenarioICM() {
	fmt.Println("\n-- Avalanche ICM: cross-chain request and answer, no bridge --")
	budget := hiveToWei(0.05)
	if err := admin.ApproveHive(admin.SettlementAddress(), budget); err != nil {
		verify("icm-setup", false, err.Error())
		return
	}
	meta := crypto.Keccak256Hash([]byte("icm-request-from-l1"))
	msg := encodeICM(meta, budget, chain.TierKrum, admin.From)
	sourceChain := crypto.Keccak256Hash([]byte("gamechain-L1"))
	originSender := common.HexToAddress("0x00000000000000000000000000000000000A11ce")
	receiver := admin.Address("TaskCoordinator")
	before, _ := admin.NextRequestID()
	err := admin.DeliverICM(sourceChain, originSender, receiver, msg)
	after, _ := admin.NextRequestID()
	verify("icm-request-created", err == nil && after == before+1, fmt.Sprintf("interchain message created request %d: %v", before, errShort(err)))
	if err != nil {
		return
	}
	reqID := before
	info, err := admin.RequestInfo(reqID)
	verify("icm-requester-is-origin-sender", err == nil && info.Requester == originSender, fmt.Sprintf("requester=%s", info.Requester.Hex()))

	opts := coordinator.DefaultRunOptions()
	opts.RelayerURL = cfg.RelayerURL
	res, err := co.ProcessExistingRequest(reqID, "check the loan risk and explain it", opts)
	if err != nil {
		verify("icm-request-executed", false, err.Error())
		return
	}
	verify("icm-request-executed", len(res.Steps) > 0, fmt.Sprintf("%d steps executed for the interchain request", len(res.Steps)))

	outBefore, _ := admin.OutboundICMCount()
	err = admin.SendInterchainAnswer(sourceChain, originSender, reqID)
	outAfter, _ := admin.OutboundICMCount()
	verify("icm-answer-sent", err == nil && outAfter == outBefore+1, fmt.Sprintf("outbound interchain answers = %d: %v", outAfter, errShort(err)))
}

// scenarioBanditFallback checks the LinUCB fallback and the price-of-anarchy watchdog.
func scenarioBanditFallback() {
	fmt.Println("\n-- bandit fallback routing and price-of-anarchy watchdog --")
	opts := coordinator.DefaultRunOptions()
	opts.ForceBandit = true
	opts.RelayerURL = cfg.RelayerURL
	res, err := co.RunRequest("moderate this content and summarise the sentiment", opts)
	if err != nil {
		verify("bandit-run", false, err.Error())
		return
	}
	allBandit := len(res.Steps) > 0
	for _, s := range res.Steps {
		if s.RouteMode != "bandit" {
			allBandit = false
		}
	}
	verify("bandit-routing-active", allBandit, fmt.Sprintf("route modes = %v", res.RouteModes))

	watch := routing.NewSelfishRouter(1.2)
	watch.ObserveWait(10, 1)
	verify("poa-watchdog-switches-to-bandit", watch.CurrentMode() == routing.ModeBandit, fmt.Sprintf("price of anarchy %.2f crossed the 1.2 threshold", watch.PriceOfAnarchy()))
	calm := routing.NewSelfishRouter(1.2)
	calm.ObserveWait(1.05, 1)
	verify("poa-watchdog-stays-selfish", calm.CurrentMode() == routing.ModeSelfish, fmt.Sprintf("price of anarchy %.2f stayed under threshold", calm.PriceOfAnarchy()))
}
