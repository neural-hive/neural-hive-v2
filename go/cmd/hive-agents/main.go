// Command hive-agents launches the whole configured pool of reusable DeepSeek agent instances.
//
// Each roster entry becomes an independent HTTP server on its own port with its own id, identity
// index and capability tags; the pool size and tags come from configuration (HIVE_AGENT_COUNT,
// HIVE_AGENT_BASE_PORT, AGENT_<n>_TAGS or HIVE_AGENT_ROSTER). The Hub talks to them purely over
// HTTP, exactly as it would to agents launched by an external process manager.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/deepseek"
	"github.com/neural-hive/hive-node/internal/deepworker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if len(cfg.Hub.Roster) == 0 {
		log.Fatalf("no agents configured")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	roster := make([]map[string]interface{}, 0, len(cfg.Hub.Roster))
	for _, a := range cfg.Hub.Roster {
		roster = append(roster, map[string]interface{}{
			"id": a.ID, "index": a.Index, "port": a.Port, "tags": a.Tags, "model": a.Model, "endpoint": a.Endpoint(),
		})
	}
	if b, err := json.Marshal(roster); err == nil {
		log.Printf("hive-agents roster (%d): %s", len(roster), string(b))
	}
	if cfg.DeepSeek.APIKey == "" {
		log.Printf("WARNING DEEPSEEK_API_KEY is not configured; agents will report 503")
	}

	var wg sync.WaitGroup
	for _, entry := range cfg.Hub.Roster {
		a := entry
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := deepseek.NewClient(cfg.DeepSeek.APIKey, cfg.DeepSeek.BaseURL, cfg.DeepSeek.Timeout, nil)
			srv := deepworker.New(deepworker.Config{
				ID: a.ID, Index: a.Index, Port: a.Port, Tags: a.Tags,
				Model: a.Model, System: deepworker.DefaultSystemPrompt, Temperature: 0.4, Timeout: cfg.DeepSeek.Timeout,
			}, client)
			if err := srv.Start(ctx); err != nil {
				log.Printf("hive-agent %s stopped: %v", a.ID, err)
			}
		}()
	}
	wg.Wait()
}
