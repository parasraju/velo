package protocol

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type StreamBuilder struct {
	text           strings.Builder
	calls          map[int]*ToolCallDelta
	usage          *Usage
	finish         FinishReason
	finishExplicit bool
	done           bool
	failed         bool
	err            error
}

func NewStreamBuilder() *StreamBuilder {
	return &StreamBuilder{calls: make(map[int]*ToolCallDelta)}
}

func (b *StreamBuilder) Apply(ev StreamEvent) error {
	if b.done {
		return NewProviderError(ErrProtocol, "", "stream builder: event received after done")
	}
	if err := ev.Validate(); err != nil {
		if ev.Type == StreamError {
			b.failed = true
			if ev.Error != nil {
				b.err = ev.Error
			} else {
				b.err = NewProviderError(ErrProtocol, "", "stream builder: error event without error")
			}
		}
		return err
	}
	switch ev.Type {
	case StreamTextDelta:
		b.text.WriteString(ev.Text)
	case StreamToolCallDelta:
		d := ev.ToolCall
		cur, ok := b.calls[d.Index]
		if !ok {
			cur = &ToolCallDelta{Index: d.Index}
			b.calls[d.Index] = cur
		}
		if d.ID != "" {
			cur.ID = d.ID
		}
		if d.Name != "" {
			cur.Name = d.Name
		}
		cur.ArgumentsDelta += d.ArgumentsDelta
	case StreamUsage:
		if ev.Usage != nil {
			u := *ev.Usage
			b.usage = &u
		}
	case StreamDone:
		if ev.FinishReason != "" {
			b.finish = ev.FinishReason
			b.finishExplicit = true
		} else {
			b.finish = FinishStop
		}
		b.done = true
	case StreamError:
		b.failed = true
		if ev.Error != nil {
			b.err = ev.Error
		} else {
			b.err = NewProviderError(ErrProtocol, "", "stream builder: error event without error")
		}
		return b.err
	}
	return nil
}

func (b *StreamBuilder) Response() (*Response, error) {
	if b.failed {
		if b.err != nil {
			return nil, b.err
		}
		return nil, NewProviderError(ErrProtocol, "", "stream builder: failed without error")
	}

	msg := Message{Role: RoleAssistant}
	if b.text.Len() > 0 {
		msg.Content = append(msg.Content, TextContent(b.text.String()))
	}

	indexes := make([]int, 0, len(b.calls))
	for idx := range b.calls {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	for _, idx := range indexes {
		d := b.calls[idx]
		args := json.RawMessage(d.ArgumentsDelta)
		if len(strings.TrimSpace(string(args))) == 0 {
			args = json.RawMessage("{}")
		}
		if !json.Valid(args) {
			return nil, fmt.Errorf("stream builder: tool call %d has invalid arguments json", idx)
		}
		msg.Content = append(msg.Content, ToolCallContent(ToolCall{
			ID:        d.ID,
			Name:      d.Name,
			Arguments: args,
		}))
	}

	finish := b.finish
	if !b.finishExplicit && len(indexes) > 0 {
		finish = FinishToolCalls
	}
	if finish == "" {
		if len(indexes) > 0 {
			finish = FinishToolCalls
		} else {
			finish = FinishStop
		}
	}

	resp := &Response{
		Message:      msg,
		FinishReason: finish,
	}
	if b.usage != nil {
		u := *b.usage
		u.Normalize()
		if err := u.Validate(); err != nil {
			return nil, fmt.Errorf("stream builder: %w", err)
		}
		resp.Usage = &u
	}
	if err := resp.Validate(); err != nil {
		return nil, err
	}
	return resp, nil
}
