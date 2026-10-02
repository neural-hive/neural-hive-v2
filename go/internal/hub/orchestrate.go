package hub

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/deepseek"
	"github.com/neural-hive/hive-node/internal/deepworker"
	"github.com/neural-hive/hive-node/internal/llm"
)

// Selection weights. Tags dominate (capability must fit the subtask); reputation, risk, historical
// success and latency break the tie between equally capable agents. The weights are fixed and
// documented so a routing decision can always be explained in the execution trace.
const (
	wTag         = 0.45
	wReputation  = 0.20
	wRisk        = 0.15
	wSuccess     = 0.10
	wLatency     = 0.10
	latencyCapMs = 10000.0
)

// Hub is the central Neural Hive orchestrator over a pool of model-backed agents.
type Hub struct {
	Cfg       config.Hub
	Registry  *Registry
	Provider  llm.Provider
	Agg       *llm.Wrapper
	AgentTime time.Duration
	client    *http.Client
	UILog     *log.Logger
	Pay       *Payments
	Events    *eventBus
	bandit    *bandit
	Wallets   *WalletBook
	// WalletFactory deploys and tracks the per-agent AgentWallet smart contracts.
	WalletFactory *AgentWalletBook
	cfg           *config.Config
	chainMu       sync.Mutex
	onchain       *OnChain

	mu      sync.Mutex
	history []*Result

	regMu       sync.Mutex
	pendingRegs map[string]*pendingRegistration

	// maxAgentsMu guards maxAgents, the runtime cap on how many agents one task may be sent to.
	// It starts at the configured HIVE_MAX ceiling and can be lowered from the UI, but never raised above HIVE_MAX. decideHIVE clamps every routing decision to this value.
	maxAgentsMu sync.Mutex
	maxAgents   int
}

type pendingRegistration struct {
	Card   AgentCard
	Detail string
	At     time.Time
}

// New builds a Hub over the resolved configuration, the agent registry and the shared DeepSeek
// provider used for the aggregation step.
func New(cfg *config.Config, reg *Registry) *Hub {
	provider := deepseek.NewClient(cfg.DeepSeek.APIKey, cfg.DeepSeek.BaseURL, cfg.DeepSeek.Timeout, nil)
	h := NewWithProvider(cfg.Hub, reg, provider, cfg.DeepSeek.Timeout)
	h.cfg = cfg
	if owner := h.OwnerAddress(); owner != "" {
		for _, card := range reg.Snapshot() {
			reg.SetOwner(card.ID, owner)
		}
	}
	return h
}

// NewWithProvider builds a Hub over an explicit provider; tests inject a fake provider here so the
// orchestration can be exercised without calling the live DeepSeek API.
func NewWithProvider(cfg config.Hub, reg *Registry, provider llm.Provider, agentTimeout time.Duration) *Hub {
	if agentTimeout <= 0 {
		agentTimeout = 120 * time.Second
	}
	agg := llm.NewWrapper(llm.Config{
		Provider:    provider,
		Model:       cfg.AggregatorModel,
		System:      cfg.AggregatorSystem,
		Temperature: 0.3,
	})
	return &Hub{
		Cfg:           cfg,
		Registry:      reg,
		Provider:      provider,
		Agg:           agg,
		AgentTime:     agentTimeout,
		client:        &http.Client{Timeout: agentTimeout + 10*time.Second},
		history:       []*Result{},
		Pay:           newPayments(cfg),
		Events:        newEventBus(),
		bandit:        newBandit(4),
		Wallets:       newWalletBook(),
		WalletFactory: newAgentWalletBook(cfg.WalletFactoryAddress, cfg.TokenAddress),
		pendingRegs:   map[string]*pendingRegistration{},
		maxAgents:     cfg.HIVE.Max,
	}
}

