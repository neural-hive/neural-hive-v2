// Package deepworker is the reusable, capability-tagged DeepSeek agent instance of Neural Hive.
//
// One DeepSeek worker is one independent HTTP server on its own port with its own stable
// identity (agent id plus deterministic key index) and its own capability tags. It holds no
// orchestration logic: it receives a subtask, calls the DeepSeek API through the shared
// internal/llm wrapper and returns the generated text with full provenance (model, provider,
// request id, timings). The Hub launches many of these from configuration, so the pool size and
// the capability tags are data, never code, and every instance shares exactly this implementation.
//
// The worker never fabricates an answer. A missing key, a transport error, an HTTP status or an
// empty completion is returned to the caller as an error and travels to the user interface.
package deepworker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/neural-hive/hive-node/internal/llm"
)

// DefaultSystemPrompt is the system prompt every Neural Hive DeepSeek worker runs with unless
// overridden, so the whole pool reasons with the same house style.
const DefaultSystemPrompt = "You are a specialist worker inside Neural Hive, a verified multi-agent " +
	"network. You take part in a continuing conversation: the turns that precede the current " +
	"subtask are the real earlier messages of this conversation and are the source of truth " +
	"about the user (for example their name, preferences and any facts they stated). When the user " +
	"asks something already established earlier, such as their name, answer from that earlier " +
	"context instead of saying you were never told. You receive one focused subtask and answer " +
	"it directly, accurately and concisely. Prefer short paragraphs and lists, stay on the " +
	"assigned subtask, and say plainly when you are unsure instead of inventing facts."

// TaskRequest is one subtask dispatched by the Hub to an agent instance.
type ChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type TaskRequest struct {
	SubTaskID string     `json:"subTaskId"`
	TaskID    string     `json:"taskId"`
	Prompt    string     `json:"prompt"`
	Require   []string   `json:"requireTags,omitempty"`
	History   []ChatTurn `json:"history,omitempty"`
}

// TaskResponse is an agent answer plus the provenance the Hub records in the execution trace.
type TaskResponse struct {
	AgentID    string   `json:"agentId"`
	AgentIndex int      `json:"agentIndex"`
	Port       int      `json:"port"`
	Tags       []string `json:"tags"`
	SubTaskID  string   `json:"subTaskId"`
	Answer     string   `json:"answer"`
	Model      string   `json:"model"`
	Provider   string   `json:"provider"`
	RequestID  string   `json:"requestId"`
	StartedAt  string   `json:"startedAt"`
	FinishedAt string   `json:"finishedAt"`
	LatencyMs  int64    `json:"latencyMs"`
}

// Config parameterises one worker instance.
type Config struct {
	ID          string
	Index       int
	Port        int
	Tags        []string
	Model       string
	System      string
	Temperature float64
	Timeout     time.Duration
}

// keyed is implemented by providers that can report whether they are configured; the DeepSeek
// client exposes HasKey without ever revealing the key.
type keyed interface {
	HasKey() bool
}

// Server is one independent DeepSeek agent instance.
type Server struct {
	cfg     Config
	prov    llm.Provider
	wrapper *llm.Wrapper
	mu      sync.Mutex
	served  uint64
	failed  uint64
	latency int64
	http    *http.Server
}

// New builds a worker over an llm provider, which is the DeepSeek client in production.
func New(cfg Config, provider llm.Provider) *Server {
	s := &Server{cfg: cfg, prov: provider}
	s.wrapper = llm.NewWrapper(llm.Config{
		Provider:    provider,
		Model:       cfg.Model,
		System:      cfg.System,
		Temperature: cfg.Temperature,
	})
	return s
}

// ID returns the agent identifier.
func (s *Server) ID() string { return s.cfg.ID }

// Index returns the deterministic identity index.
func (s *Server) Index() int { return s.cfg.Index }

// Port returns the listen port.
func (s *Server) Port() int { return s.cfg.Port }

// Tags returns a copy of the capability tags.
func (s *Server) Tags() []string { return append([]string{}, s.cfg.Tags...) }

// Model returns the configured model.
func (s *Server) Model() string { return s.wrapper.Model() }

// Provider returns the provider name.
func (s *Server) Provider() string { return s.wrapper.Provider() }

// Endpoint returns the loopback URL of the instance.
func (s *Server) Endpoint() string { return fmt.Sprintf("http://127.0.0.1:%d", s.cfg.Port) }

// Configured reports whether the provider holds credentials, without revealing them.
func (s *Server) Configured() bool {
	if k, ok := s.prov.(keyed); ok {
		return k.HasKey()
	}
	return s.prov != nil
}

