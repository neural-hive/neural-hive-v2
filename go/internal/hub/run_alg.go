package hub

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// RunAlg runs one task with an explicit algorithm configuration (routing, aggregation, byzantine).
func (h *Hub) RunAlg(ctx context.Context, text string, hiveOverride int, pay *Payment, alg AlgConfig) (*Result, error) {
	return h.runEngine(ctx, text, hiveOverride, pay, alg)
}

// runEngine is the algorithm-aware pipeline: classify, decide HIVE, decompose, price, route
// (selfish or LinUCB), run the independent subtasks concurrently (optionally with byzantine
// agents), aggregate (mean, majority, Krum or Multi-Krum), hash and anchor, emitting live events
// and the full execution trace at every step.
func (h *Hub) runEngine(ctx context.Context, text string, hiveOverride int, pay *Payment, cfg AlgConfig) (*Result, error) {
	alg := h.resolveAlg(cfg)
	start := time.Now()
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty task")
	}
	res := &Result{TaskID: "task-" + start.UTC().Format("20060102T150405.000000000"), Task: text, StartedAt: start.UTC().Format(time.RFC3339Nano)}
	emit := func(kind, label, detail, agentID, agentName, value string, data map[string]interface{}) {
		if h.Events != nil {
			h.Events.Emit(res.TaskID, kind, label, detail, agentID, agentName, value, data)
		}
	}
	emit("received", "Request received", "Hub accepted the request", "", "", "", map[string]interface{}{"task": text})
	trace(res, "hub.received", "Hub received the task", map[string]interface{}{"taskId": res.TaskID})

	cx := Classify(text)
	res.Complexity = cx
	trace(res, "hub.complexity", "Classified the task as "+cx.Level, map[string]interface{}{"level": cx.Level, "score": cx.Score, "signals": cx.Signals, "domains": cx.Domains})
	emit("classify", "Classified", "Complexity "+cx.Level, "", "", cx.Level, map[string]interface{}{"signals": cx.Signals, "domains": cx.Domains})

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
		trace(res, "hub.payment", fmt.Sprintf("Verified payment of %s HIVE from %s (tx %s)", pay.PaidHive, pay.Payer, pay.TxHash), map[string]interface{}{"payer": pay.Payer, "paidHive": pay.PaidHive, "costHive": pay.CostHive, "agents": pay.Agents, "pricePerAgent": pay.PricePerAgent, "txHash": pay.TxHash, "block": pay.BlockNumber, "treasury": pay.Treasury})
		emit("payment", "HIVE payment completed", fmt.Sprintf("%s HIVE paid from %s", pay.PaidHive, pay.Payer), "", "", pay.PaidHive+" HIVE", map[string]interface{}{"txHash": pay.TxHash, "block": pay.BlockNumber, "treasury": pay.Treasury, "payer": pay.Payer, "costHive": pay.CostHive})
	case pay.Mode == "selftest":
		trace(res, "hub.payment", fmt.Sprintf("Startup self-test: no payment taken (a user task would cost %s HIVE)", pay.CostHive), map[string]interface{}{"costHive": pay.CostHive})
		emit("payment", "Payment (self-test)", "No payment taken; would cost "+pay.CostHive+" HIVE", "", "", pay.CostHive+" HIVE", nil)
	default:
		trace(res, "hub.payment", fmt.Sprintf("Payment not required by configuration (this task would cost %s HIVE)", pay.CostHive), map[string]interface{}{"costHive": pay.CostHive})
		emit("payment", "Cost estimated", "Would cost "+pay.CostHive+" HIVE", "", "", pay.CostHive+" HIVE", nil)
	}

	subs := Decompose(text, hive, cx.Domains)
	res.Subtasks = subs
	trace(res, "hub.decompose", fmt.Sprintf("Decomposed the task into %d subtask(s)", len(subs)), map[string]interface{}{"subtasks": subs})
	emit("decompose", "Hub decomposed the query", fmt.Sprintf("Created %d subtask(s)", len(subs)), "", "", fmt.Sprintf("%d", len(subs)), map[string]interface{}{"subtasks": subs})

	poa := priceOfAnarchy(h.Registry.Snapshot())
	routeMode := alg.Routing
	if routeMode == "selfish" && poa > 2.0 {
		routeMode = "linucb"
	}
	emit("routing", "Routing subtasks", fmt.Sprintf("Mode %s (price of anarchy %.2f)", routeMode, poa), "", "", routeMode, nil)

	used := map[string]bool{}
	cost := CostEstimate{Subtasks: len(subs)}
	total := 0.0
	baseline := parseFloatSafe(h.Cfg.PricePerAgent) * float64(len(subs))
	for _, sub := range subs {
		card, sel := h.selectAlg(sub, used, routeMode)
		if card.ID == "" {
			res.Errors = append(res.Errors, "no agent available for subtask "+sub.ID)
			trace(res, "hub.select", "No agent available for "+sub.ID, map[string]interface{}{"subtask": sub.ID, "requiredTags": sub.Tags})
			continue
		}
		used[card.ID] = true
		sel.PriceHive = card.PriceHive
		sel.CostHive = formatHiveFloat(card.PriceHive)
		res.Selections = append(res.Selections, sel)
		total += card.PriceHive
		cost.Breakdown = append(cost.Breakdown, SubtasksCost{SubtaskID: sub.ID, Tags: sub.Tags, AgentID: card.ID, PriceHive: card.PriceHive})
		trace(res, "hub.select", "Selected "+card.ID+" for "+sub.ID, map[string]interface{}{"subtask": sub.ID, "requiredTags": sub.Tags, "agent": card.ID, "port": card.Port, "tags": card.Tags, "matchingTags": sel.MatchingTags, "reputation": sel.Reputation, "risk": sel.Risk, "score": sel.Score, "reason": sel.Reason, "priceHive": card.PriceHive, "routeMode": routeMode})
		emit("select", "Routing to "+card.DisplayName(), sel.Reason, card.ID, card.DisplayName(), formatHiveFloat(card.PriceHive)+" HIVE", map[string]interface{}{"subtask": sub.ID, "requiredTags": sub.Tags, "routeMode": routeMode, "priceHive": card.PriceHive})
	}
	cost.TotalHive = formatHiveFloat(total)
	cost.PerAgentHive = h.Cfg.PricePerAgent
	if baseline > 0 {
		cost.BaselineHive = formatHiveFloat(baseline)
		cost.SavedHive = formatHiveFloat(baseline - total)
		cost.Optimized = total <= baseline
	} else {
		cost.BaselineHive = cost.TotalHive
		cost.SavedHive = "0"
	}
	res.Cost = cost
	trace(res, "hub.cost", fmt.Sprintf("Expected total cost %s HIVE for %d subtask(s)", cost.TotalHive, len(subs)), map[string]interface{}{"totalHive": cost.TotalHive, "baselineHive": cost.BaselineHive, "breakdown": cost.Breakdown})
	emit("cost", "Cost estimated", cost.TotalHive+" HIVE for "+fmt.Sprintf("%d", len(subs))+" subtask(s)", "", "", cost.TotalHive+" HIVE", nil)

	byz := map[string]bool{}
	if alg.Byzantine > 0 {
		for _, sel := range res.Selections {
			if !byz[sel.AgentID] {
				byz[sel.AgentID] = true
				res.ByzantineAgents = append(res.ByzantineAgents, sel.AgentID)
			}
			if len(byz) >= alg.Byzantine {
				break
			}
		}
		emit("simulation", "Byzantine simulation", fmt.Sprintf("%d agent(s) marked byzantine", len(res.ByzantineAgents)), "", "", fmt.Sprintf("%d", len(res.ByzantineAgents)), map[string]interface{}{"byzantineAgents": res.ByzantineAgents})
	}

	type slot struct {
		card AgentCard
		sub  Subtask
		byz  bool
	}
	slots := []slot{}
	for _, sel := range res.Selections {
		sub, sok := findSub(subs, sel.SubtaskID)
		card, cok := h.Registry.Get(sel.AgentID)
		if !sok || !cok {
			continue
		}
		slots = append(slots, slot{card: card, sub: sub, byz: byz[card.ID]})
	}
	for _, s := range slots {
		trace(res, "hub.dispatch", "Dispatched "+s.sub.ID+" to "+s.card.ID, map[string]interface{}{"subtask": s.sub.ID, "agent": s.card.ID, "port": s.card.Port, "endpoint": s.card.Endpoint, "tags": s.card.Tags, "byzantine": s.byz})
		emit("dispatch", "Dispatched to "+s.card.DisplayName(), "Subtask "+s.sub.ID, s.card.ID, s.card.DisplayName(), "", map[string]interface{}{"subtask": s.sub.ID, "port": s.card.Port, "byzantine": s.byz})
	}

	results := make([]AgentResult, len(slots))
	var wg sync.WaitGroup
	for i := range slots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = h.executeAgentAlg(ctx, res.TaskID, slots[i].sub, slots[i].card, slots[i].byz, alg.History)
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		res.AgentResults = append(res.AgentResults, r)
		if r.Status == "ok" {
			kind := "response"
			label := "Agent " + r.AgentName + " replied"
			if r.Byzantine {
				kind = "response-byzantine"
				label = "Agent " + r.AgentName + " replied (byzantine)"
			}
			trace(res, "hub.response", "Answer received from "+r.AgentID, map[string]interface{}{"subtask": r.SubtaskID, "agent": r.AgentID, "port": r.Port, "latencyMs": r.LatencyMs, "reputationBefore": r.ReputationBefore, "riskBefore": r.RiskBefore, "reputationAfter": r.ReputationAfter, "riskAfter": r.RiskAfter, "hash": r.Hash})
			emit(kind, label, answerPreview(r.Answer), r.AgentID, r.AgentName, fmt.Sprintf("%d ms", r.LatencyMs), map[string]interface{}{"subtask": r.SubtaskID, "port": r.Port, "hash": r.Hash, "answerPreview": preview(r.Answer, 240), "byzantine": r.Byzantine})
			if !r.Byzantine && h.bandit != nil {
				if sub, ok := findSub(subs, r.SubtaskID); ok {
					if card2, ok2 := h.Registry.Get(r.AgentID); ok2 {
						h.bandit.update(r.AgentID, contextFor(sub, card2), rewardOf(r))
					}
				}
			}
		} else {
			res.Errors = append(res.Errors, r.AgentID+" failed: "+r.Error)
			trace(res, "hub.response", "Subtask failed on "+r.AgentID, map[string]interface{}{"subtask": r.SubtaskID, "agent": r.AgentID, "port": r.Port, "error": r.Error, "reputationAfter": r.ReputationAfter, "riskAfter": r.RiskAfter})
			emit("response-failed", "Agent "+r.AgentName+" failed", r.Error, r.AgentID, r.AgentName, "", nil)
		}
	}
	for _, sel := range res.Selections {
		res.AgentsUsed = append(res.AgentsUsed, sel.AgentID)
	}

	emit("aggregate", "Aggregating", fmt.Sprintf("%s over %d answer(s)", alg.Aggregation, len(results)), "", "", alg.Aggregation, nil)
	answer, agg, summary := h.aggregateAlg(ctx, text, res.AgentResults, alg)
	res.Answer = answer
	res.Aggregation = agg
	res.Algorithms = summary
	if agg.OK {
		trace(res, "hub.aggregate", fmt.Sprintf("Aggregated %d agent answer(s) with %s into the final response", agg.Inputs, alg.Aggregation), map[string]interface{}{"model": agg.Model, "provider": agg.Provider, "inputs": agg.Inputs, "latencyMs": agg.LatencyMs, "aggregation": alg.Aggregation, "krumOutliers": summary.KrumOutliers})
	} else {
		res.Errors = append(res.Errors, "aggregation: "+agg.Error)
		trace(res, "hub.aggregate", "Aggregation model unavailable", map[string]interface{}{"error": agg.Error, "inputs": agg.Inputs})
	}
	emit("aggregated", "Aggregation complete", fmt.Sprintf("%s over %d answers", alg.Aggregation, agg.Inputs), "", "", alg.Aggregation, map[string]interface{}{"krumOutliers": summary.KrumOutliers, "byzantine": summary.Byzantine})

	// Settlement: if a real HIVE payment funded this task, keep the Hub fee and pay the serving
	// agents their equal share on chain. The fee percent is HUB_FEE_PERCENT; the remainder is split
	// across the agents that actually served the task.
	// Settlement also runs for the startup self-test. In the self-test the HIVE comes from the Hub treasury, not the user, but the split is still a real on-chain transfer.
	if pay != nil && (pay.Verified || pay.Mode == "selftest") {
		served := uniqueStrings(res.AgentsUsed)
		if len(served) > 0 {
			totalWei, _ := new(big.Int).SetString(pay.PaidWei, 10)
			if totalWei == nil || totalWei.Sign() <= 0 {
				totalWei, _ = new(big.Int).SetString(pay.CostWei, 10)
			}
			settle := h.SettlePayment(ctx, res.TaskID, served, totalWei)
			res.Settlement = &settle
			trace(res, "hub.settlement", fmt.Sprintf("Split %s HIVE: hub fee %s (%.2f%%), %d agent(s) paid equally", settle.TotalHive, settle.FeeHive, settle.FeePercent, settle.Agents), map[string]interface{}{"feeHive": settle.FeeHive, "feePercent": settle.FeePercent, "agents": settle.Agents, "payouts": settle.Payouts, "error": settle.Error})
			emit("settlement", "Paid the serving agents", settle.FeeHive+" HIVE hub fee, agents paid", "", "", settle.FeeHive+" HIVE", map[string]interface{}{"payouts": settle.Payouts, "feePercent": settle.FeePercent})
		}
	}

	res.AnswerHash = hashHex(res.Answer)
	trace(res, "hub.hash", "Computed the final answer hash", map[string]interface{}{"hash": res.AnswerHash})
	emit("final", "Final response ready", "The aggregated answer is ready", "", "", res.AnswerHash, map[string]interface{}{"answerChars": len(res.Answer)})

	anch := h.anchor(ctx, res.AnswerHash, res.TaskID)
	res.Anchor = anch
	if anch.OK {
		trace(res, "hub.anchor", "Anchored the answer hash through the existing backend", map[string]interface{}{"requestId": anch.RequestID, "requestTx": anch.RequestTx, "settleTx": anch.SettleTx, "network": anch.Network, "chainId": anch.ChainID, "coordinator": anch.Coordinator})
		emit("anchor", "Anchored on-chain", "Answer hash recorded on-chain", "", "", anch.RequestTx, map[string]interface{}{"requestId": anch.RequestID, "settleTx": anch.SettleTx, "network": anch.Network})
	} else {
		trace(res, "hub.anchor", "On-chain anchoring unavailable", map[string]interface{}{"error": anch.Error})
		emit("anchor-failed", "On-chain anchor unavailable", anch.Error, "", "", "", nil)
	}

	res.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	res.ElapsedMs = time.Since(start).Milliseconds()
	h.push(res)
	_ = h.Registry.Save()
	return res, nil
}
