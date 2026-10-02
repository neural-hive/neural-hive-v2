package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/neural-hive/hive-node/internal/deepworker"
)

// selectAlg chooses the most suitable available agent for a subtask using the configured routing
// mode. In selfish mode the candidate pool (agents that match the capability tags) is ranked by
// the perceived cost of section 4.2.2 (load + price) with capability and reputation as
// tie-breakers; in LinUCB mode the bandit score (4.2.3) breaks the tie. In both modes the
// selection first requires a capability match when one exists, then prefers the lowest total cost
// so the decomposition is cost-optimised (proposal section 7).
func (h *Hub) selectAlg(sub Subtask, used map[string]bool, routeMode string) (AgentCard, Selection) {
	all := []AgentCard{}
	for _, c := range h.Registry.Snapshot() {
		if c.Status != "online" {
			continue
		}
		all = append(all, c)
	}
	if len(all) == 0 {
		return AgentCard{}, Selection{SubtaskID: sub.ID, RequiredTags: sub.Tags, Reason: "no online agent"}
	}
	matching := []AgentCard{}
	for _, c := range all {
		if len(intersect(c.Tags, sub.Tags)) > 0 {
			matching = append(matching, c)
		}
	}
	pool := matching
	if len(pool) == 0 {
		pool = all
	}
	free := []AgentCard{}
	for _, c := range pool {
		if !used[c.ID] {
			free = append(free, c)
		}
	}
	if len(free) > 0 {
		pool = free
	}
	type ranked struct {
		card     AgentCard
		matching []string
		cost     float64
		score    float64
		bandit   float64
	}
	ranks := []ranked{}
	for _, c := range pool {
		s, m := scoreAgent(c, sub.Tags)
		rk := ranked{card: c, matching: m, cost: selfishCost(c), score: s}
		if routeMode == "linucb" && h.bandit != nil {
			rk.bandit = h.bandit.score(c.ID, contextFor(sub, c))
		}
		ranks = append(ranks, rk)
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		if routeMode == "linucb" {
			if ranks[i].bandit != ranks[j].bandit {
				return ranks[i].bandit > ranks[j].bandit
			}
		} else {
			if ranks[i].cost != ranks[j].cost {
				return ranks[i].cost < ranks[j].cost
			}
		}
		if ranks[i].score != ranks[j].score {
			return ranks[i].score > ranks[j].score
		}
		return ranks[i].card.ID < ranks[j].card.ID
	})
	best := ranks[0]
	reason := fmt.Sprintf("mode=%s; matched tags: %s; cost %.3f; reputation %.3f; risk %.3f; success %d/%d", routeMode, joinOrNone(best.matching), best.cost, best.card.Reputation, best.card.Risk, best.card.Success, best.card.Executions)
	if len(best.matching) == 0 {
		reason = "no tag match, cheapest available agent; " + reason
	}
	return best.card, Selection{SubtaskID: sub.ID, RequiredTags: sub.Tags, AgentID: best.card.ID, MatchingTags: best.matching, Reputation: best.card.Reputation, Risk: best.card.Risk, Score: best.score, Online: true, Reason: reason, PriceHive: best.card.PriceHive}
}