// scoreAgent blends the routing signals into one comparable number.
func scoreAgent(c AgentCard, tags []string) (float64, []string) {
	matching := intersect(c.Tags, tags)
	overlap := 0.0
	if len(tags) > 0 {
		overlap = float64(len(matching)) / float64(len(tags))
	}
	successRate := 0.5
	latencyScore := 0.5
	if c.Executions > 0 {
		successRate = float64(c.Success) / float64(c.Executions)
		latencyScore = 1 - clamp01(float64(c.AvgLatencyMs)/latencyCapMs)
	}
	score := wTag*overlap + wReputation*clamp01(c.Reputation) + wRisk*(1-clamp01(c.Risk)) + wSuccess*successRate + wLatency*latencyScore
	return score, matching
}

func intersect(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range a {
		set[strings.ToLower(x)] = true
	}
	out := []string{}
	for _, y := range b {
		if set[strings.ToLower(y)] {
			out = append(out, y)
		}
	}
	sort.Strings(out)
	return out
}

func hashHex(s string) string {
	return "0x" + hex.EncodeToString(crypto.Keccak256([]byte(s)))
}

// Select chooses the most suitable available agent for a subtask. It never picks arbitrarily and
// never picks the first N: it ranks eligible agents by tag match, reputation, risk, historical
// success and latency, prefers a tag match and prefers an agent that is not already busy on this
// task so independent subtasks can run in parallel.
func (h *Hub) Select(sub Subtask, used map[string]bool) (AgentCard, Selection) {
	type cand struct {
		card     AgentCard
		score    float64
		matching []string
	}
	all := []cand{}
	for _, c := range h.Registry.Snapshot() {
		if c.Status != "online" {
			continue
		}
		s, m := scoreAgent(c, sub.Tags)
		all = append(all, cand{card: c, score: s, matching: m})
	}
	if len(all) == 0 {
		return AgentCard{}, Selection{SubtaskID: sub.ID, RequiredTags: sub.Tags, Reason: "no online agent"}
	}
	matching := []cand{}
	for _, c := range all {
		if len(c.matching) > 0 {
			matching = append(matching, c)
		}
	}
	if len(matching) == 0 {
		matching = all
	}
	pool := []cand{}
	for _, c := range matching {
		if !used[c.card.ID] {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		for _, c := range all {
			if !used[c.card.ID] {
				pool = append(pool, c)
			}
		}
	}
	if len(pool) == 0 {
		pool = matching
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].score == pool[j].score {
			return pool[i].card.ID < pool[j].card.ID
		}
		return pool[i].score > pool[j].score
	})
	best := pool[0]
	reason := fmt.Sprintf("matched tags: %s; reputation %.3f; risk %.3f; success %d/%d; avg latency %d ms",
		joinOrNone(best.matching), best.card.Reputation, best.card.Risk, best.card.Success, best.card.Executions, best.card.AvgLatencyMs)
	if len(best.matching) == 0 {
		reason = "no tag match, best available agent; " + reason
	}
	return best.card, Selection{
		SubtaskID: sub.ID, RequiredTags: sub.Tags, AgentID: best.card.ID, MatchingTags: best.matching,
		Reputation: best.card.Reputation, Risk: best.card.Risk, Score: best.score, Online: true, Reason: reason,
	}
}

func joinOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func buildSubtaskPrompt(sub Subtask) string {
	return "Subtask (capability tags: " + joinOrNone(sub.Tags) + "): " + sub.Description + "\n\nAddress this subtask only and answer it directly."
}