// Answer runs one generation and stamps the provenance. A provider error is returned unchanged so
// the Hub can surface the real reason; nothing is fabricated.
func (s *Server) Answer(ctx context.Context, prompt string, history []ChatTurn) (*TaskResponse, error) {
	start := time.Now()
	res, err := s.answerWithHistory(ctx, prompt, history)
	finish := time.Now()
	s.mu.Lock()
	s.latency += finish.Sub(start).Milliseconds()
	if err != nil {
		s.failed++
	} else {
		s.served++
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &TaskResponse{
		AgentID:    s.cfg.ID,
		AgentIndex: s.cfg.Index,
		Port:       s.cfg.Port,
		Tags:       s.Tags(),
		Answer:     res.Text,
		Model:      res.Model,
		Provider:   res.Provider,
		RequestID:  res.RequestID,
		StartedAt:  start.UTC().Format(time.RFC3339Nano),
		FinishedAt: finish.UTC().Format(time.RFC3339Nano),
		LatencyMs:  finish.Sub(start).Milliseconds(),
	}, nil
}

// answerWithHistory builds a multi-turn conversation from the forwarded chat history (bounded
// by the Hub) followed by the current subtask prompt, so an agent answers with the user context.
func (s *Server) answerWithHistory(ctx context.Context, prompt string, history []ChatTurn) (*llm.Result, error) {
	if len(history) == 0 {
		return s.wrapper.Ask(ctx, prompt)
	}
	msgs := make([]llm.Message, 0, len(history)+1)
	for _, h := range history {
		role := h.Role
		if role != llm.RoleUser && role != llm.RoleAssistant && role != llm.RoleSystem {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, llm.Message{Role: role, Content: h.Content})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: prompt})
	return s.wrapper.Chat(ctx, msgs)
}

// Stats is the observable counter set of one worker.
func (s *Server) Stats() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	avg := int64(0)
	if total := s.served + s.failed; total > 0 {
		avg = s.latency / int64(total)
	}
	return map[string]interface{}{
		"agentId":      s.cfg.ID,
		"served":       s.served,
		"failed":       s.failed,
		"avgLatencyMs": avg,
	}
}

// Handler returns the worker HTTP surface: /health, /info, /stats and /task.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ok := s.Configured()
		status := http.StatusOK
		body := map[string]interface{}{
			"ok": ok, "agentId": s.cfg.ID, "index": s.cfg.Index, "port": s.cfg.Port,
			"tags": s.cfg.Tags, "provider": s.Provider(), "model": s.Model(), "endpoint": s.Endpoint(),
		}
		if !ok {
			status = http.StatusServiceUnavailable
			body["error"] = "provider is not configured"
		}
		writeJSON(w, status, body)
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agentId": s.cfg.ID, "index": s.cfg.Index, "port": s.cfg.Port, "endpoint": s.Endpoint(),
			"tags": s.cfg.Tags, "provider": s.Provider(), "model": s.Model(),
		})
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Stats())
	})
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		var req TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
			return
		}
		log.Printf("hive-agent %s port=%d RECEIVED subtask=%s task=%s tags=%v requireTags=%v historyTurns=%d", s.cfg.ID, s.cfg.Port, req.SubTaskID, req.TaskID, s.cfg.Tags, req.Require, len(req.History))
		timeout := s.cfg.Timeout
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		resp, err := s.Answer(ctx, req.Prompt, req.History)
		if err != nil {
			log.Printf("hive-agent %s port=%d subtask=%s task=%s FAILED: %v", s.cfg.ID, s.cfg.Port, req.SubTaskID, req.TaskID, err)
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
			return
		}
		resp.SubTaskID = req.SubTaskID
		log.Printf("hive-agent %s port=%d subtask=%s task=%s OK model=%s provider=%s requestId=%s latencyMs=%d answerChars=%d", s.cfg.ID, s.cfg.Port, req.SubTaskID, req.TaskID, resp.Model, resp.Provider, resp.RequestID, resp.LatencyMs, len(resp.Answer))
		writeJSON(w, http.StatusOK, resp)
	})
	return mux
}

// Start listens on the configured port and serves until the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.cfg.Port))
	if err != nil {
		return err
	}
	s.http = &http.Server{Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sh)
	}()
	log.Printf("hive-agent %s listening on %s tags=%v model=%s provider=%s", s.cfg.ID, s.Endpoint(), s.cfg.Tags, s.Model(), s.Provider())
	return s.http.Serve(ln)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
