package hub

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/neural-hive/hive-node/internal/deepworker"
)

// registerExtraRoutes adds the live-activity, cost-estimate and agent-management endpoints.
func (h *Hub) registerExtraRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /estimate", h.handleEstimate)
	mux.HandleFunc("GET /agents/limit", h.handleGetAgentLimit)
	mux.HandleFunc("POST /agents/limit", h.handleSetAgentLimit)
	mux.HandleFunc("GET /events", h.handleEventsSSE)
	mux.HandleFunc("GET /events/{id}", h.handleEventsReplay)
	mux.HandleFunc("POST /agents/register", h.handleAgentRegister)
	mux.HandleFunc("POST /agents/register/confirm", h.handleAgentRegisterConfirm)
	mux.HandleFunc("POST /agents/{id}/price", h.handleAgentPrice)
	mux.HandleFunc("GET /agents/{id}/wallet", h.handleAgentWallet)
	mux.HandleFunc("POST /agents/{id}/withdraw", h.handleAgentWithdraw)
}

// handleEstimate prices a request with the algorithm config, before anything is executed.
func (h *Hub) handleEstimate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Task         string                `json:"task"`
		Request      string                `json:"request"`
		HIVE         int                   `json:"hive"`
		Routing      string                `json:"routing"`
		Aggregation  string                `json:"aggregation"`
		Byzantine    int                   `json:"byzantine"`
		KrumM        int                   `json:"krumM"`
		History      []deepworker.ChatTurn `json:"history"`
		HistoryLimit int                   `json:"historyLimit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	text := strings.TrimSpace(body.Task)
	if text == "" {
		text = strings.TrimSpace(body.Request)
	}
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "task is required"})
		return
	}
	alg := AlgConfig{Routing: body.Routing, Aggregation: body.Aggregation, Byzantine: body.Byzantine, KrumM: body.KrumM, History: h.trimHistoryLimit(body.History, body.HistoryLimit)}
	cost, subs, sels, summary := h.EstimateCost(text, body.HIVE, alg)
	writeJSON(w, http.StatusOK, map[string]interface{}{"cost": cost, "subtasks": subs, "selections": sels, "algorithms": summary, "pricePerAgent": h.Cfg.PricePerAgent, "symbol": hiveSymbol})
}

// agentLimitView is the JSON shape of the max-agents cap: the current cap, the configured ceiling
// (HIVE_MAX, beyond which the cap cannot be raised) and the online agent count.
func (h *Hub) agentLimitView() map[string]interface{} {
	ceiling := h.Cfg.HIVE.Max
	if ceiling < 1 {
		ceiling = 1
	}
	return map[string]interface{}{
		"maxAgents":        h.MaxAgents(),
		"maxAgentsCeiling": ceiling,
		"default":          h.Cfg.HIVE.Default,
		"complex":          h.Cfg.HIVE.Complex,
		"online":           h.Registry.OnlineCount(),
		"agents":           len(h.Registry.Snapshot()),
	}
}

// handleGetAgentLimit reports the current runtime cap on how many agents one task may be sent to.
func (h *Hub) handleGetAgentLimit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.agentLimitView())
}

// handleSetAgentLimit lowers or raises the runtime cap on how many agents one task may be sent to.
// The cap is clamped to [1, HIVE_MAX]; every subsequent routing decision (decideHIVE) is bounded by it.
func (h *Hub) handleSetAgentLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Max       int `json:"max"`
		MaxAgents int `json:"maxAgents"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	n := body.Max
	if n == 0 {
		n = body.MaxAgents
	}
	h.SetMaxAgents(n)
	writeJSON(w, http.StatusOK, h.agentLimitView())
}

// handleEventsSSE streams the live execution activity to the browser as Server-Sent Events.
func (h *Hub) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	lastSeq := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			lastSeq = n
		}
	}
	for _, ev := range h.Events.Since(lastSeq) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		lastSeq = ev.Seq
	}
	flusher.Flush()
	_, ch, unsub := h.Events.Subscribe()
	defer unsub()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handleEventsReplay returns the full live-event buffer for one run (the Execution Details page).