// executeAgent dispatches one subtask to one agent instance and records the outcome on the agent.
func (h *Hub) executeAgent(ctx context.Context, taskID string, sub Subtask, card AgentCard, history []deepworker.ChatTurn) AgentResult {
	r := AgentResult{
		SubtaskID: sub.ID, AgentID: card.ID, Port: card.Port, Tags: append([]string{}, card.Tags...),
		Endpoint: card.Endpoint, Model: card.Model, Provider: card.Provider,
	}
	req := deepworker.TaskRequest{SubTaskID: sub.ID, TaskID: taskID, Prompt: buildSubtaskPrompt(sub), Require: sub.Tags, History: history}
	body, _ := json.Marshal(req)
	tctx, cancel := context.WithTimeout(ctx, h.AgentTime)
	defer cancel()
	start := time.Now()
	httpReq, err := http.NewRequestWithContext(tctx, http.MethodPost, strings.TrimRight(card.Endpoint, "/")+"/task", bytes.NewReader(body))
	ok := false
	timedOut := false
	var answer, model, provider, reqID, errMsg, startedAt, finishedAt string
	if err != nil {
		errMsg = err.Error()
	} else {
		httpReq.Header.Set("Content-Type", "application/json")
		resp, derr := h.client.Do(httpReq)
		if derr != nil {
			errMsg = derr.Error()
			if tctx.Err() == context.DeadlineExceeded {
				timedOut = true
			}
		} else {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errMsg = fmt.Sprintf("agent %s returned %d: %s", card.ID, resp.StatusCode, strings.TrimSpace(string(raw)))
			} else {
				var out deepworker.TaskResponse
				if e := json.Unmarshal(raw, &out); e != nil {
					errMsg = e.Error()
				} else {
					answer = out.Answer
					model = out.Model
					provider = out.Provider
					reqID = out.RequestID
					startedAt = out.StartedAt
					finishedAt = out.FinishedAt
					if answer == "" {
						errMsg = "agent returned an empty answer"
					} else {
						ok = true
					}
				}
			}
		}
	}
	latency := time.Since(start).Milliseconds()
	repB, riskB, repA, riskA := h.Registry.RecordResult(card.ID, ok, timedOut, latency, errMsg)
	r.ReputationBefore, r.RiskBefore, r.ReputationAfter, r.RiskAfter = repB, riskB, repA, riskA
	r.LatencyMs = latency
	if !ok {
		r.Status = "failed"
		r.Error = errMsg
		return r
	}
	r.Status = "ok"
	r.Answer = answer
	r.RequestID = reqID
	r.StartedAt = startedAt
	r.FinishedAt = finishedAt
	if model != "" {
		r.Model = model
	}
	if provider != "" {
		r.Provider = provider
	}
	r.Hash = hashHex(answer)
	return r
}

// decideHIVE turns the complexity classification and the configured policy into the actual number
// of agents that should work on the task.
func (h *Hub) decideHIVE(cx Complexity, objectives []string, override int) int {
	hive := h.Cfg.HIVE.Default
	if cx.Level == "complex" {
		hive = h.Cfg.HIVE.Complex
		if len(objectives) > hive {
			hive = len(objectives)
		}
	}
	if override > 0 {
		hive = override
	}
	// Hard ceiling: the configured HIVE_MAX, further reduced by the runtime cap the UI sets.
	if cap := h.MaxAgents(); hive > cap {
		hive = cap
	}
	if online := h.Registry.OnlineCount(); online > 0 && hive > online {
		hive = online
	}
	if hive < 1 {
		hive = 1
	}
	return hive
}

// MaxAgents returns the runtime cap on how many agents one task may be routed to. It never
// exceeds the configured HIVE_MAX ceiling and is always at least 1.
func (h *Hub) MaxAgents() int {
	h.maxAgentsMu.Lock()
	defer h.maxAgentsMu.Unlock()
	if h.maxAgents < 1 {
		if h.Cfg.HIVE.Max < 1 {
			return 1
		}
		return h.Cfg.HIVE.Max
	}
	return h.maxAgents
}

// SetMaxAgents sets the runtime cap on how many agents one task may be routed to. The value is
// clamped to [1, HIVE_MAX] so the UI control can never raise the cap above the configured
// ceiling; it returns the value actually stored.
func (h *Hub) SetMaxAgents(n int) int {
	h.maxAgentsMu.Lock()
	defer h.maxAgentsMu.Unlock()
	max := h.Cfg.HIVE.Max
	if max < 1 {
		max = 1
	}
	if n < 1 {
		n = 1
	}
	if n > max {
		n = max
	}
	h.maxAgents = n
	return n
}

