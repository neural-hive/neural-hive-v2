// Command ollama-agent runs the Neural Hive model-backed answer agent as an external HTTP service.
//
// It is a normal Neural Hive worker: it registers the same on-chain identity, receives the same
// TaskRequest, returns the same signed EIP-712 attestation and is routable through exactly the
// same router as the deterministic mock agents. The difference is that when the router asks for
// a natural-language answer (TaskRequest.Prompt is set) it calls a real language model through
// the reusable internal/ollama wrapper instead of computing a deterministic number.
//
// It never fabricates an answer. If the model is missing, unreachable or the request times
// out, the agent returns an HTTP error and the reason travels back to the user interface.
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
	"github.com/neural-hive/hive-node/internal/ollama"
)

const systemPrompt = "You are the reasoning agent inside Neural Hive, a verified multi-agent coordination system. " +
	"Answer the user directly, accurately and concisely. Prefer short paragraphs and lists over " +
	"walls of text, and say plainly when you are unsure."

// ollamaAnswerer adapts the reusable Ollama wrapper to the agent.Answerer interface.
type ollamaAnswerer struct {
	w *ollama.Wrapper
}

func (o ollamaAnswerer) Answer(ctx context.Context, prompt string) (string, string, string, error) {
	res, err := o.w.Ask(ctx, prompt)
	if err != nil {
		return "", "", "", err
	}
	return res.Text, res.Model, res.Provider, nil
}

func main() {
	index := flag.Int("index", 5, "deterministic agent index (the mock agents use 0..4)")
	port := flag.Int("port", 9106, "HTTP listen port")
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

	wrapper := ollama.NewWrapper(ollama.Config{
		BaseURL:     cfg.Ollama.BaseURL,
		Model:       cfg.Ollama.Model,
		System:      systemPrompt,
		Temperature: 0.4,
		Timeout:     cfg.Ollama.Timeout,
	})
	log.Printf("ollama backend=%s model=%s", wrapper.BaseURL(), wrapper.Model())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := wrapper.Available(ctx); err != nil {
		log.Printf("WARNING ollama is not ready yet: %v", err)
	} else {
		log.Printf("ollama ready: model %s is installed", wrapper.Model())
	}
	cancel()

	a := &agent.Agent{
		Index:    *index,
		Key:      key,
		Address:  addr,
		Mode:     agent.Mode(*mode),
		Skills:   splitCSV(*skills),
		Price:    big.NewInt(0),
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", *port),
		Answerer: ollamaAnswerer{w: wrapper},
	}

	chainID := big.NewInt(cfg.ChainID)
	coordinator := common.HexToAddress(cfg.Contracts.TaskCoordinator)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		modelErr := wrapper.Available(ctx)
		status := 200
		body := map[string]interface{}{
			"ok":       modelErr == nil,
			"agent":    addr.Hex(),
			"mode":     a.Mode,
			"provider": wrapper.Provider(),
			"model":    wrapper.Model(),
			"baseUrl":  wrapper.BaseURL(),
			"answerer": "ollama",
		}
		if modelErr != nil {
			body["error"] = modelErr.Error()
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
			"coordinator": coordinator.Hex(),
			"provider":    wrapper.Provider(),
			"model":       wrapper.Model(),
			"baseUrl":     wrapper.BaseURL(),
		})
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		var req agent.TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]interface{}{"error": err.Error()})
			return
		}
		resp, err := a.Handle(chainID, coordinator, req)
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

	log.Printf("ollama-agent %d listening on port %d address=%s mode=%s skills=%v", *index, *port, addr.Hex(), a.Mode, a.Skills)
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
