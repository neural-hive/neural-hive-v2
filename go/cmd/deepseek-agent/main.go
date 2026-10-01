// Command deepseek-agent runs the Neural Hive DeepSeek-backed answer agent as an external HTTP
// service.
//
// It is a normal Neural Hive worker: it registers the same on-chain identity, receives the same
// TaskRequest, returns the same signed EIP-712 attestation and is routable through exactly the
// same router as the deterministic mock agents. The only difference is that when the router asks
// for a natural-language answer (TaskRequest.Prompt is set) it calls the DeepSeek API through
// the reusable internal/llm wrapper and the internal/deepseek provider instead of computing a
// deterministic number.
//
// It never fabricates an answer. If the key is missing, the model is unreachable, the provider
// rate-limits the call or the request times out, the agent returns an HTTP error carrying the
// real reason, which travels back to the user interface.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/deepseek"
	"github.com/neural-hive/hive-node/internal/llm"
)

const systemPrompt = "You are the reasoning agent inside Neural Hive, a verified multi-agent " +
	"coordination system. Answer the user directly, accurately and concisely. Prefer short " +
	"paragraphs and lists over walls of text, and say plainly when you are unsure."

// deepseekAnswerer adapts the reusable llm wrapper to the agent.Answerer interface.
type deepseekAnswerer struct {
	w *llm.Wrapper
}

func (d deepseekAnswerer) Answer(ctx context.Context, prompt string) (string, string, string, error) {
	res, err := d.w.Ask(ctx, prompt)
	if err != nil {
		return "", "", "", err
	}
	return res.Text, res.Model, res.Provider, nil
}

func main() {
	index := flag.Int("index", 6, "deterministic agent index (the mock agents use 0..4)")
	port := flag.Int("port", 9107, "HTTP listen port")
	seed := flag.String("seed", "neural-hive-devnet", "deterministic key seed")
	mode := flag.String("mode", "honest", "behaviour: honest, lying or error")
	skills := flag.String("skills", "explain,summarization,reasoning", "comma separated skill list")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	key, err := agent.DeriveKey(*seed, *index)
	if err != nil {
		log.Fatalf("derive key: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	client := deepseek.NewClient(cfg.DeepSeek.APIKey, cfg.DeepSeek.BaseURL, cfg.DeepSeek.Timeout, nil)
	wrapper := llm.NewWrapper(llm.Config{
		Provider:    client,
		Model:       cfg.DeepSeek.Model,
		System:      systemPrompt,
		Temperature: 0.4,
	})
	log.Printf("deepseek provider=%s model=%s base=%s", wrapper.Provider(), wrapper.Model(), client.BaseURL())
	if !client.HasKey() {
		log.Printf("WARNING deepseek is not configured: %v", deepseek.ErrNoAPIKey)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		if models, err := client.Models(ctx); err != nil {
			log.Printf("WARNING deepseek is not ready: %v", err)
		} else {
			log.Printf("deepseek ready: %s (advertised: %s)", wrapper.Model(), strings.Join(models, ", "))
		}
		cancel()
	}

	a := &agent.Agent{
		Index:    *index,
		Key:      key,
		Address:  addr,
		Mode:     agent.Mode(*mode),
		Skills:   splitCSV(*skills),
		Price:    big.NewInt(0),
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", *port),
		Answerer: deepseekAnswerer{w: wrapper},
	}

	chainID := big.NewInt(cfg.ChainID)
	coordinatorAddr := common.HexToAddress(cfg.Contracts.TaskCoordinator)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]interface{}{
			"ok":       client.HasKey(),
			"agent":    addr.Hex(),
			"mode":     a.Mode,
			"provider": wrapper.Provider(),
			"model":    wrapper.Model(),
			"baseUrl":  client.BaseURL(),
			"answerer": "deepseek",
		}
		status := 200
		if !client.HasKey() {
			body["error"] = deepseek.ErrNoAPIKey.Error()
			status = 503
		}
		writeJSON(w, status, body)
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{
			"agent":       addr.Hex(),
			"index":       a.Index,
			"mode":        a.Mode,
			"skills":      a.Skills,
			"endpoint":    a.Endpoint,
			"capability":  capability.FromSkills(a.Skills).ToOnChain(),
			"chainId":     cfg.ChainID,
			"coordinator": coordinatorAddr.Hex(),
			"provider":    wrapper.Provider(),
			"model":       wrapper.Model(),
			"baseUrl":     client.BaseURL(),
		})
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		var req agent.TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]interface{}{"error": err.Error()})
			return
		}
		resp, err := a.Handle(chainID, coordinatorAddr, req)
		if err != nil {
			log.Printf("task request=%d step=%d failed: %v", req.RequestID, req.StepID, err)
			code := 500
			if err == agent.ErrRefused {
				code = 503
			} else if strings.TrimSpace(req.Prompt) != "" {
				code = 502
			}
			writeJSON(w, code, map[string]interface{}{"error": err.Error()})
			return
		}
		writeJSON(w, 200, resp)
	})

	log.Printf("deepseek-agent index=%d listening on port %d address=%s mode=%s skills=%v", *index, *port, addr.Hex(), a.Mode, a.Skills)
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func splitCSV(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
