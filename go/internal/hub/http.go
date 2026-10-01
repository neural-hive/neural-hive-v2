package hub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// HealthCheck probes every registered agent once and records its health, provider and model.
func (h *Hub) HealthCheck(ctx context.Context) {
	for _, card := range h.Registry.Snapshot() {
		tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(tctx, http.MethodGet, strings.TrimRight(card.Endpoint, "/")+"/health", nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := h.client.Do(req)
		if err != nil {
			h.Registry.SetHealth(card.ID, "offline", "", "")
			cancel()
			continue
		}
		var body struct {
			OK       bool   `json:"ok"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		cancel()
		if resp.StatusCode == http.StatusOK && body.OK {
			h.Registry.SetHealth(card.ID, "online", body.Provider, body.Model)
		} else {
			h.Registry.SetHealth(card.ID, "unavailable", body.Provider, body.Model)
		}
	}
}

// StartHealthPoller probes the pool periodically until the context is cancelled.
func (h *Hub) StartHealthPoller(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	h.HealthCheck(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.HealthCheck(ctx)
			}
		}
	}()
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler is the Hub HTTP surface: task submission, the agent registry, the execution trace,
// history, network observability and the agentic workspace frontend.
func (h *Hub) Handler(webDir string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /payment/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, h.paymentConfig())
	})

	// POST /quote prices a task before the user pays: it returns the number of agents the task
	// needs and the cost in HIVE, plus the token, treasury and chain MetaMask must use.
	mux.HandleFunc("POST /quote", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Task    string `json:"task"`
			Request string `json:"request"`
			HIVE    int    `json:"hive"`
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
		q := h.Quote(text, body.HIVE)
		out := h.paymentConfig()
		out["quoteId"] = q.ID
		out["hive"] = q.HIVE
		out["complexity"] = q.Complexity
		out["costHive"] = FormatHive(q.CostWei)
		out["costWei"] = q.CostWei.String()
		out["expiresAt"] = q.ExpiresAt.UTC().Format(time.RFC3339)
		writeJSON(w, http.StatusOK, out)
	})

	// POST /faucet tops up a devnet wallet with test HIVE (and gas) through the coordinator, which
	// owns the admin key. It only exists on the local ganache devnet.
	mux.HandleFunc("POST /faucet", func(w http.ResponseWriter, r *http.Request) {
		if !h.faucetEnabled() {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"error": "the faucet only exists on the local devnet; fund your wallet with HIVE yourself"})
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.Cfg.CoordinatorURL, "/")+"/faucet", bytes.NewReader(raw))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": "backend faucet unavailable: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	})

	mux.HandleFunc("POST /task", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Task    string `json:"task"`
			Request string `json:"request"`
			HIVE    int    `json:"hive"`
			QuoteID string `json:"quoteId"`
			TxHash  string `json:"txHash"`
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
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
		defer cancel()

		var pay *Payment
		hive := body.HIVE
		if h.Cfg.PaymentRequired {
			if h.isSelfTest(r) {
				pay = &Payment{Mode: "selftest"}
			} else {
				p, paidAgents, err := h.Settle(ctx, body.QuoteID, body.TxHash, text)
				if err != nil {
					code := http.StatusPaymentRequired
					if pe, ok := err.(*PaymentError); ok {
						code = pe.Status
					}
					out := h.paymentConfig()
					out["error"] = err.Error()
					out["paymentRequired"] = true
					writeJSON(w, code, out)
					return
				}
				pay = p
				hive = paidAgents
			}
		}
		result, err := h.RunPaid(ctx, text, hive, pay)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("GET /agents", func(w http.ResponseWriter, r *http.Request) {
		cards := h.Registry.Snapshot()
		online := 0
		for _, c := range cards {
			if c.Status == "online" {
				online++
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"count": len(cards), "online": online, "agents": cards})
	})

	mux.HandleFunc("GET /agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		card, ok := h.Registry.Get(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "unknown agent"})
			return
		}
		writeJSON(w, http.StatusOK, card)
	})

	mux.HandleFunc("GET /trace/{id}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := h.Find(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "unknown task"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		hist := h.History()
		out := make([]map[string]interface{}, 0, len(hist))
		for _, res := range hist {
			out = append(out, map[string]interface{}{
				"taskId": res.TaskID, "task": res.Task, "complexity": res.Complexity.Level,
				"hive": res.HIVE, "payment": res.Payment, "answer": res.Answer, "answerHash": res.AnswerHash,
				"agentsUsed": res.AgentsUsed, "elapsedMs": res.ElapsedMs, "startedAt": res.StartedAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"count": len(out), "tasks": out})
	})

	mux.HandleFunc("GET /network", func(w http.ResponseWriter, r *http.Request) {
		cards := h.Registry.Snapshot()
		online, totalSuccess, totalFailure, latSum, exec := 0, 0, 0, int64(0), 0
		for _, c := range cards {
			if c.Status == "online" {
				online++
			}
			totalSuccess += c.Success
			totalFailure += c.Failure
			latSum += c.TotalLatencyMs
			exec += c.Executions
		}
		avg := int64(0)
		if exec > 0 {
			avg = latSum / int64(exec)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agents": len(cards), "online": online,
			"completedTasks": len(h.History()), "agentSuccess": totalSuccess, "agentFailure": totalFailure,
			"avgAgentLatencyMs": avg,
			"provider":          h.Agg.Provider(), "aggregatorModel": h.Agg.Model(),
			"hive":        map[string]interface{}{"default": h.Cfg.HIVE.Default, "complex": h.Cfg.HIVE.Complex, "max": h.Cfg.HIVE.Max},
			"payment":     h.paymentConfig(),
			"coordinator": h.Cfg.CoordinatorURL,
		})
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "agents": len(h.Registry.Snapshot()), "online": h.Registry.OnlineCount(),
			"provider": h.Agg.Provider(), "aggregatorModel": h.Agg.Model(),
		})
	})

	if webDir != "" {
		fs := http.FileServer(http.Dir(webDir))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			started := time.Now()
			fs.ServeHTTP(rec, r)
			if h.UILog != nil {
				h.UILog.Println("ui", r.Method, r.URL.Path, rec.status, time.Since(started).Milliseconds(), "ms", "from", r.RemoteAddr)
			}
		})
	}
	return withCORS(mux)
}

// statusRecorder captures the HTTP status so the workspace UI access log can record it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// paymentConfig is what the browser needs to charge for a task: the price, the token, the
// treasury and the chain MetaMask has to be on.
func (h *Hub) paymentConfig() map[string]interface{} {
	return map[string]interface{}{
		"paymentRequired":  h.Cfg.PaymentRequired,
		"symbol":           hiveSymbol,
		"pricePerAgent":    h.Cfg.PricePerAgent,
		"pricePerAgentWei": h.priceWeiString(),
		"token":            h.Cfg.TokenAddress,
		"treasury":         h.Cfg.Treasury,
		"chainId":          h.Cfg.ChainID,
		"chainIdHex":       fmt.Sprintf("0x%x", h.Cfg.ChainID),
		"network":          h.Cfg.Network,
		"rpcUrl":           h.Cfg.RPCURL,
		"faucet":           h.faucetEnabled(),
	}
}

func (h *Hub) priceWeiString() string {
	if h.Cfg.PricePerAgentWei == nil {
		return "0"
	}
	return h.Cfg.PricePerAgentWei.String()
}

// faucetEnabled is true only on the local ganache devnet.
func (h *Hub) faucetEnabled() bool {
	return strings.EqualFold(h.Cfg.Network, "ganache") && strings.TrimSpace(h.Cfg.CoordinatorURL) != ""
}

// isSelfTest recognises the startup verification run by start.ps1: a loopback caller presenting
// the per-start secret. Browsers never have it, so a user task can never skip payment this way.
func (h *Hub) isSelfTest(r *http.Request) bool {
	tok := h.Cfg.SelfTestToken
	if tok == "" {
		return false
	}
	got := r.Header.Get("X-Hive-Selftest")
	if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
