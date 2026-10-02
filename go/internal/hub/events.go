package hub

import (
	"sync"
	"time"
)

// LiveEvent is one event of the live execution activity feed shown in the workspace: the same
// milestones the trace records (payment -> routing -> agent reply -> aggregation -> final
// response), addressed to the run that produced them so the browser can stream them live.
type LiveEvent struct {
	Seq       int64                  `json:"seq"`
	TaskID    string                 `json:"taskId"`
	Kind      string                 `json:"kind"`
	Label     string                 `json:"label"`
	Detail    string                 `json:"detail"`
	AgentID   string                 `json:"agentId,omitempty"`
	AgentName string                 `json:"agentName,omitempty"`
	Value     string                 `json:"value,omitempty"`
	At        string                 `json:"at"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// eventBus is a per-task live event buffer. Subscribers receive every event appended after they
// subscribed, and the full buffer is kept so the Execution Details page can replay a run.
type eventBus struct {
	mu     sync.Mutex
	seq    int64
	all    []LiveEvent
	subs   map[int64]chan LiveEvent
	nextID int64
	max    int
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[int64]chan LiveEvent{}, max: 2000}
}

func (b *eventBus) Emit(taskID, kind, label, detail, agentID, agentName, value string, data map[string]interface{}) LiveEvent {
	b.mu.Lock()
	b.seq++
	ev := LiveEvent{Seq: b.seq, TaskID: taskID, Kind: kind, Label: label, Detail: detail, AgentID: agentID, AgentName: agentName, Value: value, At: time.Now().UTC().Format(time.RFC3339Nano), Data: data}
	b.all = append(b.all, ev)
	if len(b.all) > b.max {
		b.all = b.all[len(b.all)-b.max:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	b.mu.Unlock()
	return ev
}

func (b *eventBus) Since(seq int64) []LiveEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []LiveEvent{}
	for _, ev := range b.all {
		if ev.Seq > seq {
			out = append(out, ev)
		}
	}
	return out
}

func (b *eventBus) Snapshot() []LiveEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]LiveEvent, len(b.all))
	copy(out, b.all)
	return out
}

func (b *eventBus) Subscribe() (int64, chan LiveEvent, func()) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	ch := make(chan LiveEvent, 256)
	b.subs[id] = ch
	b.mu.Unlock()
	return id, ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}