func (h *Hub) handleEventsReplay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out := []LiveEvent{}
	for _, ev := range h.Events.Snapshot() {
		if ev.TaskID == id {
			out = append(out, ev)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"taskId": id, "count": len(out), "events": out})
}

// handleAgentRegister verifies a new agent by really calling its endpoint, then registers it in the
// Hub registry and returns the on-chain registration calldata the owner sends from MetaMask.
func (h *Hub) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string   `json:"name"`
		Endpoint  string   `json:"endpoint"`
		Tags      []string `json:"tags"`
		PriceHive float64  `json:"priceHive"`
		Type      string   `json:"type"`
		Model     string   `json:"model"`
		Owner     string   `json:"owner"`
		Signer    string   `json:"signer"`
		TxHash    string   `json:"txHash"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	endpoint := strings.TrimRight(strings.TrimSpace(body.Endpoint), "/")
	if strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "name is required"})
		return
	}
	if endpoint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "endpoint is required"})
		return
	}
	tags := body.Tags
	if len(tags) == 0 {
		tags = []string{"analysis", "reasoning"}
	}
	id := "ext-" + slug(strings.ToLower(strings.TrimSpace(body.Name)))
	live, detail, model, provider := h.probeAgent(endpoint)
	if !live {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "status": "rejected", "detail": detail, "id": id})
		return
	}
	card := AgentCard{ID: id, Endpoint: endpoint, Tags: normTags(tags), Name: strings.TrimSpace(body.Name), Model: model, Provider: provider, PriceHive: body.PriceHive, Status: "offline"}
	onchain := map[string]interface{}{"contract": h.Cfg.RegistryAddress, "calldata": h.registrationCalldata(body.Signer, body.Owner), "abi": registrationABI, "chainId": h.Cfg.ChainID, "chainIdHex": fmt.Sprintf("0x%x", h.Cfg.ChainID), "rpcUrl": h.Cfg.RPCURL}
	fee := h.Cfg.RegistrationFeeWei
	if fee == nil {
		fee = big.NewInt(0)
	}
	if !h.Cfg.PaymentRequired || fee.Sign() == 0 {
		card.Status = "online"
		created := h.Registry.EnsureDynamic(card)
		if !created {
			h.Registry.Register(card)
			h.Registry.SetPrice(id, body.PriceHive)
		}
		h.Registry.SetHealth(id, "online", provider, model)
		_ = h.Registry.Save()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "live", "detail": detail, "agent": h.cardView(id), "id": id, "onchain": onchain})
		return
	}
	regID := fmt.Sprintf("reg-%d", time.Now().UnixNano())
	h.regMu.Lock()
	if h.pendingRegs == nil {
		h.pendingRegs = map[string]*pendingRegistration{}
	}
	for id2, p := range h.pendingRegs {
		if time.Since(p.At) > 30*time.Minute {
			delete(h.pendingRegs, id2)
		}
	}
	h.pendingRegs[regID] = &pendingRegistration{Card: card, Detail: detail, At: time.Now()}
	h.pendingRegs[id] = h.pendingRegs[regID]
	h.regMu.Unlock()
	_ = onchain
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "status": "verified", "detail": detail, "id": id, "registrationId": regID, "agent": card,
		"payment": map[string]interface{}{"symbol": hiveSymbol, "treasury": h.Cfg.Treasury, "token": h.Cfg.TokenAddress, "costHive": h.Cfg.RegistrationFee, "costWei": fee.String(), "chainId": h.Cfg.ChainID, "chainIdHex": fmt.Sprintf("0x%x", h.Cfg.ChainID), "rpcUrl": h.Cfg.RPCURL},
		"onchain": onchain,
	})
}

// handleAgentRegisterConfirm verifies the HIVE registration fee on chain and only then promotes the
// verified agent into the live registry. Any wallet may register, but it must really pay HIVE.
func (h *Hub) handleAgentRegisterConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID             string `json:"id"`
		RegistrationID string `json:"registrationId"`
		TxHash         string `json:"txHash"`
		Owner          string `json:"owner"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	regID := strings.TrimSpace(body.RegistrationID)
	if regID == "" {
		regID = strings.TrimSpace(body.ID)
	}
	h.regMu.Lock()
	pending, ok := h.pendingRegs[regID]
	h.regMu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "unknown or expired registration"})
		return
	}
	if strings.TrimSpace(body.TxHash) == "" {
		writeJSON(w, http.StatusPaymentRequired, map[string]interface{}{"error": "the HIVE registration fee has not been paid yet", "paymentRequired": true})
		return
	}
	fee := h.Cfg.RegistrationFeeWei
	if fee == nil {
		fee = big.NewInt(0)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	payer, paid, _, err := h.VerifyRegistrationFee(ctx, body.TxHash, fee)
	if err != nil {
		code := http.StatusPaymentRequired
		if pe, ok2 := err.(*PaymentError); ok2 {
			code = pe.Status
		}
		writeJSON(w, code, map[string]interface{}{"error": err.Error(), "paymentRequired": true})
		return
	}
	card := pending.Card
	card.Status = "online"
	created := h.Registry.EnsureDynamic(card)
	if !created {
		h.Registry.Register(card)
		h.Registry.SetPrice(card.ID, card.PriceHive)
	}
	h.Registry.SetHealth(card.ID, "online", card.Provider, card.Model)
	h.Registry.SetOwner(card.ID, payer.Hex())
	// Deploy the per-agent AgentWallet smart contract owned by the registering wallet.
	if _, werr := h.EnsureAgentWallet(ctx, card.ID, payer.Hex()); werr != nil {
		logf("agent-wallet %s (register): %v", card.ID, werr)
	}
	_ = h.Registry.Save()
	h.regMu.Lock()
	delete(h.pendingRegs, regID)
	h.regMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "live", "detail": pending.Detail, "agent": h.cardView(card.ID), "id": card.ID, "payer": payer.Hex(), "paidHive": FormatHive(paid), "txHash": body.TxHash})
}

