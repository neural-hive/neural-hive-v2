package coordinator

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/consensus"
	"github.com/neural-hive/hive-node/internal/decompose"
	"github.com/neural-hive/hive-node/internal/routing"
)

// RunOptions tunes one end-to-end request.
type RunOptions struct {
	Replication int
	Tier        uint8
	StepTimeout time.Duration
	RelayerURL  string
	// ForceBandit bypasses the price-of-anarchy watchdog for a request (high-stakes routing).
	ForceBandit bool
	// Byzantine makes the first routed agent on every step lie, to exercise Krum.
	Byzantine bool
	// AnswerEndpoint designates the agent that must answer the user in natural language. When it
	// is set the coordinator guarantees the agent sits on the committee of the final step and
	// sends that step the full user query, so the real generated answer comes back through the
	// normal agent interface. Leave it empty for pure protocol runs (tests, e2e).
	AnswerEndpoint string
	AnswerTimeout  time.Duration
}

// DefaultRunOptions returns the standard replicated-Krum settings.
func DefaultRunOptions() RunOptions {
	return RunOptions{Replication: 5, Tier: 1, StepTimeout: 10 * time.Minute, AnswerTimeout: 3 * time.Minute}
}

// StepResult summarises one settled step.
type StepResult struct {
	StepID         uint64   `json:"stepId"`
	Kind           string   `json:"kind"`
	Critical       bool     `json:"criticalPath"`
	RouteMode      string   `json:"routeMode"`
	Candidates     []string `json:"candidates"`
	Assigned       []string `json:"assigned"`
	Responses      int      `json:"responses"`
	Winner         string   `json:"winner"`
	RunnerUp       string   `json:"runnerUp"`
	ConsensusValue string   `json:"consensusValue"`
	PayoutHive     float64  `json:"payoutHive"`
	Status         uint8    `json:"status"`
	KrumScores     []string `json:"krumScores,omitempty"`
	// Answer is the natural-language text produced by the designated answer agent, if any.
	Answer     string `json:"answer,omitempty"`
	AnsweredBy string `json:"answeredBy,omitempty"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	// AnswerError records why the answer agent failed to produce prose, so it is never hidden.
	AnswerError string `json:"answerError,omitempty"`
	// AddStepTx and FinalizeTx are the real transaction hashes for this step.
	AddStepTx  string `json:"addStepTx,omitempty"`
	FinalizeTx string `json:"finalizeTx,omitempty"`
}

// RunResult is the outcome of a full request.
type RunResult struct {
	RequestID      uint64         `json:"requestId"`
	Steps          []StepResult   `json:"steps"`
	Budget         float64        `json:"budgetHive"`
	Spent          float64        `json:"spentHive"`
	Refunded       float64        `json:"refundedHive"`
	AnswerHash     string         `json:"answerHash"`
	RouteModes     map[string]int `json:"routeModes"`
	ElapsedMs      int64          `json:"elapsedMs"`
	PriceOfAnarchy float64        `json:"priceOfAnarchy"`
	// Answer is the primary natural-language answer shown in the workspace.
	Answer      string `json:"answer"`
	AnsweredBy  string `json:"answeredBy"`
	AnswerAgent string `json:"answerAgentEndpoint"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	AnswerError string `json:"answerError"`
	RequestTx   string `json:"requestTx"`
	SettleTx    string `json:"settleTx"`
	Network     string `json:"network"`
	ChainID     int64  `json:"chainId"`
	Coordinator string `json:"coordinatorContract"`
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

func (c *Coordinator) suggestBudget(g *decompose.Graph) *big.Int {
	total := 0.0
	for _, s := range g.Steps {
		total += s.MaxPrice
	}
	fee := 1.0 + float64(c.Cfg.ProtocolFeeBps)/10000.0
	return hiveToWei(total*fee*1.5 + 0.001)
}

// RunRequest executes one request end-to-end: decompose, route, assign, collect signed
// responses, aggregate with Krum on-chain and settle with VCG payments. When
// RunOptions.AnswerEndpoint is set the designated agent also returns the natural-language answer
// for the user, which is surfaced on RunResult.Answer.
func (c *Coordinator) RunRequest(request string, opts RunOptions) (*RunResult, error) {
	start := time.Now()
	if err := c.RefreshViews(); err != nil {
		return nil, err
	}
	if len(c.Views) == 0 {
		return nil, fmt.Errorf("no active agents registered")
	}
	graph := decompose.Decompose(request)
	order, err := graph.TopologicalSort()
	if err != nil {
		return nil, err
	}
	critical := map[string]bool{}
	for _, al := range graph.AllocateFastestAgents() {
		if al.CriticalPath {
			critical[al.StepID] = true
		}
	}
	finalID := ""
	if len(order) > 0 {
		finalID = order[len(order)-1]
	}

	budget := c.suggestBudget(graph)
	if err := c.Admin.ApproveHive(c.Admin.SettlementAddress(), budget); err != nil {
		return nil, fmt.Errorf("approve escrow: %w", err)
	}
	meta := crypto.Keccak256Hash([]byte(request))
	reqID, err := c.Admin.CreateRequest(meta, budget, opts.Tier)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	res := &RunResult{
		RequestID:  reqID,
		Budget:     weiToHive(budget),
		RouteModes: map[string]int{},
	}
	res.RequestTx = c.Admin.LastTxHash().Hex()
	res.Network = c.Cfg.Network
	res.ChainID = c.Cfg.ChainID
	res.Coordinator = c.Admin.Address("TaskCoordinator").Hex()
	res.AnswerAgent = opts.AnswerEndpoint
	for _, id := range order {
		st, ok := graph.Step(id)
		if !ok {
			continue
		}
		sr, err := c.runStep(reqID, st, request, critical[id], id == finalID, opts)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", id, err)
		}
		res.Steps = append(res.Steps, *sr)
		res.RouteModes[sr.RouteMode]++
	}
	collectAnswer(res)

	if err := c.Admin.SettleRequest(reqID); err != nil {
		return nil, fmt.Errorf("settle request: %w", err)
	}
	res.SettleTx = c.Admin.LastTxHash().Hex()
	info, err := c.Admin.RequestInfo(reqID)
	if err == nil {
		res.Spent = weiToHive(info.Spent)
		// Settlement refunds leftover escrow to the requester, so escrowAvailable is zero
		// afterwards: report the refund as the part of the budget that was never paid out.
		if info.Spent.Cmp(budget) < 0 {
			res.Refunded = weiToHive(new(big.Int).Sub(budget, info.Spent))
		}
	}
	if ah, err := c.Admin.AnswerHash(reqID); err == nil {
		res.AnswerHash = fmt.Sprintf("0x%x", ah)
	}
	res.ElapsedMs = time.Since(start).Milliseconds()
	res.PriceOfAnarchy = c.Selfish.PriceOfAnarchy()
	return res, nil
}

