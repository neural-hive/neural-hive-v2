// Command agent runs one Neural Hive worker agent as an external HTTP service.
//
// Agents are deliberately separate processes speaking HTTP: the protocol must not assume it can
// call into the agent, it must assume the agent hands back a signed answer. This binary is the
// mock-AI boundary in the prototype (deterministic compute instead of an LLM).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/config"
)

func main() {
	index := flag.Int("index", 0, "deterministic agent index")
	port := flag.Int("port", 9101, "HTTP listen port")
	seed := flag.String("seed", "neural-hive-devnet", "deterministic key seed")
	mode := flag.String("mode", "honest", "behaviour: honest, lying or error")
	skills := flag.String("skills", "summarization", "comma separated skill list")
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
	a := &agent.Agent{
		Index:    *index,
		Key:      key,
		Address:  addr,
		Mode:     agent.Mode(*mode),
		Skills:   splitCSV(*skills),
		Price:    big.NewInt(0),
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", *port),
	}

	chainID := big.NewInt(cfg.ChainID)
	coordinator := common.HexToAddress(cfg.Contracts.TaskCoordinator)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "agent": addr.Hex(), "mode": a.Mode})
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
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		writeJSON(w, 200, resp)
	})

	log.Printf("agent %d listening on port %d address=%s mode=%s", *index, *port, addr.Hex(), a.Mode)
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
	cur := ""
	for _, r := range s {
		if r == 44 {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
