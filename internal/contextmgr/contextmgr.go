// Package contextmgr provides deterministic context selection and budgeting.
//
// The estimator is intentionally conservative: it approximates tokens from the
// serialized message size instead of counting characters. It is a stopgap
// until provider usage metadata can calibrate the estimate.
package contextmgr

import (
	"encoding/json"

	"velo/internal/protocol"
)

const (
	// bytesPerToken is the conservative fallback ratio used when no provider
	// tokenizer is available.
	bytesPerToken = 4

	// messageOverhead approximates per-message framing (roles, separators,
	// tool ids) that is not part of the text payload.
	messageOverhead = 16
)

// Budget describes the input token allowance for one model request.
type Budget struct {
	// MaxInputTokens is the provider-facing input window. A non-positive value
	// disables trimming.
	MaxInputTokens int
	// ReserveOutputTokens is reserved for the model's response.
	ReserveOutputTokens int
	// SafetyMargin is a fraction of the usable window kept free to absorb
	// estimation error. Values outside [0, 1) are clamped.
	SafetyMargin float64
}

// Stats describes the effect of one Trim or Compile call.
type Stats struct {
	// Trimmed reports that history is currently elided, either now or from an
	// earlier compaction.
	Trimmed bool `json:"trimmed"`
	// Compacted reports that this call moved the retained-history boundary.
	// Compaction is infrequent by design so request prefixes stay stable.
	Compacted      bool `json:"compacted"`
	MessagesBefore int  `json:"messages_before"`
	MessagesAfter  int  `json:"messages_after"`
	Dropped        int  `json:"dropped"`
	BytesBefore    int  `json:"bytes_before"`
	BytesAfter     int  `json:"bytes_after"`
	EstimatedLimit int  `json:"estimated_limit_tokens"`
	// OverBudget reports that the retained minimum context still exceeds the
	// byte ceiling (for example, one very large recent message).
	OverBudget bool `json:"over_budget"`
}

// EstimateTokens returns a conservative token estimate for messages.
func EstimateTokens(messages []protocol.Message) int {
	return bytesToTokens(messagesBytes(messages))
}

// EstimateStringTokens returns a conservative token estimate for plain text.
func EstimateStringTokens(s string) int {
	return bytesToTokens(len(s))
}

// LimitBytes converts a budget into a byte ceiling, or 0 when trimming is
// disabled.
func (b Budget) LimitBytes() int {
	if b.MaxInputTokens <= 0 {
		return 0
	}
	tokens := b.MaxInputTokens
	if b.ReserveOutputTokens > 0 {
		tokens -= b.ReserveOutputTokens
	}
	if tokens <= 0 {
		return 0
	}
	margin := b.SafetyMargin
	if margin < 0 {
		margin = 0
	}
	if margin >= 1 {
		margin = 0.9
	}
	tokens = int(float64(tokens) * (1 - margin))
	if tokens <= 0 {
		return 0
	}
	return tokens * bytesPerToken
}

// Trim keeps the original objective, the most recent messages, and every
// assistant tool call together with its tool results. It is stateless: it
// trims whenever the history exceeds the budget. Callers that need stable
// request prefixes across turns should use Compactor instead.
//
// Trim never returns a slice that starts with a tool message, and never drops
// the first user message (the objective) unless it is the only message left.
func Trim(messages []protocol.Message, budget Budget) ([]protocol.Message, Stats) {
	compactor := NewCompactor(budget, 1, 1)
	return compactor.Compile(messages)
}

// Compactor reduces a growing conversation while keeping request prefixes
// stable between turns.
//
// Dropping one message per turn changes the prefix of every request once the
// budget is reached, which defeats provider-side prefix caching. Compaction
// instead runs rarely: it triggers only when retained history crosses a high
// watermark and then drops down to a low watermark in a single step. Between
// compactions the request is a stable prefix plus newly appended messages.
type Compactor struct {
	budget     Budget
	limitBytes int
	highBytes  int
	lowBytes   int
	// tailStart is the absolute message index of the first retained message
	// after the objective. It only moves during a compaction.
	tailStart int
}

// NewCompactor returns a compactor for one conversation. low and high are
// fractions of the byte ceiling: history is compacted when it exceeds high and
// is reduced to low in the same step. Values that do not satisfy
// 0 < low <= high <= 1 are replaced with defaults.
func NewCompactor(budget Budget, low, high float64) *Compactor {
	c := &Compactor{budget: budget, limitBytes: budget.LimitBytes()}
	if c.limitBytes <= 0 {
		return c
	}
	if !(high > 0 && high <= 1) {
		high = 0.8
	}
	if !(low > 0) || low > high {
		low = high / 2
	}
	c.highBytes = int(float64(c.limitBytes) * high)
	c.lowBytes = int(float64(c.limitBytes) * low)
	if c.highBytes <= 0 {
		c.highBytes = c.limitBytes
	}
	if c.lowBytes <= 0 {
		c.lowBytes = c.highBytes / 2
	}
	return c
}