// collectAnswer lifts the last natural-language answer produced by a step onto the run result,
// so the UI can render it as the primary product of the request.
func collectAnswer(res *RunResult) {
	for _, sr := range res.Steps {
		if sr.Answer != "" {
			res.Answer = sr.Answer
			res.AnsweredBy = sr.AnsweredBy
			res.Model = sr.Model
			res.Provider = sr.Provider
		}
		if sr.AnswerError != "" {
			res.AnswerError = sr.AnswerError
		}
	}
}

// selectAgents picks the committee for a step, mirroring SELFISH_ROUTE or LinUCB.
func (c *Coordinator) selectAgents(cands []routing.Scored, mode routing.Mode, task capability.Vector, k int) []routing.Scored {
	if len(cands) == 0 {
		return nil
	}
	if k > len(cands) {
		k = len(cands)
	}
	if mode == routing.ModeBandit {
		remaining := append([]routing.Scored{}, cands...)
		out := make([]routing.Scored, 0, k)
		for len(out) < k && len(remaining) > 0 {
			arms := make([]string, 0, len(remaining))
			for _, r := range remaining {
				arms = append(arms, r.Address)
			}
			pick := c.Bandit.Select(task, arms)
			for i, r := range remaining {
				if r.Address == pick {
					out = append(out, r)
					remaining = append(remaining[:i], remaining[i+1:]...)
					break
				}
			}
		}
		return out
	}
	sorted := append([]routing.Scored{}, cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ci := c.Selfish.PerceivedCost(sorted[i].AgentView)
		cj := c.Selfish.PerceivedCost(sorted[j].AgentView)
		if ci == cj {
			if sorted[i].Reputation == sorted[j].Reputation {
				return sorted[i].Address < sorted[j].Address
			}
			return sorted[i].Reputation > sorted[j].Reputation
		}
		return ci < cj
	})
	return sorted[:k]
}