func trace(res *Result, stage, detail string, data map[string]interface{}) {
	log.Printf("hub %s task=%s %s", stage, res.TaskID, detail)
	res.Trace = append(res.Trace, TraceEvent{
		At: time.Now().UTC().Format(time.RFC3339Nano), Stage: stage, Detail: detail, Data: data,
	})
}

func findSub(subs []Subtask, id string) (Subtask, bool) {
	for _, s := range subs {
		if s.ID == id {
			return s, true
		}
	}
	return Subtask{}, false
}

// Run executes one task end to end: classify, decide HIVE, decompose, select agents, run the
// independent subtasks concurrently, aggregate, hash and anchor, recording the full trace.
func (h *Hub) Run(ctx context.Context, text string, hiveOverride int) (*Result, error) {
	return h.RunPaid(ctx, text, hiveOverride, nil)
}

// RunPaid is Run with the payment that funded the task. A nil payment means no payment was taken
// (payment not required, or the task came from the startup self-test); the cost is still computed
// and recorded so the interface can always show what a request costs in HIVE.
func (h *Hub) RunPaid(ctx context.Context, text string, hiveOverride int, pay *Payment) (*Result, error) {
	start := time.Now()
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty task")
	}
	res := &Result{TaskID: "task-" + start.UTC().Format("20060102T150405.000000000"), Task: text, StartedAt: start.UTC().Format(time.RFC3339Nano)}
	trace(res, "hub.received", "Hub received the task", map[string]interface{}{"taskId": res.TaskID})

	cx := Classify(text)
	res.Complexity = cx
	trace(res, "hub.complexity", "Classified the task as "+cx.Level, map[string]interface{}{"level": cx.Level, "score": cx.Score, "signals": cx.Signals, "domains": cx.Domains})

	objectives := SplitObjectives(text)
	hive := h.decideHIVE(cx, objectives, hiveOverride)
	res.HIVE = hive
	trace(res, "hub.hive", fmt.Sprintf("HIVE requirement = %d agent(s)", hive), map[string]interface{}{"hive": hive, "onlineAgents": h.Registry.OnlineCount()})

	if pay == nil {
		pay = h.paymentRecord("none", hive)
	} else if pay.Mode == "selftest" {
		pay = h.paymentRecord("selftest", hive)
	}
	res.Payment = pay
	switch {
	case pay.Verified:
		trace(res, "hub.payment", fmt.Sprintf("Verified payment of %s HIVE from %s (tx %s)", pay.PaidHive, pay.Payer, pay.TxHash), map[string]interface{}{
			"payer": pay.Payer, "paidHive": pay.PaidHive, "costHive": pay.CostHive, "agents": pay.Agents,
			"pricePerAgent": pay.PricePerAgent, "txHash": pay.TxHash, "block": pay.BlockNumber, "treasury": pay.Treasury,
		})
	case pay.Mode == "selftest":
		trace(res, "hub.payment", fmt.Sprintf("Startup self-test: no payment taken (a user task would cost %s HIVE)", pay.CostHive), map[string]interface{}{"costHive": pay.CostHive})
	default:
		trace(res, "hub.payment", fmt.Sprintf("Payment not required by configuration (this task would cost %s HIVE)", pay.CostHive), map[string]interface{}{"costHive": pay.CostHive})
	}

	subs := Decompose(text, hive, cx.Domains)
	res.Subtasks = subs
	trace(res, "hub.decompose", fmt.Sprintf("Decomposed the task into %d subtask(s)", len(subs)), map[string]interface{}{"subtasks": subs})

	used := map[string]bool{}
	for _, sub := range subs {
		card, sel := h.Select(sub, used)
		if card.ID == "" {
			res.Errors = append(res.Errors, "no agent available for subtask "+sub.ID)
			trace(res, "hub.select", "No agent available for "+sub.ID, map[string]interface{}{"subtask": sub.ID, "requiredTags": sub.Tags})
			continue
		}
		used[card.ID] = true
		res.Selections = append(res.Selections, sel)
		trace(res, "hub.select", "Selected "+card.ID+" for "+sub.ID, map[string]interface{}{
			"subtask": sub.ID, "requiredTags": sub.Tags, "agent": card.ID, "port": card.Port,
			"tags": card.Tags, "matchingTags": sel.MatchingTags, "reputation": sel.Reputation,
			"risk": sel.Risk, "score": sel.Score, "reason": sel.Reason,
		})
	}

	type slot struct {
		card AgentCard
		sub  Subtask
	}
	slots := []slot{}
	for _, sel := range res.Selections {
		sub, sok := findSub(subs, sel.SubtaskID)
		card, cok := h.Registry.Get(sel.AgentID)
		if !sok || !cok {
			continue
		}
		slots = append(slots, slot{card: card, sub: sub})
	}
	for _, s := range slots {
		trace(res, "hub.dispatch", "Dispatched "+s.sub.ID+" to "+s.card.ID, map[string]interface{}{"subtask": s.sub.ID, "agent": s.card.ID, "port": s.card.Port, "endpoint": s.card.Endpoint, "tags": s.card.Tags})
	}

	results := make([]AgentResult, len(slots))
	var wg sync.WaitGroup
	for i := range slots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.executeAgent(ctx, res.TaskID, slots[i].sub, slots[i].card, nil)
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		res.AgentResults = append(res.AgentResults, r)
		if r.Status == "ok" {
			trace(res, "hub.response", "Answer received from "+r.AgentID, map[string]interface{}{
				"subtask": r.SubtaskID, "agent": r.AgentID, "port": r.Port, "latencyMs": r.LatencyMs,
				"reputationBefore": r.ReputationBefore, "riskBefore": r.RiskBefore,
				"reputationAfter": r.ReputationAfter, "riskAfter": r.RiskAfter, "hash": r.Hash,
			})
		} else {
			res.Errors = append(res.Errors, r.AgentID+" failed: "+r.Error)
			trace(res, "hub.response", "Subtask failed on "+r.AgentID, map[string]interface{}{
				"subtask": r.SubtaskID, "agent": r.AgentID, "port": r.Port, "error": r.Error,
				"reputationAfter": r.ReputationAfter, "riskAfter": r.RiskAfter,
			})
		}
	}
	for _, sel := range res.Selections {
		res.AgentsUsed = append(res.AgentsUsed, sel.AgentID)
	}

	answer, agg := h.aggregate(ctx, text, res.AgentResults)
	res.Answer = answer
	res.Aggregation = agg
	if agg.OK {
		trace(res, "hub.aggregate", fmt.Sprintf("Aggregated %d agent answer(s) into the final response", agg.Inputs), map[string]interface{}{"model": agg.Model, "provider": agg.Provider, "inputs": agg.Inputs, "latencyMs": agg.LatencyMs})
	} else {
		res.Errors = append(res.Errors, "aggregation: "+agg.Error)
		trace(res, "hub.aggregate", "Aggregation model unavailable", map[string]interface{}{"error": agg.Error, "inputs": agg.Inputs})
	}

	res.AnswerHash = hashHex(res.Answer)
	trace(res, "hub.hash", "Computed the final answer hash", map[string]interface{}{"hash": res.AnswerHash})

	anch := h.anchor(ctx, res.AnswerHash, res.TaskID)
	res.Anchor = anch
	if anch.OK {
		trace(res, "hub.anchor", "Anchored the answer hash through the existing backend", map[string]interface{}{"requestId": anch.RequestID, "requestTx": anch.RequestTx, "settleTx": anch.SettleTx, "network": anch.Network, "chainId": anch.ChainID, "coordinator": anch.Coordinator})
	} else {
		trace(res, "hub.anchor", "On-chain anchoring unavailable", map[string]interface{}{"error": anch.Error})
	}

	res.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	res.ElapsedMs = time.Since(start).Milliseconds()
	h.push(res)
	_ = h.Registry.Save()
	return res, nil
}

