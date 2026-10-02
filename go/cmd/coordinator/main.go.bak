// Command coordinator runs the Neural Hive coordination layer as a service.
//
// Endpoints:
//
//	POST /bootstrap  fund, register and stake the seed agent population
//	POST /run        execute one request end-to-end and return the outcome
//	GET  /route      show the router shortlist for a task description
//	GET  /agents     list the agents that are currently routable
//	GET  /health
//	GET  /           operations dashboard (static UI served from web/)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/chain"
	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/coordinator"
	"github.com/neural-hive/hive-node/internal/decompose"
	"github.com/neural-hive/hive-node/internal/routing"
)

type runBody struct {
	Request     string `json:"request"`
	Replication int    `json:"replication"`
	Tier        uint8  `json:"tier"`
	ForceBandit bool   `json:"forceBandit"`
	Byzantine   bool   `json:"byzantine"`
	RelayerURL  string `json:"relayerUrl"`
}

type bootstrapBody struct {
	Count    int      `json:"count"`
	BaseURLs []string `json:"baseUrls"`
	Price    float64  `json:"priceHive"`
}

// faucetAmountWei is the HIVE balance the devnet faucet tops a wallet up to (HIVE_FAUCET_AMOUNT,
// default 100 HIVE).
func faucetAmountWei() *big.Int {
	raw := strings.TrimSpace(os.Getenv("HIVE_FAUCET_AMOUNT"))
	if raw == "" {
		raw = "100"
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok || r.Sign() <= 0 {
		r, _ = new(big.Rat).SetString("100")
	}
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
	return new(big.Int).Quo(r.Num(), r.Denom())
}

func main() {
	port := flag.Int("port", 9200, "HTTP listen port")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	adminKey := os.Getenv("COORDINATOR_PRIVATE_KEY")
	if adminKey == "" {
		adminKey = cfg.PrivateKey1
	}
	admin, err := chain.New(cfg, adminKey)
	if err != nil {
		log.Fatalf("chain client: %v", err)
	}
	seed := os.Getenv("AGENT_SEED")
	if seed == "" {
		seed = "neural-hive-devnet"
	}
	co := coordinator.NewCoordinator(cfg, admin, seed)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "admin": admin.From.Hex(), "coordinator": admin.Address("TaskCoordinator").Hex(), "answerAgent": cfg.DeepSeek.AgentURL})
	})
	mux.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		if err := co.RefreshViews(); err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		type agentView struct {
			Address    string  `json:"address"`
			Reputation float64 `json:"reputation"`
			Price      float64 `json:"priceHive"`
			Stake      float64 `json:"stakeHive"`
			Endpoint   string  `json:"endpoint"`
		}
		out := []agentView{}
		for _, v := range co.Views {
			out = append(out, agentView{Address: v.Address, Reputation: v.Reputation, Price: v.Price, Stake: v.Stake, Endpoint: v.Endpoint})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
		writeJSON(w, 200, map[string]interface{}{
			"count":          len(out),
			"routeMode":      string(co.Selfish.CurrentMode()),
			"priceOfAnarchy": co.Selfish.PriceOfAnarchy(),
			"agents":         out,
		})
	})
	mux.HandleFunc("/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		var b bootstrapBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		if len(b.BaseURLs) == 0 {
			b.BaseURLs = cfg.AgentURLs
		}
		if b.Count == 0 {
			b.Count = len(cfg.AgentURLs)
		}
		if b.Price == 0 {
			b.Price = 0.02
		}
		price := new(big.Int).SetUint64(uint64(b.Price * 1e18))
		specs := coordinator.DefaultAgentSpecs(b.BaseURLs, price)
		if b.Count > 0 && b.Count < len(specs) {
			specs = specs[:b.Count]
		}
		if cfg.DeepSeek.AgentURL != "" {
			specs = append(specs, coordinator.DeepSeekAgentSpec(cfg.DeepSeek.AgentIndex, cfg.DeepSeek.AgentURL, price))
		}
		created := 0
		for _, s := range specs {
			if _, err := co.BootstrapAgent(s); err != nil {
				writeJSON(w, 500, map[string]interface{}{"error": err.Error(), "created": created})
				return
			}
			created++
		}
		_ = co.RefreshViews()
		writeJSON(w, 200, map[string]interface{}{"ok": true, "agents": created})
	})
	mux.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		var b runBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeJSON(w, 400, map[string]interface{}{"error": err.Error()})
			return
		}
		opts := coordinator.DefaultRunOptions()
		if b.Replication > 0 {
			opts.Replication = b.Replication
		}
		opts.Tier = b.Tier
		opts.ForceBandit = b.ForceBandit
		opts.Byzantine = b.Byzantine
		opts.RelayerURL = b.RelayerURL
		if cfg.DeepSeek.AgentURL != "" {
			opts.AnswerEndpoint = cfg.DeepSeek.AgentURL
		}
		start := time.Now()
		res, err := co.RunRequest(b.Request, opts)
		if err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error(), "elapsedMs": time.Since(start).Milliseconds()})
			return
		}
		writeJSON(w, 200, res)
	})
	// POST /faucet tops a devnet wallet up with test HIVE (and gas) so a fresh MetaMask account can
	// pay for tasks. Devnet only: it refuses to run unless NETWORK=ganache.
	mux.HandleFunc("/faucet", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "POST required"})
			return
		}
		if !strings.EqualFold(cfg.Network, "ganache") {
			writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "faucet is only available on the local ganache devnet"})
			return
		}
		var b struct {
			Address string `json:"address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !common.IsHexAddress(b.Address) {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "a valid wallet address is required"})
			return
		}
		to := common.HexToAddress(b.Address)
		amount := faucetAmountWei()
		hiveBal, err := admin.HiveBalance(to)
		if err != nil {
			writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "read HIVE balance: " + err.Error()})
			return
		}
		sentHive := big.NewInt(0)
		if hiveBal.Cmp(amount) < 0 {
			sentHive = new(big.Int).Sub(amount, hiveBal)
			if err := admin.TransferHive(to, sentHive); err != nil {
				writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "send HIVE: " + err.Error()})
				return
			}
		}
		gasWant := new(big.Int).Mul(big.NewInt(1), big.NewInt(1_000_000_000_000_000_000))
		gasMin := new(big.Int).Mul(big.NewInt(5), big.NewInt(10_000_000_000_000_000))
		sentEth := big.NewInt(0)
		if ethBal, err := admin.BalanceWei(to); err == nil && ethBal.Cmp(gasMin) < 0 {
			if err := admin.TransferEth(to, gasWant); err != nil {
				writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "send gas: " + err.Error()})
				return
			}
			sentEth = gasWant
		}
		newBal, _ := admin.HiveBalance(to)
		writeJSON(w, 200, map[string]interface{}{
			"ok": true, "address": to.Hex(), "sentHiveWei": sentHive.String(), "sentGasWei": sentEth.String(),
			"hiveBalanceWei": func() string {
				if newBal == nil {
					return ""
				}
				return newBal.String()
			}(),
		})
	})
	mux.HandleFunc("/anchor", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Hash  string `json:"hash"`
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		res, err := co.Anchor(b.Hash, b.Label)
		if err != nil {
			writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, res)
	})

	mux.HandleFunc("/route", func(w http.ResponseWriter, r *http.Request) {
		task := r.URL.Query().Get("task")
		if task == "" {
			writeJSON(w, 400, map[string]interface{}{"error": "task query param required"})
			return
		}
		if err := co.RefreshViews(); err != nil {
			writeJSON(w, 500, map[string]interface{}{"error": err.Error()})
			return
		}
		g := decompose.Decompose(task)
		type stepRoute struct {
			StepID     string   `json:"stepId"`
			Kind       string   `json:"kind"`
			Critical   bool     `json:"criticalPath"`
			Candidates []string `json:"candidates"`
		}
		out := []stepRoute{}
		crit := map[string]bool{}
		for _, al := range g.AllocateFastestAgents() {
			if al.CriticalPath {
				crit[al.StepID] = true
			}
		}
		order, _ := g.TopologicalSort()
		for _, id := range order {
			st, _ := g.Step(id)
			vec := capability.FromSkills(st.Skills)
			cands := routing.RouteCandidates(co.Index, co.Views, vec, 16, 8)
			addrs := []string{}
			for _, c := range cands {
				addrs = append(addrs, c.Address)
			}
			out = append(out, stepRoute{StepID: id, Kind: st.Kind, Critical: crit[id], Candidates: addrs})
		}
		writeJSON(w, 200, map[string]interface{}{
			"request":        task,
			"mode":           string(co.Selfish.CurrentMode()),
			"priceOfAnarchy": co.Selfish.PriceOfAnarchy(),
			"steps":          out,
		})
	})

	webDir := filepath.Join(cfg.Root, "web")
	if st, err := os.Stat(webDir); err == nil && st.IsDir() {
		mux.Handle("/", http.FileServer(http.Dir(webDir)))
	}

	log.Printf("coordinator listening on port %d admin=%s web=%s", *port, admin.From.Hex(), webDir)
	if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
