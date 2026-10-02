// Command hub runs the Neural Hive Hub, the central orchestrator over the DeepSeek agent pool.
//
// Endpoints: POST /task (submit a task), GET /agents, GET /agents/{id}, GET /trace/{id},
// GET /history, GET /network, GET /health and the agentic workspace UI served from web/.
package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/hub"
)

func main() {
	port := 0
	for i, a := range os.Args {
		if a == "-port" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if port == 0 {
		port = cfg.Hub.Port
	}

	stateFile := cfg.Hub.StateFile
	if !filepath.IsAbs(stateFile) {
		stateFile = filepath.Join(cfg.Root, stateFile)
	}
	reg := hub.NewRegistry(stateFile)
	for _, a := range cfg.Hub.Roster {
		reg.Register(hub.AgentCard{
			ID: a.ID, Index: a.Index, Port: a.Port, Endpoint: a.Endpoint(), Name: a.ID,
			Tags: a.Tags, Model: a.Model, Status: "unknown",
		})
		reg.SetPrice(a.ID, priceFloat(cfg.Hub.PricePerAgentWei))

		// Seed agents are owned by the Hub key (the deployer): it is their on-chain operator.
	}
	if err := reg.Load(); err != nil {
		log.Printf("hub: no prior state (%v)", err)
	}

	h := hub.New(cfg, reg)
	h.Pay.LoadLedger(filepath.Join(filepath.Dir(stateFile), "hub-payments.jsonl"))
	// Deploy/attach the per-agent AgentWallet smart contracts so each agent has its own on-chain
	// wallet that holds its HIVE and that only the agent owner can withdraw from.
	h.EnsureAllAgentWallets(context.Background())
	// Make the Hub/deployer a registered on-chain agent so the owner can change agent prices from
	// MetaMask (CapabilityRegistry.updatePrice requires the caller to be a registered operator).
	if err := h.EnsureOwnerOnChain(context.Background()); err != nil {
		log.Printf("hub: on-chain owner registration skipped: %v", err)
	}
	h.UILog = openFrontendLog(cfg.Root)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h.StartHealthPoller(ctx, 8*time.Second)

	webDir := filepath.Join(cfg.Root, "web")
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: h.Handler(webDir)}
	log.Printf("hive payment: required=%v price=%s HIVE/agent token=%s treasury=%s chain=%d", cfg.Hub.PaymentRequired, cfg.Hub.PricePerAgent, cfg.Hub.TokenAddress, cfg.Hub.Treasury, cfg.Hub.ChainID)
	log.Printf("neural hive hub on http://127.0.0.1:%d (agents=%d, hive default=%d complex=%d max=%d, aggregator=%s/%s)",
		port, len(cfg.Hub.Roster), cfg.Hub.HIVE.Default, cfg.Hub.HIVE.Complex, cfg.Hub.HIVE.Max, h.Agg.Provider(), h.Agg.Model())
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("hub listen: %v", err)
	}
}

// openFrontendLog opens logs/frontend.log for the workspace UI access log. Failing to open the
// file only disables that separate log; the Hub keeps serving the UI.
func openFrontendLog(root string) *log.Logger {
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "frontend.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return log.New(f, "", log.LstdFlags)
}

// priceFloat converts the configured per-agent price (18-decimal base units) to a float.
func priceFloat(wei *big.Int) float64 {
	if wei == nil {
		return 0
	}
	f := new(big.Float).SetInt(wei)
	f.Quo(f, big.NewFloat(1e18))
	v, _ := f.Float64()
	return v
}