// Reset forgets compaction state, for example when an agent starts a new run.
func (c *Compactor) Reset() {
	if c == nil {
		return
	}
	c.tailStart = 0
}

// Compile returns the messages to send for this turn.
func (c *Compactor) Compile(messages []protocol.Message) ([]protocol.Message, Stats) {
	stats := Stats{
		MessagesBefore: len(messages),
		BytesBefore:    messagesBytes(messages),
	}
	if c == nil {
		stats.MessagesAfter = len(messages)
		stats.BytesAfter = stats.BytesBefore
		return messages, stats
	}
	stats.EstimatedLimit = c.budget.MaxInputTokens
	if c.limitBytes <= 0 || len(messages) == 0 {
		stats.MessagesAfter = len(messages)
		stats.BytesAfter = stats.BytesBefore
		return messages, stats
	}

	objective := firstUserIndex(messages)
	if objective < 0 {
		objective = firstRetainableIndex(messages)
	}
	if c.tailStart <= objective {
		c.tailStart = objective + 1
	}
	if c.tailStart > len(messages) {
		c.tailStart = len(messages)
	}

	retained := retainHistory(messages, objective, c.tailStart)
	if messagesBytes(retained) > c.highBytes {
		if next := selectTail(messages, objective, c.lowBytes); next > c.tailStart {
			c.tailStart = next
			stats.Compacted = true
		}
		retained = retainHistory(messages, objective, c.tailStart)
	}

	stats.MessagesAfter = len(retained)
	stats.BytesAfter = messagesBytes(retained)
	stats.Dropped = stats.MessagesBefore - stats.MessagesAfter
	stats.Trimmed = stats.Dropped > 0
	stats.OverBudget = stats.BytesAfter > c.limitBytes
	return retained, stats
}

// retainHistory returns the objective followed by the tail starting at start.
// When nothing has been dropped it returns messages unchanged so an
// uncompacted conversation keeps a byte-identical prefix.
func retainHistory(messages []protocol.Message, objective, start int) []protocol.Message {
	if objective == 0 && start == 1 {
		return messages
	}
	if start < objective+1 {
		start = objective + 1
	}
	out := make([]protocol.Message, 0, 1+len(messages)-start)
	out = append(out, messages[objective])
	if start < len(messages) {
		out = append(out, messages[start:]...)
	}
	return out
}

// selectTail walks backwards from the newest message and returns the earliest
// safe boundary whose objective-plus-tail fits targetBytes. It never starts
// the tail on a tool message, which would separate a tool result from its
// assistant call.
func selectTail(messages []protocol.Message, objective, targetBytes int) int {
	cut := len(messages)
	sum := messageBytes(messages[objective])
	for i := len(messages) - 1; i > objective; i-- {
		sum += messageBytes(messages[i])
		if sum > targetBytes {
			break
		}
		if safeBoundary(messages[i].Role) {
			cut = i
		}
	}
	if cut < objective+1 {
		cut = objective + 1
	}
	return cut
}

func firstUserIndex(messages []protocol.Message) int {
	for i, m := range messages {
		if m.Role == protocol.RoleUser {
			return i
		}
	}
	return -1
}

// firstRetainableIndex returns the first index whose message can safely start
// a request. Tool results must never lead a request.
func firstRetainableIndex(messages []protocol.Message) int {
	for i, m := range messages {
		if m.Role != protocol.RoleTool {
			return i
		}
	}
	return 0
}

func safeBoundary(role protocol.Role) bool {
	return role == protocol.RoleUser || role == protocol.RoleAssistant
}

func messagesBytes(messages []protocol.Message) int {
	total := 0
	for _, m := range messages {
		total += messageBytes(m)
	}
	return total
}

func messageBytes(m protocol.Message) int {
	size := messageOverhead + len(m.Text()) + len(m.ToolCallID) + len(m.Name)
	for _, call := range m.ToolCalls() {
		size += len(call.ID) + len(call.Name) + len(call.Arguments)
	}
	if len(m.ProviderMetadata) > 0 {
		size += len(m.ProviderMetadata)
	}
	if b, err := json.Marshal(m); err == nil && len(b) > size {
		// Some provider-specific content may not be reachable through the
		// helpers above; the serialized size is the safe upper bound.
		size = len(b)
	}
	return size
}

func bytesToTokens(size int) int {
	if size <= 0 {
		return 0
	}
	return (size + bytesPerToken - 1) / bytesPerToken
}
