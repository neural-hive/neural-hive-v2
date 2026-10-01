// Command relayer is a permissionless Neural Hive relayer.
//
// It receives an already-signed agent response over HTTP and forwards it to the TaskCoordinator.
// The relayer never sees an agent private key and cannot mint a valid attestation, so a rogue
// relayer can at worst censor or replay - replays are rejected on-chain by the per-agent nonce.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/common"

	"github.com/neural-hive/hive-node/internal/chain"
	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/coordinator"
)

func main() {
	port := flag.Int("port", 9300, "HTTP listen port")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	key := os.Getenv("RELAYER_PRIVATE_KEY")
	if key == "" {
		key = cfg.PrivateKey2
	}
	if key == "" {
		log.Fatalf("no relayer key configured (RELAYER_PRIVATE_KEY or PRIVATE_KEY_2)")
	}
	cl, err := chain.New(cfg, key)
	if err != nil {
		log.Fatalf("chain client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "relayer": cl.From.Hex()})
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		var rr coordinator.RelayRequest
		if err := json.NewDecoder(r.Body).Decode(&rr); err != nil {
			writeJSON(w, 400, coordinator.RelayResponse{OK: false, Error: err.Error()})
			return
		}
		value, ok := new(big.Int).SetString(rr.OutputValue, 10)
		if !ok {
			writeJSON(w, 400, coordinator.RelayResponse{OK: false, Error: "bad outputValue"})
			return
		}
		err := cl.SubmitResponse(
			rr.StepID,
			common.HexToAddress(rr.Agent),
			rr.OutputHash,
			value,
			new(big.Int).SetUint64(rr.Nonce),
			new(big.Int).SetUint64(rr.Deadline),
			rr.Signature,
		)
		if err != nil {
			writeJSON(w, 200, coordinator.RelayResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, 200, coordinator.RelayResponse{OK: true})
	})

	log.Printf("relayer %s listening on port %d", cl.From.Hex(), *port)
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