// handleAgentPrice sets an agent advertised price and returns the on-chain updatePrice details.
func (h *Hub) handleAgentPrice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		PriceHive float64 `json:"priceHive"`
		TxHash    string  `json:"txHash"`
		From      string  `json:"from"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	card, ok := h.Registry.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "unknown agent"})
		return
	}
	owner := h.ownerOf(card)
	from := strings.TrimSpace(body.From)
	if from == "" {
		from = owner
	}
	// Only the on-chain owner of the agent may change its price.
	if owner != "" && !strings.EqualFold(owner, from) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{"error": "only the agent owner (" + owner + ") can update this price", "owner": owner})
		return
	}
	priceWei := hiveToWei(body.PriceHive)
	// The owner must really send updatePrice(uint256) from their own wallet.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := h.VerifyPriceUpdate(ctx, body.TxHash, from, priceWei); err != nil {
		code := http.StatusPaymentRequired
		if pe, ok2 := err.(*PaymentError); ok2 {
			code = pe.Status
		}
		writeJSON(w, code, map[string]interface{}{"error": err.Error(), "owner": owner, "paymentRequired": true})
		return
	}
	if !h.Registry.SetPrice(id, body.PriceHive) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "unknown agent"})
		return
	}
	_ = h.Registry.Save()
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "agent": h.cardView(id), "owner": owner, "txHash": body.TxHash, "onchain": map[string]interface{}{"contract": h.Cfg.RegistryAddress, "method": "updatePrice", "selector": "updatePrice(uint256)", "chainId": h.Cfg.ChainID, "chainIdHex": fmt.Sprintf("0x%x", h.Cfg.ChainID), "rpcUrl": h.Cfg.RPCURL, "priceWei": priceWei.String()}})
}

func (h *Hub) cardView(id string) AgentCard {
	c, _ := h.Registry.Get(id)
	return c
}

// probeAgent really calls a candidate agent to confirm it answers, returning liveness and provenance.
func (h *Hub) probeAgent(endpoint string) (bool, string, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]interface{}{"subTaskId": "registration-probe", "taskId": "registration", "prompt": "Reply with one short sentence confirming you are online."})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/task", strings.NewReader(string(payload)))
	if err != nil {
		return false, "bad endpoint: " + err.Error(), "", ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return false, "the agent did not respond: " + err.Error(), "", ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("agent returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))), "", ""
	}
	var out struct {
		Answer   string `json:"answer"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(raw, &out)
	if strings.TrimSpace(out.Answer) == "" {
		return false, "the agent replied but produced no answer text", out.Model, out.Provider
	}
	return true, "verified: agent answered a live probe", out.Model, out.Provider
}

