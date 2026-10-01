// Package hub is the central Neural Hive orchestrator. It sits between the existing backend and a
// pool of independent, capability-tagged DeepSeek agents: it classifies the task, decides the
// HIVE (required agent count), decomposes a complex task into subtasks, routes each subtask to a
// suitable agent by capability tags, reputation, risk, health and history, runs the independent
// subtasks concurrently, aggregates the answers and records a complete execution trace.
package hub

import "time"

// Task is one user request as it enters the Hub.
type Task struct {
	ID         string    `json:"taskId"`
	Text       string    `json:"task"`
	HIVE       int       `json:"hive"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// Complexity is the Hub classification of a task, with the signals that produced it.
type Complexity struct {
	Level   string   `json:"level"`
	Score   int      `json:"score"`
	Signals []string `json:"signals"`
	Domains []string `json:"domains"`
}

// Subtask is one decomposed unit of work together with the capability tags it requires.
type Subtask struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Selection records why one agent was chosen for one subtask.
type Selection struct {
	SubtaskID    string   `json:"subtaskId"`
	RequiredTags []string `json:"requiredTags"`
	AgentID      string   `json:"agentId"`
	MatchingTags []string `json:"matchingTags"`
	Reputation   float64  `json:"reputation"`
	Risk         float64  `json:"risk"`
	Score        float64  `json:"score"`
	Online       bool     `json:"online"`
	Reason       string   `json:"reason"`
}

// AgentResult is the outcome of one agent executing one subtask, plus the provenance the UI shows.
type AgentResult struct {
	SubtaskID        string   `json:"subtaskId"`
	AgentID          string   `json:"agentId"`
	Port             int      `json:"port"`
	Tags             []string `json:"tags"`
	Endpoint         string   `json:"endpoint"`
	Model            string   `json:"model"`
	Provider         string   `json:"provider"`
	RequestID        string   `json:"requestId"`
	Answer           string   `json:"answer"`
	StartedAt        string   `json:"startedAt"`
	FinishedAt       string   `json:"finishedAt"`
	LatencyMs        int64    `json:"latencyMs"`
	ReputationBefore float64  `json:"reputationBefore"`
	RiskBefore       float64  `json:"riskBefore"`
	ReputationAfter  float64  `json:"reputationAfter"`
	RiskAfter        float64  `json:"riskAfter"`
	Status           string   `json:"status"`
	Error            string   `json:"error,omitempty"`
	Hash             string   `json:"hash"`
}

// TraceEvent is one step of the execution trace.
type TraceEvent struct {
	At     string                 `json:"at"`
	Stage  string                 `json:"stage"`
	Detail string                 `json:"detail"`
	Data   map[string]interface{} `json:"data,omitempty"`
}

// Aggregation records the synthesis step that turns many agent answers into the final response.
type Aggregation struct {
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Inputs    int    `json:"inputs"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// Anchor is the on-chain evidence for the final answer, produced by the existing backend.
type Anchor struct {
	OK          bool   `json:"ok"`
	Hash        string `json:"hash"`
	RequestID   uint64 `json:"requestId"`
	RequestTx   string `json:"requestTx"`
	SettleTx    string `json:"settleTx"`
	Network     string `json:"network"`
	ChainID     int64  `json:"chainId"`
	Coordinator string `json:"coordinator"`
	Error       string `json:"error,omitempty"`
}

// Payment is the HIVE payment that funded one task: what it cost, who paid, and the on-chain
// transaction the Hub verified before running any agent.
type Payment struct {
	// Mode is "onchain" (verified MetaMask payment), "selftest" (start.ps1 verification, no
	// payment) or "none" (payment not required by configuration; cost is informational).
	Mode             string `json:"mode"`
	Verified         bool   `json:"verified"`
	Agents           int    `json:"agents"`
	PricePerAgent    string `json:"pricePerAgent"`
	PricePerAgentWei string `json:"pricePerAgentWei"`
	CostHive         string `json:"costHive"`
	CostWei          string `json:"costWei"`
	PaidHive         string `json:"paidHive"`
	PaidWei          string `json:"paidWei"`
	Symbol           string `json:"symbol"`
	Payer            string `json:"payer,omitempty"`
	Treasury         string `json:"treasury,omitempty"`
	Token            string `json:"token,omitempty"`
	TxHash           string `json:"txHash,omitempty"`
	BlockNumber      uint64 `json:"blockNumber,omitempty"`
	ChainID          int64  `json:"chainId,omitempty"`
	QuoteID          string `json:"quoteId,omitempty"`
}

// Result is the complete Hub output for one task.
type Result struct {
	TaskID       string        `json:"taskId"`
	Task         string        `json:"task"`
	Complexity   Complexity    `json:"complexity"`
	HIVE         int           `json:"hive"`
	Subtasks     []Subtask     `json:"subtasks"`
	Selections   []Selection   `json:"selections"`
	AgentResults []AgentResult `json:"agentResults"`
	Answer       string        `json:"answer"`
	AnswerHash   string        `json:"answerHash"`
	Aggregation  Aggregation   `json:"aggregation"`
	Anchor       Anchor        `json:"anchor"`
	Payment      *Payment      `json:"payment,omitempty"`
	Trace        []TraceEvent  `json:"trace"`
	StartedAt    string        `json:"startedAt"`
	FinishedAt   string        `json:"finishedAt"`
	ElapsedMs    int64         `json:"elapsedMs"`
	AgentsUsed   []string      `json:"agentsUsed"`
	Errors       []string      `json:"errors"`
}

// AgentCard is the registry view of one agent instance.
type AgentCard struct {
	ID             string   `json:"id"`
	Index          int      `json:"index"`
	Port           int      `json:"port"`
	Endpoint       string   `json:"endpoint"`
	Tags           []string `json:"tags"`
	Model          string   `json:"model"`
	Provider       string   `json:"provider"`
	Status         string   `json:"status"`
	Reputation     float64  `json:"reputation"`
	Risk           float64  `json:"risk"`
	Success        int      `json:"success"`
	Failure        int      `json:"failure"`
	Timeouts       int      `json:"timeouts"`
	Executions     int      `json:"executions"`
	TotalLatencyMs int64    `json:"totalLatencyMs"`
	AvgLatencyMs   int64    `json:"avgLatencyMs"`
	LastSeen       string   `json:"lastSeen"`
	LastError      string   `json:"lastError"`
}