// aggregate synthesises the specialist answers into one final response through the shared LLM
// wrapper. A single successful answer is returned directly; if the aggregation model is
// unavailable the real agent answers are returned verbatim and clearly labelled, never invented.
func (h *Hub) aggregate(ctx context.Context, task string, results []AgentResult) (string, Aggregation) {
	good := []AgentResult{}
	for _, r := range results {
		if r.Status == "ok" {
			good = append(good, r)
		}
	}
	agg := Aggregation{Model: h.Agg.Model(), Provider: h.Agg.Provider(), Inputs: len(good)}
	if len(good) == 0 {
		agg.Error = "no agent produced an answer"
		return "", agg
	}
	if len(good) == 1 {
		agg.OK = true
		return good[0].Answer, agg
	}
	start := time.Now()
	res, err := h.Agg.Ask(ctx, buildAggregationPrompt(task, good))
	agg.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		agg.Error = err.Error()
		return compileAnswers(good), agg
	}
	agg.OK = true
	return res.Text, agg
}

func buildAggregationMessages(task string, history []deepworker.ChatTurn, good []AgentResult) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+2)
	for _, h := range history {
		role := h.Role
		if role != llm.RoleUser && role != llm.RoleAssistant && role != llm.RoleSystem {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, llm.Message{Role: role, Content: h.Content})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: buildAggregationPrompt(task, good)})
	return msgs
}