func normTags(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		out = []string{"analysis", "reasoning"}
	}
	return out
}

func slug(s string) string {
	out := []rune{}
	for _, r := range s {
		if (r >= 97 && r <= 122) || (r >= 48 && r <= 57) {
			out = append(out, r)
		} else if r == 32 || r == 45 || r == 95 {
			out = append(out, 45)
		}
	}
	s = strings.Trim(string(out), "-")
	if s == "" {
		s = "agent"
	}
	return s
}

// registrationCalldata builds the registerAgent(address,int256[8],uint256,string,uint256,bytes) call
// the agent owner signs from MetaMask: signer defaults to the owner, capability is zero (set later),
// the deadline is one hour out and the signature is empty when signer == owner.
// registrationCalldata builds the registerAgent(address,int256[8],uint256,string,uint256,bytes)
// call the agent owner signs from MetaMask. When signer == owner the call carries no EIP-712
// signature; otherwise the owner must first sign the AgentRegistration digest. The capability is
// zero (refined off-chain) and the deadline is one hour out, matching CapabilityRegistry.registerAgent.
func (h *Hub) registrationCalldata(signer, owner string) string {
	s := strings.TrimSpace(signer)
	if s == "" {
		s = strings.TrimSpace(owner)
	}
	addr := common.HexToAddress(s)
	data := registerAgentCalldata(addr, big.NewInt(0), h.Cfg.Treasury)
	return "0x" + hex.EncodeToString(data)
}

const registrationABI = "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"signer\",\"type\":\"address\"},{\"internalType\":\"int256[8]\",\"name\":\"capability\",\"type\":\"int256[8]\"},{\"internalType\":\"uint256\",\"name\":\"price\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"endpoint\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"deadline\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"}],\"name\":\"registerAgent\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]"

// handleAgentWallet reports the deterministic on-chain wallet and HIVE balance of one agent.
func (h *Hub) handleAgentWallet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	info := h.AgentWalletContractInfo(ctx, id)
	if info == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "unknown agent"})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleAgentWithdraw returns the AgentWallet.withdraw() transaction the owner signs from MetaMask
// (withdraw instructions), or verifies a submitted owner withdraw transaction. The vault contract
// itself enforces that only the agent owner can withdraw.
func (h *Hub) handleAgentWithdraw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Owner  string `json:"owner"`
		From   string `json:"from"`
		TxHash string `json:"txHash"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	owner := strings.TrimSpace(body.Owner)
	if owner == "" {
		owner = strings.TrimSpace(body.From)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if strings.TrimSpace(body.TxHash) != "" {
		out, err := h.VerifyOwnerWithdraw(ctx, id, owner, body.TxHash)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out, err := h.WithdrawTxData(ctx, id, owner)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}
