// Package events defines the live run-event stream the agent publishes for
// user interfaces. It is deliberately dependency-light: producers (the agent
// loop, the approval layer) emit small immutable values; consumers (the TUI,
// embedders) translate them into views.
//
// The stream complements, never replaces, the session journal written by
// session.AppendEvent. Journal events are persistence records for resume and
// audit; stream events are ephemeral UI notifications and may be coalesced
// or capped at the producer's discretion.
package events

import (
	"sync"
	"time"

	"velo/internal/protocol"
)

// Kind enumerates the event types published on the stream.
type Kind string

const (
	// RunStarted carries the objective and session id for a fresh run.
	RunStarted Kind = "run_started"
	// TurnStarted carries the 1-based turn number.
	TurnStarted Kind = "turn_started"
	// TextDelta carries streamed or whole-message assistant text.
	TextDelta Kind = "text_delta"
	// ToolStarted carries the Tool descriptor with arguments preview.
	ToolStarted Kind = "tool_started"
	// ToolFinished carries the completed Tool descriptor.
	ToolFinished Kind = "tool_finished"
	// ApprovalRequested carries the Tool descriptor awaiting a decision.
	ApprovalRequested Kind = "approval_requested"
	// ApprovalResolved carries the outcome of an approval decision.
	ApprovalResolved Kind = "approval_resolved"
	// ContextCompacted reports history trimming for the next request.
	ContextCompacted Kind = "context_compacted"
	// UsageUpdated carries cumulative token usage.
	UsageUpdated Kind = "usage_updated"
	// StateChanged carries an agent state-machine transition.
	StateChanged Kind = "state_changed"
	// Error reports a failure that ended or degraded the run.
	Error Kind = "error"
	// RunFinished is the terminal event of every run.
	RunFinished Kind = "run_finished"
)

// Maximum preview sizes. Producers must cap strings before emitting so the
// stream stays cheap to consume.
const (
	MaxArgumentPreview = 2048
	MaxOutputPreview   = 4096
	MaxTextEvent       = 8192
	MaxErrorPreview    = 512
)

// Tool describes a single tool invocation in flight or completed.
type Tool struct {
	ID          string
	Name        string
	Category    string
	Arguments   string // capped JSON preview, see MaxArgumentPreview
	Status      string // running | succeeded | failed | requested | approved | denied
	DurationMS  int64
	Output      string // capped result preview, see MaxOutputPreview
	Error       string
	Destructive bool
}

// ContextStats mirrors the compaction statistics from contextmgr.Stats.
type ContextStats struct {
	Compacted      bool
	Trimmed        bool
	OverBudget     bool
	MessagesBefore int
	MessagesAfter  int
	Dropped        int
	BytesBefore    int
	BytesAfter     int
	LimitTokens    int
}

// StateChange is a from/to pair of agent states.
type StateChange struct {
	From string
	To   string
}

// RunSummary describes how a run ended. Mirrors agent.Result without the
// import cycle.
type RunSummary struct {
	Status             string
	Content            string
	Turns              int
	ToolCalls          int
	Usage              *protocol.Usage
	Verification       string
	VerificationDetail string
	Error              string
	// Truncated reports a length-limit completion. Status stays "completed".
	Truncated bool
}

// Event is one immutable notification on the run stream. Exactly the fields
// relevant to Kind are populated.
type Event struct {
	Kind      Kind
	Time      time.Time
	SessionID string

	Turn  int    // TurnStarted, TextDelta, UsageUpdated
	Delta string // TextDelta

	Tool    *Tool         // ToolStarted, ToolFinished, ApprovalRequested, ApprovalResolved
	Context *ContextStats // ContextCompacted
	State   *StateChange  // StateChanged
	Usage   *protocol.Usage
	Message string // Error

	Run       *RunSummary // RunFinished
	Objective string      // RunStarted
}

// Emitter receives events. Implementations must not block for long and must
// be safe for concurrent use.
type Emitter interface {
	Emit(Event)
}

// Cap truncates s to at most n bytes on a rune boundary, appending an
// ellipsis marker when it had to cut.
func Cap(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !isRuneStart(s[len(cut)]) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Recorder captures events for tests.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *Recorder) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Snapshot returns a copy of every event recorded so far.
func (r *Recorder) Snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Kinds returns the ordered list of event kinds recorded so far.
func (r *Recorder) Kinds() []Kind {
	snaps := r.Snapshot()
	out := make([]Kind, len(snaps))
	for i, e := range snaps {
		out[i] = e.Kind
	}
	return out
}