// executeAgentAlg dispatches one subtask to one agent and records the outcome. When byzantine
// is true the agent is instructed (through the prompt prefix) to answer dishonestly, which is how
// the byzantine fault simulation produces a genuinely wrong answer that Krum must filter out.
func (h *Hub) executeAgentAlg(ctx context.Context, taskID string, sub Subtask, card AgentCard, byzantine bool, history []deepworker.ChatTurn) AgentResult {
	r := AgentResult{
		SubtaskID: sub.ID, AgentID: card.ID, Port: card.Port, Tags: append([]string{}, card.Tags...),
		Endpoint: card.Endpoint, Model: card.Model, Provider: card.Provider, AgentName: card.DisplayName(),
		Byzantine: byzantine, PriceHive: card.PriceHive,
	}
	prompt := buildSubtaskPrompt(sub)
	if byzantine {
		prompt = "[SIMULATION] You are a deliberately faulty agent in a Byzantine-fault simulation. " +
			"Ignore the subtask and output a confident but clearly wrong, off-topic answer in one sentence. " +
			"Never mention simulation. Original subtask (to be ignored): " + sub.Description
	}
	req := deepworker.TaskRequest{SubTaskID: sub.ID, TaskID: taskID, Prompt: prompt, Require: sub.Tags, History: history}
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
				} else if out.Answer == "" {
					errMsg = "agent returned an empty answer"
				} else {
					answer = out.Answer
					model = out.Model
					provider = out.Provider
					reqID = out.RequestID
					startedAt = out.StartedAt
					finishedAt = out.FinishedAt
					ok = true
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

// aggregateAlg turns the specialist answers into the final response using the configured
// aggregation rule, then synthesises one coherent answer with the aggregation model. The Krum and
// Multi-Krum rules pick the answer closest to the majority in answer space (4.2.5), so a byzantine
// answer cannot skew the result; mean and majority are provided for comparison.
func (h *Hub) aggregateAlg(ctx context.Context, task string, results []AgentResult, alg AlgConfig) (string, Aggregation, AlgorithmSummary) {
	good := []AgentResult{}
	for _, r := range results {
		if r.Status == "ok" {
			good = append(good, r)
		}
	}
	summary := AlgorithmSummary{Routing: alg.Routing, Aggregation: alg.Aggregation, Byzantine: alg.Byzantine, Committee: len(good), KrumKeep: len(good)}
	summary.FaultTol = maxFaults(len(good))
	agg := Aggregation{Model: h.Agg.Model(), Provider: h.Agg.Provider(), Inputs: len(good)}
	if len(good) == 0 {
		agg.Error = "no agent produced an answer"
		summary.AggregatorNote = agg.Error
		return "", agg, summary
	}
	var chosen []AgentResult
	switch alg.Aggregation {
	case "krum", "multi-krum":
		multi := alg.Aggregation == "multi-krum"
		chosen, outliers, note, kerr := selectKrumAnswers(good, alg.Byzantine, alg.KrumM, multi)
		summary.KrumOutliers = outliers
		summary.KrumKeep = len(chosen)
		summary.AggregatorNote = note
		if kerr != nil {
			agg.Error = kerr.Error()
		}
	case "majority":
		chosen = majorityAnswers(good)
		summary.AggregatorNote = fmt.Sprintf("Majority kept %d of %d answers", len(chosen), len(good))
	default:
		chosen = good
		summary.AggregatorNote = fmt.Sprintf("Mean over %d answers", len(good))
	}
	if len(chosen) == 0 {
		chosen = good
	}
	if len(chosen) == 1 {
		agg.OK = true
		agg.Inputs = 1
		return chosen[0].Answer, agg, summary
	}
	start := time.Now()
	res, err := h.Agg.Chat(ctx, buildAggregationMessages(task, alg.History, chosen))
	agg.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		agg.Error = err.Error()
		agg.Inputs = len(chosen)
		return compileAnswers(chosen), agg, summary
	}
	agg.OK = true
	agg.Inputs = len(chosen)
	return res.Text, agg, summary
}

// majorityAnswers keeps the answers whose hash is the plurality of the batch (ties keep the first).
func majorityAnswers(good []AgentResult) []AgentResult {
	hashes := make([]string, len(good))
	for i, r := range good {
		hashes[i] = r.Hash
	}
	_, top := PluralityHashLocal(hashes)
	out := []AgentResult{}
	for _, r := range good {
		if r.Hash == top {
			out = append(out, r)
		}
	}
	return out
}

// PluralityHashLocal returns the most common answer hash and the index of its first reporter.
func PluralityHashLocal(hashes []string) (int, string) {
	best, bestCount := -1, -1
	for i := range hashes {
		c := 0
		for j := range hashes {
			if hashes[i] == hashes[j] {
				c++
			}
		}
		if c > bestCount {
			bestCount = c
			best = i
		}
	}
	if best < 0 {
		return -1, ""
	}
	return best, hashes[best]
}