// ensureEndpoint guarantees the designated answer agent sits on the committee. The on-chain step
// accepts at most its replication factor of assignments, so an unselected member is swapped out
// rather than appended, which keeps every consensus and settlement invariant intact.
func ensureEndpoint(chosen []routing.Scored, cands []routing.Scored, endpoint string, k int) []routing.Scored {
	if endpoint == "" {
		return chosen
	}
	for _, ch := range chosen {
		if ch.Endpoint == endpoint {
			return chosen
		}
	}
	var target *routing.Scored
	for i := range cands {
		if cands[i].Endpoint == endpoint {
			target = &cands[i]
			break
		}
	}
	if target == nil {
		return chosen
	}
	out := append([]routing.Scored{}, chosen...)
	if len(out) < k {
		return append(out, *target)
	}
	if len(out) == 0 {
		return []routing.Scored{*target}
	}
	out[len(out)-1] = *target
	return out
}

// runStep executes one step: add it on-chain, choose the committee, dispatch the task to each
// member, submit the signed responses and finalise with on-chain Krum.
//
// requestText is the original user request. isFinal marks the last step in topological order,
// which is where the designated answer agent is asked for the natural-language answer.
func (c *Coordinator) runStep(reqID uint64, step decompose.Step, requestText string, critical, isFinal bool, opts RunOptions) (*StepResult, error) {
	taskVec := capability.FromSkills(step.Skills)
	replication := opts.Replication
	if replication < 1 {
		replication = 1
	}
	// Never ask for a larger committee than the number of eligible candidates: a step whose
	// replication exceeds its assignable agents can never reach the Krum quorum and would be
	// finalised as FAILED, wasting the request budget.
	if avail := len(routing.RouteCandidates(c.Index, c.Views, taskVec, 16, 16)); replication > avail && avail > 0 {
		replication = avail
	}
	f := 0
	if replication >= 3 {
		f = consensus.MaxFaults(replication)
	}
	deadline := uint64(time.Now().Add(opts.StepTimeout).Unix())
	maxPrice := hiveToWei(step.MaxPrice)

	stepID, err := c.Admin.AddStep(reqID, Vec8(taskVec), uint32(replication), uint32(f), maxPrice, deadline, step.Numeric)
	if err != nil {
		return nil, fmt.Errorf("add step: %w", err)
	}

	cands := routing.RouteCandidates(c.Index, c.Views, taskVec, 16, 16)
	mode := routing.ModeSelfish
	if opts.ForceBandit || c.Selfish.ShouldUseBandit() {
		mode = routing.ModeBandit
	}
	chosen := c.selectAgents(cands, mode, taskVec, replication)
	if isFinal && opts.AnswerEndpoint != "" {
		chosen = ensureEndpoint(chosen, cands, opts.AnswerEndpoint, replication)
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("no candidates for step %s", step.ID)
	}

	sr := &StepResult{StepID: stepID, Kind: step.Kind, Critical: critical, RouteMode: string(mode)}
	sr.AddStepTx = c.Admin.LastTxHash().Hex()
	for _, ch := range cands {
		sr.Candidates = append(sr.Candidates, ch.Address)
	}

	for _, ch := range chosen {
		addr := common.HexToAddress(ch.Address)
		bid := hiveToWei(ch.Price)
		if err := c.Admin.AssignAgent(stepID, addr, bid); err != nil {
			return nil, fmt.Errorf("assign %s: %w", ch.Address, err)
		}
		sr.Assigned = append(sr.Assigned, ch.Address)
	}

	stepStart := time.Now()
	responded := 0
	for i, ch := range chosen {
		modeOverride := ""
		if opts.Byzantine && i == 0 {
			modeOverride = string(agent.ModeLying)
		}
		req := agent.TaskRequest{
			RequestID: reqID,
			StepID:    stepID,
			TaskKey:   step.Kind,
			TaskVec:   vecToInts(taskVec),
			Deadline:  deadline,
			Mode:      modeOverride,
		}
		wantsAnswer := isFinal && opts.AnswerEndpoint != "" && ch.Endpoint == opts.AnswerEndpoint
		if wantsAnswer {
			req.Prompt = answerPrompt(requestText, step)
		}
		timeout := 20 * time.Second
		if wantsAnswer && opts.AnswerTimeout > 0 {
			timeout = opts.AnswerTimeout
		}
		resp, err := callAgent(ch.Endpoint, req, timeout)
		if err != nil {
			if wantsAnswer {
				sr.AnswerError = err.Error()
			}
			continue
		}
		if resp.Answer != "" {
			sr.Answer = resp.Answer
			sr.AnsweredBy = ch.Address
			sr.Model = resp.Model
			sr.Provider = resp.Provider
		}
		if err := c.submitSignedResponse(stepID, resp, opts); err != nil {
			if wantsAnswer && sr.AnswerError == "" {
				sr.AnswerError = "answer produced but the signed attestation was rejected: " + err.Error()
			}
			continue
		}
		responded++
	}

	if err := c.Admin.FinalizeStep(stepID); err != nil {
		return nil, fmt.Errorf("finalize step %d: %w", stepID, err)
	}
	sr.FinalizeTx = c.Admin.LastTxHash().Hex()
	st, err := c.Admin.StepInfo(stepID)
	if err != nil {
		return nil, err
	}
	sr.Responses = responded
	sr.Status = st.Status
	sr.Winner = st.Winner.Hex()
	sr.RunnerUp = st.RunnerUp.Hex()
	sr.PayoutHive = weiToHive(st.Payout)
	if st.Numeric && st.ConsensusValue != nil {
		sr.ConsensusValue = st.ConsensusValue.String()
	}
	if n, err := c.Admin.ConsensusScoreCount(stepID); err == nil && n > 0 {
		for i := uint64(0); i < n; i++ {
			if sc, err := c.Admin.ConsensusScoreAt(stepID, i); err == nil {
				sr.KrumScores = append(sr.KrumScores, sc.String())
			}
		}
	}

	// Feed the price-of-anarchy watchdog and the bandit learner with the observed outcome.
	actual := time.Since(stepStart).Seconds()
	c.Selfish.ObserveWait(actual, step.Duration)
	if len(chosen) > 0 {
		reward := 1.0 / (1.0 + actual)
		c.Bandit.Update(chosen[0].Address, taskVec, reward)
	}
	return sr, nil
}

