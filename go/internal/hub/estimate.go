package hub

// EstimateCost prices a request the same way runEngine will route it, without executing any agent,
// so the UI can show the estimated HIVE cost before the user pays. It decomposes the task, selects
// the cheapest capable agent for each subtask under the requested routing mode and sums the prices.
func (h *Hub) EstimateCost(text string, hiveOverride int, alg AlgConfig) (CostEstimate, []Subtask, []Selection, AlgorithmSummary) {
	a := h.resolveAlg(alg)
	// Mirror runEngine: a selfish request is automatically upgraded to LinUCB when the measured
	// price of anarchy exceeds 2, so the price quoted here is the price the engine will charge.
	if a.Routing == "selfish" && priceOfAnarchy(h.Registry.Snapshot()) > 2.0 {
		a.Routing = "linucb"
	}
	cx := Classify(text)
	hive := h.decideHIVE(cx, SplitObjectives(text), hiveOverride)
	subs := Decompose(text, hive, cx.Domains)
	used := map[string]bool{}
	sels := []Selection{}
	cost := CostEstimate{Subtasks: len(subs)}
	total := 0.0
	baseline := parseFloatSafe(h.Cfg.PricePerAgent) * float64(len(subs))
	for _, sub := range subs {
		card, sel := h.selectAlg(sub, used, a.Routing)
		if card.ID == "" {
			continue
		}
		used[card.ID] = true
		sel.PriceHive = card.PriceHive
		sel.CostHive = formatHiveFloat(card.PriceHive)
		sels = append(sels, sel)
		total += card.PriceHive
		cost.Breakdown = append(cost.Breakdown, SubtasksCost{SubtaskID: sub.ID, Tags: sub.Tags, AgentID: card.ID, PriceHive: card.PriceHive})
	}
	cost.TotalHive = formatHiveFloat(total)
	cost.PerAgentHive = h.Cfg.PricePerAgent
	if baseline > 0 {
		cost.BaselineHive = formatHiveFloat(baseline)
		cost.SavedHive = formatHiveFloat(baseline - total)
		cost.Optimized = total <= baseline
	} else {
		cost.BaselineHive = cost.TotalHive
	}
	summary := AlgorithmSummary{Routing: a.Routing, Aggregation: a.Aggregation, Byzantine: a.Byzantine, Committee: len(subs), FaultTol: maxFaults(len(subs)), KrumKeep: len(subs), PriceOfAnarchy: priceOfAnarchy(h.Registry.Snapshot())}
	return cost, subs, sels, summary
}
