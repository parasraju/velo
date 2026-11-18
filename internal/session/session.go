package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"velo/internal/protocol"
)

var (
	ErrSessionNotFound = errors.New("session: not found")
	ErrInvalidSession  = errors.New("session: invalid")
	ErrInvalidID       = errors.New("session: invalid id")
)

type Event struct {
	Seq  int64          `json:"seq"`
	Time time.Time      `json:"time"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data,omitempty"`
}

type State struct {
	Status    string    `json:"status"`
	Turns     int       `json:"turns"`
	Objective string    `json:"objective,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Summary struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	Status    string    `json:"status"`
	Turns     int       `json:"turns"`
	Messages  int       `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type line struct {
	Type      string            `json:"type"`
	Schema    int               `json:"schema,omitempty"`
	ID        string            `json:"id,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Seq       int64             `json:"seq,omitempty"`
	Time      time.Time         `json:"time,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Data      map[string]any    `json:"data,omitempty"`
	Message   *protocol.Message `json:"message,omitempty"`
	State     *State            `json:"state,omitempty"`
}

type messageRecord struct {
	seq  int64
	time time.Time
	msg  protocol.Message
}

type Session struct {
	mu        sync.Mutex
	store     *Store
	id        string
	workspace string
	createdAt time.Time
	updatedAt time.Time
	messages  []messageRecord
	events    []Event
	state     State
	stateSet  bool
	stateSeq  int64
	stateTime time.Time
	lastSeq   int64
}

func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *Session) Store() *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store
}

func (s *Session) Workspace() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace
}

func (s *Session) CreatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createdAt
}

func (s *Session) UpdatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updatedAt
}

func (s *Session) Messages() []protocol.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return nil
	}
	out := make([]protocol.Message, len(s.messages))
	for i, rec := range s.messages {
		out[i] = cloneMessage(rec.msg)
	}
	return out
}

func (s *Session) AppendMessage(m protocol.Message) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("session: append message: %w", err)
	}
	cp := cloneMessage(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.store.Path(s.id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	seq := s.lastSeq + 1
	l := line{Type: "message", Seq: seq, Time: now, Message: &cp}
	if err := appendLineFile(path, l); err != nil {
		return fmt.Errorf("session: append message: %w", err)
	}
	s.messages = append(s.messages, messageRecord{seq: seq, time: now, msg: cp})
	s.lastSeq = seq
	s.updatedAt = now
	return nil
}

func (s *Session) AppendEvent(kind string, data map[string]any) error {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return errors.New("session: event kind is required")
	}
	d := cloneData(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.store.Path(s.id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	seq := s.lastSeq + 1
	l := line{Type: "event", Seq: seq, Time: now, Kind: kind, Data: d}
	if err := appendLineFile(path, l); err != nil {
		return fmt.Errorf("session: append event: %w", err)
	}
	s.events = append(s.events, Event{Seq: seq, Time: now, Kind: kind, Data: d})
	s.lastSeq = seq
	s.updatedAt = now
	return nil
}

func (s *Session) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil
	}
	out := make([]Event, len(s.events))
	for i, e := range s.events {
		out[i] = cloneEvent(e)
	}
	return out
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) SaveState(st State) error {
	if strings.TrimSpace(st.Status) == "" {
		return errors.New("session: state status is required")
	}
	if st.Turns < 0 {
		return errors.New("session: state turns must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.store.Path(s.id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	st.UpdatedAt = now
	seq := s.lastSeq + 1
	l := line{Type: "state", Seq: seq, Time: now, State: &st}
	if err := appendLineFile(path, l); err != nil {
		return fmt.Errorf("session: save state: %w", err)
	}
	s.state = st
	s.stateSet = true
	s.stateSeq = seq
	s.stateTime = now
	s.lastSeq = seq
	s.updatedAt = now
	return nil
}

func (s *Session) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.store.Path(s.id)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	write := func(l line) error {
		b, err := marshalLine(l)
		if err != nil {
			return err
		}
		_, err = buf.Write(b)
		return err
	}
	if err := write(line{Type: "session", Schema: 1, ID: s.id, Workspace: s.workspace, CreatedAt: s.createdAt}); err != nil {
		return fmt.Errorf("session: compact: %w", err)
	}
	for i := range s.messages {
		rec := s.messages[i]
		if err := write(line{Type: "message", Seq: rec.seq, Time: rec.time, Message: &rec.msg}); err != nil {
			return fmt.Errorf("session: compact: %w", err)
		}
	}
	if s.stateSet {
		st := s.state
		if err := write(line{Type: "state", Seq: s.stateSeq, Time: s.stateTime, State: &st}); err != nil {
			return fmt.Errorf("session: compact: %w", err)
		}
	}
	events := make([]Event, len(s.events))
	copy(events, s.events)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	for _, e := range events {
		if err := write(line{Type: "event", Seq: e.Seq, Time: e.Time, Kind: e.Kind, Data: e.Data}); err != nil {
			return fmt.Errorf("session: compact: %w", err)
		}
	}
	tmp, err := os.CreateTemp(s.store.root, ".compact-*.tmp")
	if err != nil {
		return fmt.Errorf("session: compact: %w", err)
	}
	tmpName := tmp.Name()
	remove := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		remove()
		return fmt.Errorf("session: compact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		remove()
		return fmt.Errorf("session: compact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("session: compact: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("session: compact: %w", err)
	}
	return nil
}

func marshalLine(l line) ([]byte, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func appendLineFile(path string, l line) error {
	b, err := marshalLine(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func cloneMessage(m protocol.Message) protocol.Message {
	c := m
	if m.Content != nil {
		c.Content = make([]protocol.Content, len(m.Content))
		for i, ct := range m.Content {
			c.Content[i] = ct
			if ct.ToolCall != nil {
				tc := *ct.ToolCall
				if tc.Arguments != nil {
					tc.Arguments = append(json.RawMessage(nil), tc.Arguments...)
				}
				c.Content[i].ToolCall = &tc
			}
			if ct.ToolResult != nil {
				tr := *ct.ToolResult
				c.Content[i].ToolResult = &tr
			}
		}
	}
	if m.ProviderMetadata != nil {
		c.ProviderMetadata = append(json.RawMessage(nil), m.ProviderMetadata...)
	}
	return c
}

func cloneEvent(e Event) Event {
	c := e
	c.Data = cloneData(e.Data)
	return c
}

func cloneData(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		out[k] = v
	}
	return out
}
