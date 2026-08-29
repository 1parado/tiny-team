package main

import (
	"sync"
	"time"
)

// TraceEvent is one recorded moment in a run: a model reply, a tool call, a
// delegation between agents, the final answer. Depth marks delegation nesting
// (0 = the top-level agent), so a UI can render the run as a tree.
type TraceEvent struct {
	Time       time.Time         `json:"time"`
	Agent      string            `json:"agent"`
	Depth      int               `json:"depth"`
	Type       string            `json:"type"` // run_start, step, assistant, tool_result, delegate_start, delegate_end, final, error
	Step       int               `json:"step,omitempty"`
	Text       string            `json:"text,omitempty"` // task / thought / observation / error message
	ToolName   string            `json:"tool_name,omitempty"`
	Args       string            `json:"args,omitempty"` // JSON-encoded tool arguments
	IsErr      bool              `json:"is_error,omitempty"`
	DurationMS float64           `json:"duration_ms,omitempty"`
	ToolCalls  []ToolCallSummary `json:"tool_calls,omitempty"`
	Usage      Usage             `json:"usage"`
}

// ToolCallSummary is the UI-facing form of one requested tool call.
type ToolCallSummary struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

// Tracer records TraceEvents in chronological order. It is safe for
// concurrent use, so the web UI can snapshot while the run continues.
type Tracer struct {
	mu     sync.Mutex
	depth  int
	events []TraceEvent
	done   bool
}

// NewTracer returns an empty tracer with no open delegation frames.
func NewTracer() *Tracer { return &Tracer{} }

// Enter opens a delegation frame: events recorded until Exit are nested one
// level deeper.
func (t *Tracer) Enter() { t.mu.Lock(); t.depth++; t.mu.Unlock() }

// Exit closes a delegation frame opened by Enter.
func (t *Tracer) Exit() { t.mu.Lock(); t.depth--; t.mu.Unlock() }

// Record appends e, stamping its time and the current delegation depth.
func (t *Tracer) Record(e TraceEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e.Time = time.Now()
	e.Depth = t.depth
	t.events = append(t.events, e)
}

// SetDone marks the run as finished.
func (t *Tracer) SetDone() { t.mu.Lock(); t.done = true; t.mu.Unlock() }

// Snapshot copies all recorded events and the done flag.
func (t *Tracer) Snapshot() ([]TraceEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TraceEvent, len(t.events))
	copy(out, t.events)
	return out, t.done
}

// msSince returns the elapsed milliseconds since start, for event durations.
func msSince(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

// toolCallSummaries converts the model's requested tool calls to their
// UI-facing form.
func toolCallSummaries(calls []ToolCall) []ToolCallSummary {
	summaries := make([]ToolCallSummary, len(calls))
	for i, c := range calls {
		summaries[i] = ToolCallSummary{Name: c.Function.Name, Args: c.Function.Arguments}
	}
	return summaries
}