func buildAggregationPrompt(task string, good []AgentResult) string {
	var b strings.Builder
	b.WriteString("Original task:\n")
	b.WriteString(task)
	b.WriteString("\n\nSpecialist agent answers:\n")
	for i, r := range good {
		fmt.Fprintf(&b, "\n[%d] agent=%s tags=%s\n%s\n", i+1, r.AgentID, joinOrNone(r.Tags), r.Answer)
	}
	b.WriteString("\nSynthesise one coherent final answer for the user from the above. Do not invent facts.")
	return b.String()
}

func compileAnswers(good []AgentResult) string {
	var b strings.Builder
	b.WriteString("The aggregation model was unavailable, so the specialist answers are shown verbatim.\n\n")
	for i, r := range good {
		fmt.Fprintf(&b, "%d. %s (tags: %s)\n%s\n\n", i+1, r.AgentID, joinOrNone(r.Tags), r.Answer)
	}
	return strings.TrimSpace(b.String())
}

// anchor records the final answer hash on-chain through the existing backend coordinator, which
// keeps the protocol hash/blockchain evidence working. An unavailable backend is reported, never
// faked.
func (h *Hub) anchor(ctx context.Context, hash, label string) Anchor {
	a := Anchor{Hash: hash}
	if strings.TrimSpace(h.Cfg.CoordinatorURL) == "" {
		a.Error = "no coordinator configured"
		return a
	}
	body, _ := json.Marshal(map[string]string{"hash": hash, "label": label})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.Cfg.CoordinatorURL, "/")+"/anchor", bytes.NewReader(body))
	if err != nil {
		a.Error = err.Error()
		return a
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		a.Error = fmt.Sprintf("coordinator returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		return a
	}
	var out struct {
		OK          bool   `json:"ok"`
		RequestID   uint64 `json:"requestId"`
		RequestTx   string `json:"requestTx"`
		SettleTx    string `json:"settleTx"`
		Network     string `json:"network"`
		ChainID     int64  `json:"chainId"`
		Coordinator string `json:"coordinator"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	a.OK = out.OK
	a.RequestID = out.RequestID
	a.RequestTx = out.RequestTx
	a.SettleTx = out.SettleTx
	a.Network = out.Network
	a.ChainID = out.ChainID
	a.Coordinator = out.Coordinator
	a.Error = out.Error
	return a
}

func (h *Hub) push(res *Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = append([]*Result{res}, h.history...)
	if len(h.history) > 200 {
		h.history = h.history[:200]
	}
}

// History returns the most recent results, newest first.
func (h *Hub) History() []*Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Result, len(h.history))
	copy(out, h.history)
	return out
}

// Find returns a stored result by task id.
func (h *Hub) Find(taskID string) (*Result, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.history {
		if r.TaskID == taskID {
			return r, true
		}
	}
	return nil, false
}