// answerPrompt frames the user request for the answer agent, adding the step context so a
// multi-step request still reads as a coherent instruction.
func answerPrompt(requestText string, step decompose.Step) string {
	if strings.TrimSpace(requestText) == "" {
		return step.Kind
	}
	return requestText
}

func (c *Coordinator) agentByAddress(hex string) *agent.Agent {
	for _, a := range c.Agents {
		if a.Address.Hex() == hex {
			return a
		}
	}
	return nil
}

func vecToInts(v capability.Vector) []int64 {
	return v.ToOnChain()
}

// StepPlan is the decomposed, scheduled form of a request.
type StepPlan struct {
	Graph    *decompose.Graph
	Order    []string
	Critical map[string]bool
}

// Plan decomposes a request and works out the critical path (4.2.4).
func Plan(request string) (*StepPlan, error) {
	g := decompose.Decompose(request)
	order, err := g.TopologicalSort()
	if err != nil {
		return nil, err
	}
	crit := map[string]bool{}
	for _, al := range g.AllocateFastestAgents() {
		if al.CriticalPath {
			crit[al.StepID] = true
		}
	}
	return &StepPlan{Graph: g, Order: order, Critical: crit}, nil
}

// ProcessExistingRequest runs the steps of an already-funded request (used by the ICM path, where
// the request was created by a message from another Avalanche L1).
func (c *Coordinator) ProcessExistingRequest(requestID uint64, requestText string, opts RunOptions) (*RunResult, error) {
	start := time.Now()
	if err := c.RefreshViews(); err != nil {
		return nil, err
	}
	plan, err := Plan(requestText)
	if err != nil {
		return nil, err
	}
	finalID := ""
	if len(plan.Order) > 0 {
		finalID = plan.Order[len(plan.Order)-1]
	}
	res := &RunResult{RequestID: requestID, RouteModes: map[string]int{}}
	res.Network = c.Cfg.Network
	res.ChainID = c.Cfg.ChainID
	res.Coordinator = c.Admin.Address("TaskCoordinator").Hex()
	res.AnswerAgent = opts.AnswerEndpoint
	for _, id := range plan.Order {
		st, ok := plan.Graph.Step(id)
		if !ok {
			continue
		}
		sr, err := c.runStep(requestID, st, requestText, plan.Critical[id], id == finalID, opts)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", id, err)
		}
		res.Steps = append(res.Steps, *sr)
		res.RouteModes[sr.RouteMode]++
	}
	collectAnswer(res)
	if err := c.Admin.SettleRequest(requestID); err != nil {
		return nil, fmt.Errorf("settle: %w", err)
	}
	res.SettleTx = c.Admin.LastTxHash().Hex()
	if info, err := c.Admin.RequestInfo(requestID); err == nil {
		res.Spent = weiToHive(info.Spent)
		if info.Budget.Cmp(info.Spent) > 0 {
			res.Refunded = weiToHive(new(big.Int).Sub(info.Budget, info.Spent))
		}
	}
	if ah, err := c.Admin.AnswerHash(requestID); err == nil {
		res.AnswerHash = fmt.Sprintf("0x%x", ah)
	}
	res.ElapsedMs = time.Since(start).Milliseconds()
	res.PriceOfAnarchy = c.Selfish.PriceOfAnarchy()
	return res, nil
}
