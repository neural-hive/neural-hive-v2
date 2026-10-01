// Command hive-agent runs ONE reusable DeepSeek agent instance as an independent HTTP service.
// It is the single-instance form of the Neural Hive DeepSeek worker: a unique id, its own port
// and its own capability tags. The Hub resolves the roster from configuration and launches
// however many instances are needed.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/deepseek"
	"github.com/neural-hive/hive-node/internal/deepworker"
)

func main() {
	id := flag.String("id", "agent-01", "agent id")
	index := flag.Int("index", 100, "deterministic identity index")
	port := flag.Int("port", 9401, "HTTP listen port")
	tags := flag.String("tags", "research,analysis", "comma separated capability tags")
	model := flag.String("model", "", "model override (defaults to DEEPSEEK_MODEL)")
	system := flag.String("system", "", "system prompt override")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *model == "" {
		*model = cfg.DeepSeek.Model
	}
	if *system == "" {
		*system = deepworker.DefaultSystemPrompt
	}
	client := deepseek.NewClient(cfg.DeepSeek.APIKey, cfg.DeepSeek.BaseURL, cfg.DeepSeek.Timeout, nil)
	if !client.HasKey() {
		log.Printf("WARNING DEEPSEEK_API_KEY is not configured: %v", deepseek.ErrNoAPIKey)
	}
	srv := deepworker.New(deepworker.Config{
		ID: *id, Index: *index, Port: *port, Tags: splitCSV(*tags),
		Model: *model, System: *system, Temperature: 0.4, Timeout: cfg.DeepSeek.Timeout,
	}, client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("hive-agent %s tags=%v model=%s provider=%s key=%v", srv.ID(), srv.Tags(), srv.Model(), srv.Provider(), srv.Configured())
	if err := srv.Start(ctx); err != nil {
		time.Sleep(200 * time.Millisecond)
		log.Fatalf("listen: %v", err)
	}
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
