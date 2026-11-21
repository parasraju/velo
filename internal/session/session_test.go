package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"velo/internal/protocol"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func writeRaw(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readLines(t *testing.T, path string) []line {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []line
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("decode line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func headerLine(id, ws, created string) string {
	return fmt.Sprintf(`{"type":"session","schema":1,"id":%q,"workspace":%q,"created_at":%q}`+"\n", id, ws, created)
}

func messageLine(seq int64, ts, role, text string) string {
	return fmt.Sprintf(`{"type":"message","seq":%d,"time":%q,"message":{"role":%q,"content":[{"type":"text","text":%q}]}}`+"\n", seq, ts, role, text)
}

func stateLine(seq int64, ts, status string, turns int) string {
	return fmt.Sprintf(`{"type":"state","seq":%d,"time":%q,"state":{"status":%q,"turns":%d,"updated_at":%q}}`+"\n", seq, ts, status, turns, ts)
}

func TestCreateWritesHeader(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("alpha", "D:/work")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID() != "alpha" || sess.Workspace() != "D:/work" {
		t.Fatalf("unexpected session identity: %q %q", sess.ID(), sess.Workspace())
	}
	if sess.Store() != s {
		t.Fatal("Store() mismatch")
	}
	st := sess.State()
	if st.Status != "created" || st.Turns != 0 {
		t.Fatalf("unexpected initial state: %+v", st)
	}
	if !st.UpdatedAt.Equal(sess.CreatedAt()) {
		t.Fatalf("initial state UpdatedAt %v != CreatedAt %v", st.UpdatedAt, sess.CreatedAt())
	}
	path, err := s.Path("alpha")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	h := lines[0]
	if h.Type != "session" || h.Schema != 1 || h.ID != "alpha" || h.Workspace != "D:/work" {
		t.Fatalf("bad header: %+v", h)
	}
	if h.CreatedAt.IsZero() {
		t.Fatal("header created_at is zero")
	}
	ok, err := s.Exists("alpha")
	if err != nil || !ok {
		t.Fatalf("Exists: %v %v", ok, err)
	}
}

func TestAppendReopenPreservesOrder(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("reopen", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	msgs := []protocol.Message{
		{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("first")}},
		{Role: protocol.RoleAssistant, Content: []protocol.Content{protocol.TextContent("second")}},
		{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("third")}},
	}
	for _, m := range msgs {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	if err := sess.AppendEvent("turn.start", map[string]any{"turn": 1}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := sess.SaveState(State{Status: "running", Turns: 1, Objective: "ship"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	// Close so Open re-reads the journal. A still-open session is the same
	// writer and would not exercise the on-disk decode.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := s.Open("reopen")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := reopened.Messages()
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	for i, want := range []string{"first", "second", "third"} {
		if got[i].Text() != want {
			t.Fatalf("message %d = %q, want %q", i, got[i].Text(), want)
		}
		if got[i].Role != msgs[i].Role {
			t.Fatalf("message %d role = %q, want %q", i, got[i].Role, msgs[i].Role)
		}
	}
	events := reopened.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != "turn.start" || events[0].Data["turn"] != float64(1) {
		t.Fatalf("bad event: %+v", events[0])
	}
	if events[0].Seq <= 0 || events[0].Time.IsZero() {
		t.Fatalf("bad event seq/time: %+v", events[0])
	}
	st := reopened.State()
	if st.Status != "running" || st.Turns != 1 || st.Objective != "ship" {
		t.Fatalf("bad state: %+v", st)
	}
	if reopened.CreatedAt().IsZero() || reopened.UpdatedAt().Before(reopened.CreatedAt()) {
		t.Fatalf("bad times: created=%v updated=%v", reopened.CreatedAt(), reopened.UpdatedAt())
	}
	if !reopened.UpdatedAt().Equal(st.UpdatedAt) {
		t.Fatalf("session UpdatedAt %v != state UpdatedAt %v", reopened.UpdatedAt(), st.UpdatedAt)
	}
	path, _ := s.Path("reopen")
	if err := reopened.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("fourth")}}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	lines := readLines(t, path)
	last := lines[len(lines)-1]
	if last.Type != "message" {
		t.Fatalf("last line type = %q", last.Type)
	}
	if last.Seq != 6 {
		t.Fatalf("last seq = %d, want 6", last.Seq)
	}
	if last.Message == nil || last.Message.Text() != "fourth" {
		t.Fatalf("bad last message: %+v", last.Message)
	}
}

func TestMessagesReturnsCopy(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("copy", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	call := protocol.ToolCall{ID: "c1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)}
	msgs := []protocol.Message{
		{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("original")}},
		{Role: protocol.RoleAssistant, Content: []protocol.Content{protocol.ToolCallContent(call)}},
	}
	for _, m := range msgs {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	got := sess.Messages()
	got[0].Role = protocol.RoleAssistant
	got[0].Content[0].Text = "mutated"
	got[1].Content[0].ToolCall.Name = "changed"
	got[1].Content[0].ToolCall.Arguments[0] = 'X'
	got[0].Content = append(got[0].Content, protocol.TextContent("extra"))

	again := sess.Messages()
	if again[0].Role != protocol.RoleUser {
		t.Fatalf("role mutated: %q", again[0].Role)
	}
	if again[0].Content[0].Text != "original" {
		t.Fatalf("text mutated: %q", again[0].Content[0].Text)
	}
	if len(again[0].Content) != 1 {
		t.Fatalf("content slice mutated: %d", len(again[0].Content))
	}
	if again[1].Content[0].ToolCall.Name != "read" {
		t.Fatalf("tool name mutated: %q", again[1].Content[0].ToolCall.Name)
	}
	if string(again[1].Content[0].ToolCall.Arguments) != `{"path":"a"}` {
		t.Fatalf("tool arguments mutated: %s", again[1].Content[0].ToolCall.Arguments)
	}
}

func TestInvalidIDs(t *testing.T) {
	s := newStore(t)
	valid := []string{"a", "A1._-", "1abc", strings.Repeat("a", 128)}
	for _, id := range valid {
		if _, err := s.Path(id); err != nil {
			t.Fatalf("valid id %q rejected: %v", id, err)
		}
	}
	invalid := []string{
		"", "..", "a/b", `a\b`, ".hidden", "-abc", "a b", "a..b",
		strings.Repeat("a", 129),
		"CON", "con", "con.txt", "CON.jsonl", "PRN", "AUX", "NUL",
		"COM1", "com1.log", "COM9", "LPT1", "lpt9.txt",
	}
	for _, id := range invalid {
		if _, err := s.Path(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Path(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := s.Create(id, "ws"); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Create(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := s.Open(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Open(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := s.Exists(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Exists(%q) = %v, want ErrInvalidID", id, err)
		}
		if err := s.Delete(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Delete(%q) = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestDuplicateCreate(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("dup", "ws"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := s.Create("dup", "ws")
	if err == nil {
		t.Fatal("expected error for duplicate create")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate create error = %v, want os.ErrExist", err)
	}
}

func TestOpenMissing(t *testing.T) {
	s := newStore(t)
	if _, err := s.Open("missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Open missing = %v, want ErrSessionNotFound", err)
	}
}

func TestOpenCorrupt(t *testing.T) {
	id := "bad"
	hdr := headerLine(id, "ws", "2024-01-01T00:00:00Z")
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"not json", "garbage\n"},
		{"wrong type", `{"type":"other","schema":1,"id":"bad","workspace":"ws","created_at":"2024-01-01T00:00:00Z"}` + "\n"},
		{"bad schema", `{"type":"session","schema":2,"id":"bad","workspace":"ws","created_at":"2024-01-01T00:00:00Z"}` + "\n"},
		{"wrong id", headerLine("other", "ws", "2024-01-01T00:00:00Z")},
		{"empty workspace", `{"type":"session","schema":1,"id":"bad","workspace":"","created_at":"2024-01-01T00:00:00Z"}` + "\n"},
		{"blank first line", "\n" + hdr},
		{"duplicate seq", hdr + messageLine(1, "2024-01-01T00:00:01Z", "user", "a") + messageLine(1, "2024-01-01T00:00:02Z", "user", "b")},
		{"backward seq", hdr + messageLine(2, "2024-01-01T00:00:01Z", "user", "a") + messageLine(1, "2024-01-01T00:00:02Z", "user", "b")},
		{"invalid message", hdr + `{"type":"message","seq":1,"time":"2024-01-01T00:00:01Z","message":{"role":"tool","content":[{"type":"text","text":"a"}]}}` + "\n"},
		{"nil message", hdr + `{"type":"message","seq":1,"time":"2024-01-01T00:00:01Z"}` + "\n"},
		{"nil state", hdr + `{"type":"state","seq":1,"time":"2024-01-01T00:00:01Z"}` + "\n"},
		{"empty event kind", hdr + `{"type":"event","seq":1,"time":"2024-01-01T00:00:01Z","kind":""}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := NewStore(dir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			writeRaw(t, dir, id+".jsonl", tc.body)
			if _, err := st.Open(id); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("Open = %v, want ErrInvalidSession", err)
			}
		})
	}
}

func TestOpenIgnoresUnknownLines(t *testing.T) {
	s := newStore(t)
	body := headerLine("unknown", "ws", "2024-01-01T00:00:00Z") +
		`{"type":"future","seq":1,"time":"2024-01-01T00:00:01Z","payload":{"x":1}}` + "\n" +
		messageLine(2, "2024-01-01T00:00:02Z", "user", "hello")
	writeRaw(t, s.Root(), "unknown.jsonl", body)
	sess, err := s.Open("unknown")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := sess.Messages(); len(got) != 1 || got[0].Text() != "hello" {
		t.Fatalf("bad messages: %+v", got)
	}
}

func TestLastStateWins(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("states", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sess.SaveState(State{Status: "one", Turns: 1, Objective: "a"}); err != nil {
		t.Fatalf("SaveState 1: %v", err)
	}
	if err := sess.SaveState(State{Status: "two", Turns: 2, Objective: "b"}); err != nil {
		t.Fatalf("SaveState 2: %v", err)
	}
	st := sess.State()
	if st.Status != "two" || st.Turns != 2 || st.Objective != "b" {
		t.Fatalf("in-memory state = %+v", st)
	}
	reopened, err := s.Open("states")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st = reopened.State()
	if st.Status != "two" || st.Turns != 2 || st.Objective != "b" {
		t.Fatalf("reopened state = %+v", st)
	}
	path, _ := s.Path("states")
	lines := readLines(t, path)
	var stateLines []line
	for _, l := range lines {
		if l.Type == "state" {
			stateLines = append(stateLines, l)
		}
	}
	if len(stateLines) != 2 {
		t.Fatalf("expected 2 state lines before compact, got %d", len(stateLines))
	}
	last := stateLines[len(stateLines)-1]
	if last.State == nil || last.State.Status != "two" || last.State.Turns != 2 {
		t.Fatalf("bad last state line: %+v", last)
	}
}

func TestListSortsAndSkipsMalformed(t *testing.T) {
	s := newStore(t)
	dir := s.Root()
	writeRaw(t, dir, "a.jsonl", headerLine("a", "ws", "2024-01-01T00:00:00Z"))
	writeRaw(t, dir, "b.jsonl", headerLine("b", "ws", "2024-01-01T00:00:00Z"))
	writeRaw(t, dir, "c.jsonl", headerLine("c", "ws", "2024-01-02T00:00:00Z")+
		stateLine(1, "2024-01-02T00:00:01Z", "running", 3)+
		messageLine(2, "2024-01-02T00:00:02Z", "user", "hi"))
	writeRaw(t, dir, "broken.jsonl", "not json at all\n")
	writeRaw(t, dir, "wrong.jsonl", `{"type":"session","schema":2,"id":"wrong","workspace":"ws"}`+"\n")
	writeRaw(t, dir, "notes.txt", "ignore me\n")

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 summaries, got %d: %+v", len(list), list)
	}
	if list[0].ID != "c" || list[1].ID != "a" || list[2].ID != "b" {
		t.Fatalf("bad order: %v %v %v", list[0].ID, list[1].ID, list[2].ID)
	}
	c := list[0]
	if c.Workspace != "ws" || c.Status != "running" || c.Turns != 3 || c.Messages != 1 {
		t.Fatalf("bad summary c: %+v", c)
	}
	if !c.UpdatedAt.Equal(time.Date(2024, 1, 2, 0, 0, 2, 0, time.UTC)) {
		t.Fatalf("bad updated_at: %v", c.UpdatedAt)
	}
	if list[1].Status != "created" || list[1].Messages != 0 {
		t.Fatalf("bad summary a: %+v", list[1])
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("gone", "ws"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete("gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, err := s.Exists("gone")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if ok {
		t.Fatal("session still exists after delete")
	}
	if err := s.Delete("gone"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second Delete = %v, want ErrSessionNotFound", err)
	}
}

func TestCompact(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("compact", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i, text := range []string{"m1", "m2", "m3"} {
		if err := sess.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent(text)}}); err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
	}
	for _, kind := range []string{"e1", "e2"} {
		if err := sess.AppendEvent(kind, map[string]any{"kind": kind}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	for i, status := range []string{"s1", "s2", "s3"} {
		if err := sess.SaveState(State{Status: status, Turns: i}); err != nil {
			t.Fatalf("SaveState %d: %v", i, err)
		}
	}
	path, _ := s.Path("compact")
	before := readLines(t, path)
	var beforeMsg, beforeEvent []int64
	var lastStateSeq int64
	var maxSeq int64
	for _, l := range before {
		if l.Seq > maxSeq {
			maxSeq = l.Seq
		}
		switch l.Type {
		case "message":
			beforeMsg = append(beforeMsg, l.Seq)
		case "event":
			beforeEvent = append(beforeEvent, l.Seq)
		case "state":
			lastStateSeq = l.Seq
		}
	}
	if err := sess.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := readLines(t, path)
	if len(after) != 7 {
		t.Fatalf("expected 7 lines after compact, got %d", len(after))
	}
	if after[0].Type != "session" || after[0].ID != "compact" || after[0].Workspace != "ws" {
		t.Fatalf("bad header after compact: %+v", after[0])
	}
	var afterMsg, afterEvent []int64
	var afterStates []line
	for _, l := range after[1:] {
		switch l.Type {
		case "message":
			afterMsg = append(afterMsg, l.Seq)
		case "event":
			afterEvent = append(afterEvent, l.Seq)
		case "state":
			afterStates = append(afterStates, l)
		}
	}
	if !reflect.DeepEqual(beforeMsg, afterMsg) {
		t.Fatalf("message seqs changed: %v -> %v", beforeMsg, afterMsg)
	}
	if !reflect.DeepEqual(beforeEvent, afterEvent) {
		t.Fatalf("event seqs changed: %v -> %v", beforeEvent, afterEvent)
	}
	if len(afterStates) != 1 {
		t.Fatalf("expected 1 state line, got %d", len(afterStates))
	}
	latest := afterStates[0]
	if latest.Seq != lastStateSeq || latest.State == nil || latest.State.Status != "s3" || latest.State.Turns != 2 {
		t.Fatalf("bad latest state after compact: %+v", latest)
	}
	reopened, err := s.Open("compact")
	if err != nil {
		t.Fatalf("Open after compact: %v", err)
	}
	msgs := reopened.Messages()
	if len(msgs) != 3 {
		t.Fatalf("messages after reopen = %d", len(msgs))
	}
	for i, want := range []string{"m1", "m2", "m3"} {
		if msgs[i].Text() != want {
			t.Fatalf("message %d = %q, want %q", i, msgs[i].Text(), want)
		}
	}
	if evs := reopened.Events(); len(evs) != 2 || evs[0].Kind != "e1" || evs[1].Kind != "e2" {
		t.Fatalf("bad events after reopen: %+v", evs)
	}
	if st := reopened.State(); st.Status != "s3" || st.Turns != 2 {
		t.Fatalf("bad state after reopen: %+v", st)
	}
	if err := reopened.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("m4")}}); err != nil {
		t.Fatalf("append after compact: %v", err)
	}
	lines := readLines(t, path)
	last := lines[len(lines)-1]
	if last.Seq != maxSeq+1 {
		t.Fatalf("append after compact seq = %d, want %d", last.Seq, maxSeq+1)
	}
}

func TestConcurrentAppends(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("concurrent", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const goroutines = 20
	const perGoroutine = 10
	errs := make(chan error, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				text := fmt.Sprintf("%d-%d", g, i)
				m := protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent(text)}}
				if err := sess.AppendMessage(m); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent AppendMessage: %v", err)
	}
	if got := sess.Messages(); len(got) != goroutines*perGoroutine {
		t.Fatalf("messages = %d, want %d", len(got), goroutines*perGoroutine)
	}
	path, _ := s.Path("concurrent")
	lines := readLines(t, path)
	var seqs []int64
	for _, l := range lines {
		if l.Type == "message" {
			seqs = append(seqs, l.Seq)
		}
	}
	if len(seqs) != goroutines*perGoroutine {
		t.Fatalf("message lines = %d, want %d", len(seqs), goroutines*perGoroutine)
	}
	seen := make(map[int64]bool, len(seqs))
	for i, seq := range seqs {
		if seq <= 0 {
			t.Fatalf("message %d has bad seq %d", i, seq)
		}
		if seen[seq] {
			t.Fatalf("duplicate seq %d", seq)
		}
		seen[seq] = true
		if i > 0 && seq <= seqs[i-1] {
			t.Fatalf("seq not increasing at %d: %d after %d", i, seq, seqs[i-1])
		}
	}
	reopened, err := s.Open("concurrent")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := reopened.Messages(); len(got) != goroutines*perGoroutine {
		t.Fatalf("reopened messages = %d", len(got))
	}
	if err := reopened.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("after")}}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	lines = readLines(t, path)
	last := lines[len(lines)-1]
	if last.Seq != int64(goroutines*perGoroutine)+1 {
		t.Fatalf("next seq = %d, want %d", last.Seq, goroutines*perGoroutine+1)
	}
}

func TestSaveStateValidation(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("stateval", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, st := range []State{{Status: "", Turns: 0}, {Status: "   ", Turns: 0}, {Status: "ok", Turns: -1}} {
		if err := sess.SaveState(st); err == nil {
			t.Fatalf("SaveState(%+v) succeeded, want error", st)
		}
	}
	if got := sess.State(); got.Status != "created" || got.Turns != 0 {
		t.Fatalf("state changed after failed saves: %+v", got)
	}
	if err := sess.SaveState(State{Status: "ok", Turns: 0, Objective: "obj"}); err != nil {
		t.Fatalf("SaveState valid: %v", err)
	}
	if got := sess.State(); got.Status != "ok" || got.Objective != "obj" || got.UpdatedAt.IsZero() {
		t.Fatalf("bad saved state: %+v", got)
	}
}

func TestAppendEventValidation(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("eventval", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, kind := range []string{"", "   "} {
		if err := sess.AppendEvent(kind, nil); err == nil {
			t.Fatalf("AppendEvent(%q) succeeded, want error", kind)
		}
	}
	if got := sess.Events(); len(got) != 0 {
		t.Fatalf("events written after failed appends: %+v", got)
	}
	if err := sess.AppendEvent("  start  ", map[string]any{"x": 1}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	evs := sess.Events()
	if len(evs) != 1 || evs[0].Kind != "start" || evs[0].Data["x"] != 1 {
		t.Fatalf("bad event: %+v", evs)
	}
}

func TestAppendMessageValidation(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("msgval", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sess.AppendMessage(protocol.Message{Role: "bogus"}); err == nil {
		t.Fatal("invalid role accepted")
	}
	if got := sess.Messages(); len(got) != 0 {
		t.Fatalf("messages written after failed append: %+v", got)
	}
}

func TestNewIDFormatAndUniqueness(t *testing.T) {
	pattern := regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{16}$`)
	seen := make(map[string]struct{}, 512)
	for i := 0; i < 512; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("NewID format = %q", id)
		}
		if len(id) > 128 {
			t.Fatalf("NewID too long: %d", len(id))
		}
		if err := validateID(id); err != nil {
			t.Fatalf("NewID %q fails validateID: %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate NewID %q", id)
		}
		seen[id] = struct{}{}
	}
	s := newStore(t)
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := s.Create(id, "ws"); err != nil {
		t.Fatalf("Create with NewID: %v", err)
	}
	if _, err := s.Open(id); err != nil {
		t.Fatalf("Open with NewID: %v", err)
	}
}

func TestOpenIgnoresPartialTrailingRecord(t *testing.T) {
	id := "partial"
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	body := headerLine(id, "ws", "2024-01-01T00:00:00Z") +
		messageLine(1, "2024-01-01T00:00:01Z", "user", "kept") +
		`{"type":"message","seq":2,"time":"2024-01-01T00:00:02Z","message":{"role":"user","cont`
	writeRaw(t, dir, id+".jsonl", body)
	sess, err := st.Open(id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	msgs := sess.Messages()
	if len(msgs) != 1 || msgs[0].Text() != "kept" {
		t.Fatalf("messages = %+v", msgs)
	}
	p, err := parseReader(strings.NewReader(body), id)
	if err != nil {
		t.Fatalf("parseReader: %v", err)
	}
	if len(p.messages) != 1 || p.lastSeq != 1 {
		t.Fatalf("parsed partial state: messages=%d lastSeq=%d", len(p.messages), p.lastSeq)
	}
}

func TestOpenRejectsMalformedCompleteLine(t *testing.T) {
	id := "complete"
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	body := headerLine(id, "ws", "2024-01-01T00:00:00Z") +
		messageLine(1, "2024-01-01T00:00:01Z", "user", "ok") +
		`{"type":"message","seq":2,` + "\n"
	writeRaw(t, dir, id+".jsonl", body)
	if _, err := st.Open(id); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Open = %v, want ErrInvalidSession", err)
	}
}

func TestOpenAppliesUnterminatedFinalState(t *testing.T) {
	id := "finalstate"
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	body := headerLine(id, "ws", "2024-01-01T00:00:00Z") +
		messageLine(1, "2024-01-01T00:00:01Z", "user", "hello") +
		strings.TrimSuffix(stateLine(2, "2024-01-01T00:00:02Z", "paused", 4), "\n")
	writeRaw(t, dir, id+".jsonl", body)
	sess, err := st.Open(id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if msgs := sess.Messages(); len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	got := sess.State()
	if got.Status != "paused" || got.Turns != 4 {
		t.Fatalf("state = %+v", got)
	}
}

func TestOpenRejectsLonePartialHeader(t *testing.T) {
	id := "lone"
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	writeRaw(t, dir, id+".jsonl", `{"type":"session","schema":1,`)
	if _, err := st.Open(id); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Open = %v, want ErrInvalidSession", err)
	}
}

func TestListHandlesPartialTrailingRecord(t *testing.T) {
	s := newStore(t)
	dir := s.Root()
	writeRaw(t, dir, "good.jsonl", headerLine("good", "ws", "2024-01-01T00:00:00Z")+
		messageLine(1, "2024-01-01T00:00:01Z", "user", "hi")+
		`{"type":"message","seq":2,"time":"2024-01-01T00:00:02Z","mess`)
	writeRaw(t, dir, "head.jsonl", headerLine("head", "ws", "2024-01-01T00:00:00Z")+
		`{"type":"state","seq":1,"time":"2024-01-01T00:00:01Z","state":{"status":"run`)
	writeRaw(t, dir, "bad.jsonl", headerLine("bad", "ws", "2024-01-01T00:00:00Z")+
		`{"type":"message"} garbage`+"\n")
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := make(map[string]Summary, len(list))
	for _, sum := range list {
		byID[sum.ID] = sum
	}
	if _, ok := byID["bad"]; ok {
		t.Fatalf("malformed complete line was listed: %+v", byID["bad"])
	}
	good, ok := byID["good"]
	if !ok {
		t.Fatalf("good session missing: %+v", list)
	}
	if good.Messages != 1 || good.Workspace != "ws" {
		t.Fatalf("bad summary: %+v", good)
	}
	head, ok := byID["head"]
	if !ok {
		t.Fatalf("header-only session with partial record missing: %+v", list)
	}
	if head.Messages != 0 || head.Status != "created" {
		t.Fatalf("bad header-only summary: %+v", head)
	}
}

type failAfterReader struct {
	data []byte
	err  error
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func TestParseReaderPropagatesReadError(t *testing.T) {
	boom := errors.New("boom")
	r := &failAfterReader{data: []byte(headerLine("readerr", "ws", "2024-01-01T00:00:00Z")), err: boom}
	_, err := parseReader(r, "")
	if err == nil {
		t.Fatal("expected read error")
	}
	if errors.Is(err, ErrInvalidSession) {
		t.Fatalf("read error reported as ErrInvalidSession: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("read error lost: %v", err)
	}
}

func TestConcurrentAppendStateCompact(t *testing.T) {
	s := newStore(t)
	sess, err := s.Create("mixed", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const goroutines = 8
	const perGoroutine = 10
	errs := make(chan error, goroutines*perGoroutine*4+8)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				text := fmt.Sprintf("%d-%d", g, i)
				m := protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent(text)}}
				if err := sess.AppendMessage(m); err != nil {
					errs <- err
				}
				if i%3 == 0 {
					if err := sess.SaveState(State{Status: "running", Turns: i}); err != nil {
						errs <- err
					}
				}
				if i%5 == 0 {
					if err := sess.AppendEvent("tick", map[string]any{"g": g}); err != nil {
						errs <- err
					}
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if err := sess.Compact(); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent operation: %v", err)
	}
	if got := len(sess.Messages()); got != goroutines*perGoroutine {
		t.Fatalf("messages = %d, want %d", got, goroutines*perGoroutine)
	}
	reopened, err := s.Open("mixed")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := len(reopened.Messages()); got != goroutines*perGoroutine {
		t.Fatalf("reopened messages = %d", got)
	}
	path, err := s.Path("mixed")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	var maxSeq int64
	for _, l := range readLines(t, path) {
		if l.Seq > maxSeq {
			maxSeq = l.Seq
		}
	}
	if err := reopened.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("after")}}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	lines := readLines(t, path)
	last := lines[len(lines)-1]
	if last.Seq != maxSeq+1 {
		t.Fatalf("next seq = %d, want %d", last.Seq, maxSeq+1)
	}
}

func TestIndependentOpensShareSequence(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	created, err := s1.Create("indep", "ws")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	opened, err := s2.Open("indep")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if created != opened {
		t.Fatal("independent Open returned a second session")
	}

	const goroutines = 20
	const perGoroutine = 10
	errs := make(chan error, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		handle := created
		if g%2 == 1 {
			handle = opened
		}
		go func(g int, handle *Session) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				text := fmt.Sprintf("%d-%d", g, i)
				m := protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent(text)}}
				if err := handle.AppendMessage(m); err != nil {
					errs <- err
				}
			}
		}(g, handle)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append: %v", err)
	}

	path, err := s1.Path("indep")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	lines := readLines(t, path)
	var seqs []int64
	for _, l := range lines {
		if l.Type == "message" {
			seqs = append(seqs, l.Seq)
		}
	}
	if len(seqs) != goroutines*perGoroutine {
		t.Fatalf("message lines = %d, want %d", len(seqs), goroutines*perGoroutine)
	}
	seen := map[int64]bool{}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, want %d", i, seq, i+1)
		}
		if seen[seq] {
			t.Fatalf("duplicate seq %d", seq)
		}
		seen[seq] = true
	}

	if err := created.Close(); err != nil {
		t.Fatalf("Close create handle: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("Close open handle: %v", err)
	}
	if err := created.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("after close")}}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("append after close = %v, want ErrSessionClosed", err)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file still present: %v", err)
	}

	reopened, err := s2.Open("indep")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if got := len(reopened.Messages()); got != goroutines*perGoroutine {
		t.Fatalf("reopened messages = %d, want %d", got, goroutines*perGoroutine)
	}
	if err := reopened.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("tail")}}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	lines = readLines(t, path)
	last := lines[len(lines)-1]
	if last.Seq != int64(goroutines*perGoroutine)+1 {
		t.Fatalf("tail seq = %d, want %d", last.Seq, goroutines*perGoroutine+1)
	}
}

