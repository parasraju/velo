package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	root string
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session: store directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("session: resolve store directory: %w", err)
	}
	info, err := os.Stat(abs)
	switch {
	case err == nil:
		if !info.IsDir() {
			return nil, fmt.Errorf("session: store path %q is not a directory", abs)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return nil, fmt.Errorf("session: create store directory: %w", err)
		}
	default:
		return nil, fmt.Errorf("session: stat store directory: %w", err)
	}
	return &Store{root: abs}, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) Path(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, id+".jsonl"), nil
}

func (s *Store) Create(id, workspace string) (*Session, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("session: create %q: workspace is required", id)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("session: create %q: %w", id, err)
	}
	path := filepath.Join(s.root, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("session: create %q: %w", id, err)
		}
		return nil, fmt.Errorf("session: create %q: %w", id, err)
	}
	now := time.Now().UTC()
	b, err := marshalLine(line{Type: "session", Schema: 1, ID: id, Workspace: workspace, CreatedAt: now})
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("session: create %q: %w", id, err)
	}
	state := State{Status: "created", Turns: 0, UpdatedAt: now}
	return &Session{
		store:     s,
		id:        id,
		workspace: workspace,
		createdAt: now,
		updatedAt: now,
		state:     state,
	}, nil
}

func (s *Store) Open(id string) (*Session, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("session: open %q: %w", id, err)
	}
	p, perr := parseReader(f, id)
	if cerr := f.Close(); perr == nil {
		perr = cerr
	}
	if perr != nil {
		if errors.Is(perr, ErrInvalidSession) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("session: open %q: %w", id, perr)
	}
	sess := &Session{
		store:     s,
		id:        id,
		workspace: p.workspace,
		createdAt: p.createdAt,
		updatedAt: p.updatedAt,
		messages:  p.messages,
		events:    p.events,
		lastSeq:   p.lastSeq,
	}
	if p.stateSet {
		sess.state = p.state
		sess.stateSet = true
		sess.stateSeq = p.stateSeq
		sess.stateTime = p.stateTime
	} else {
		sess.state = State{Status: "created", Turns: 0, UpdatedAt: p.updatedAt}
	}
	return sess, nil
}

func (s *Store) Exists(id string) (bool, error) {
	path, err := s.Path(id)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("session: exists %q: %w", id, err)
	}
	return true, nil
}

func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("session: list: %w", err)
	}
	out := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(s.root, name))
		if err != nil {
			continue
		}
		p, perr := parseReader(f, "")
		f.Close()
		if perr != nil {
			continue
		}
		sum := Summary{
			ID:        p.id,
			Workspace: p.workspace,
			Status:    "created",
			Messages:  len(p.messages),
			CreatedAt: p.createdAt,
			UpdatedAt: p.updatedAt,
		}
		if p.stateSet {
			sum.Status = p.state.Status
			sum.Turns = p.state.Turns
		}
		out = append(out, sum)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > 200 {
		out = out[:200]
	}
	return out, nil
}

func (s *Store) Delete(id string) error {
	path, err := s.Path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("session: delete %q: %w", id, err)
	}
	return nil
}

type parsedSession struct {
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

const maxRecordSize = 32 * 1024 * 1024

func readRecord(br *bufio.Reader) ([]byte, bool, bool, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > maxRecordSize {
			return nil, false, false, fmt.Errorf("session: read: record exceeds %d bytes", maxRecordSize)
		}
		if buf == nil {
			buf = append([]byte(nil), chunk...)
		} else {
			buf = append(buf, chunk...)
		}
		switch err {
		case nil:
			return buf[:len(buf)-1], true, true, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(buf) == 0 {
				return nil, false, false, nil
			}
			return buf, false, true, nil
		default:
			return nil, false, false, err
		}
	}
}

func parseReader(r io.Reader, expectedID string) (*parsedSession, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	p := &parsedSession{}
	headerSeen := false
	var lastMessageSeq int64
	for {
		raw, terminated, ok, err := readRecord(br)
		if err != nil {
			return nil, fmt.Errorf("session: read: %w", err)
		}
		if !ok {
			break
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			if !headerSeen {
				return nil, ErrInvalidSession
			}
			if !terminated {
				break
			}
			continue
		}
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			if !terminated {
				break
			}
			return nil, ErrInvalidSession
		}
		if !headerSeen {
			if l.Type != "session" || l.Schema != 1 {
				return nil, ErrInvalidSession
			}
			if l.Workspace == "" {
				return nil, ErrInvalidSession
			}
			if validateID(l.ID) != nil {
				return nil, ErrInvalidSession
			}
			if expectedID != "" && l.ID != expectedID {
				return nil, ErrInvalidSession
			}
			p.id = l.ID
			p.workspace = l.Workspace
			p.createdAt = l.CreatedAt
			p.updatedAt = l.CreatedAt
			headerSeen = true
			continue
		}
		if !l.Time.IsZero() && l.Time.After(p.updatedAt) {
			p.updatedAt = l.Time
		}
		if l.Seq > p.lastSeq {
			p.lastSeq = l.Seq
		}
		switch l.Type {
		case "message":
			if l.Message == nil {
				return nil, ErrInvalidSession
			}
			if l.Seq <= lastMessageSeq {
				return nil, ErrInvalidSession
			}
			if err := l.Message.Validate(); err != nil {
				return nil, ErrInvalidSession
			}
			lastMessageSeq = l.Seq
			p.messages = append(p.messages, messageRecord{seq: l.Seq, time: l.Time, msg: cloneMessage(*l.Message)})
		case "event":
			if strings.TrimSpace(l.Kind) == "" {
				return nil, ErrInvalidSession
			}
			p.events = append(p.events, Event{Seq: l.Seq, Time: l.Time, Kind: l.Kind, Data: cloneData(l.Data)})
		case "state":
			if l.State == nil {
				return nil, ErrInvalidSession
			}
			st := *l.State
			if !l.Time.IsZero() {
				st.UpdatedAt = l.Time
			}
			p.state = st
			p.stateSet = true
			p.stateSeq = l.Seq
			p.stateTime = l.Time
		}
	}
	if !headerSeen {
		return nil, ErrInvalidSession
	}
	return p, nil
}

var reservedNames = map[string]struct{}{
	"CON":  {},
	"PRN":  {},
	"AUX":  {},
	"NUL":  {},
	"COM1": {},
	"COM2": {},
	"COM3": {},
	"COM4": {},
	"COM5": {},
	"COM6": {},
	"COM7": {},
	"COM8": {},
	"COM9": {},
	"LPT1": {},
	"LPT2": {},
	"LPT3": {},
	"LPT4": {},
	"LPT5": {},
	"LPT6": {},
	"LPT7": {},
	"LPT8": {},
	"LPT9": {},
}

func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session: new id: %w", err)
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
	if err := validateID(id); err != nil {
		return "", fmt.Errorf("session: new id: %w", err)
	}
	return id, nil
}

func validateID(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return ErrInvalidID
	}
	if !isAlphaNumeric(id[0]) {
		return ErrInvalidID
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if isAlphaNumeric(c) || c == '.' || c == '_' || c == '-' {
			continue
		}
		return ErrInvalidID
	}
	if strings.Contains(id, "..") {
		return ErrInvalidID
	}
	base := id
	if i := strings.IndexByte(id, '.'); i >= 0 {
		base = id[:i]
	}
	if _, ok := reservedNames[strings.ToUpper(base)]; ok {
		return ErrInvalidID
	}
	return nil
}

func isAlphaNumeric(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	default:
		return false
	}
}