func TestSessionLockRejectsLiveOwner(t *testing.T) {
	parent := os.Getppid()
	if parent <= 0 || parent == os.Getpid() || !pidAlive(parent) {
		t.Skip("parent process is not a live foreign owner")
	}
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id := "held"
	writeRaw(t, dir, id+".jsonl", headerLine(id, "ws", "2024-01-01T00:00:00Z"))
	writeRaw(t, dir, id+".jsonl.lock", fmt.Sprintf("%d\n", parent))
	_, err = st.Open(id)
	if !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("Open = %v, want ErrSessionLocked", err)
	}
	if err := st.Delete(id); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("Delete = %v, want ErrSessionLocked", err)
	}
}

func TestSessionLockStealsDeadOwner(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id := "stale"
	writeRaw(t, dir, id+".jsonl", headerLine(id, "ws", "2024-01-01T00:00:00Z")+
		messageLine(1, "2024-01-01T00:00:01Z", "user", "kept"))
	writeRaw(t, dir, id+".jsonl.lock", "2147483647\n")
	sess, err := st.Open(id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()
	if msgs := sess.Messages(); len(msgs) != 1 || msgs[0].Text() != "kept" {
		t.Fatalf("messages = %+v", msgs)
	}
	if err := sess.AppendMessage(protocol.Message{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("next")}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	path, _ := st.Path(id)
	lines := readLines(t, path)
	if lines[len(lines)-1].Seq != 2 {
		t.Fatalf("seq = %d, want 2", lines[len(lines)-1].Seq)
	}
}
