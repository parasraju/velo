package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"velo/internal/security"
)

func readDefinition(name string) Definition {
	def := validDefinition(name)
	def.Category = CategoryRead
	def.ReadOnly = true
	return def
}

func writeDefinition(name string) Definition {
	def := validDefinition(name)
	def.Category = CategoryWrite
	def.ReadOnly = false
	return def
}

func newTestDispatcher(t *testing.T, opts DispatcherOptions) (*Dispatcher, *Registry) {
	t.Helper()
	if opts.Registry == nil {
		opts.Registry = NewRegistry()
	}
	d, err := NewDispatcher(opts)
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	return d, opts.Registry
}

func TestNewDispatcherValidation(t *testing.T) {
	if _, err := NewDispatcher(DispatcherOptions{}); err == nil {
		t.Fatal("expected error for nil registry")
	}
	d, err := NewDispatcher(DispatcherOptions{Registry: NewRegistry()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.defaultTimeout != defaultDispatchTimeout {
		t.Fatalf("unexpected default timeout: %v", d.defaultTimeout)
	}
	if d.maxResultBytes != defaultMaxResultBytes {
		t.Fatalf("unexpected default max result bytes: %d", d.maxResultBytes)
	}
	d, err = NewDispatcher(DispatcherOptions{Registry: NewRegistry(), DefaultTimeout: -1, MaxResultBytes: -1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.defaultTimeout != defaultDispatchTimeout || d.maxResultBytes != defaultMaxResultBytes {
		t.Fatal("non-positive options were not defaulted")
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	d, _ := newTestDispatcher(t, DispatcherOptions{})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if res.CallID != "1" {
		t.Fatalf("unexpected call id %q", res.CallID)
	}
}

func TestDispatchRequiresIDAndName(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{def: readDefinition("read_file")})
	for _, call := range []Call{
		{Name: "read_file"},
		{ID: "1"},
		{ID: " ", Name: "read_file"},
		{ID: "1", Name: " "},
	} {
		_, err := d.Dispatch(context.Background(), call)
		if !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %+v, got %v", call, err)
		}
	}
}

func TestDispatchArgumentValidation(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{def: readDefinition("read_file")})
	cases := []json.RawMessage{
		json.RawMessage(`{`),
		json.RawMessage(`[]`),
		json.RawMessage(`"text"`),
		json.RawMessage(`123`),
		json.RawMessage(`true`),
		json.RawMessage(`null`),
		json.RawMessage(`{"a":1} {"b":2}`),
	}
	for _, args := range cases {
		_, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "read_file", Arguments: args})
		if !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("expected ErrInvalidArguments for %q, got %v", string(args), err)
		}
	}
}

func TestDispatchNormalizesArguments(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	var seen []string
	mustRegister(t, reg, &fakeTool{
		def: readDefinition("read_file"),
		exec: func(ctx context.Context, call Call) (Result, error) {
			seen = append(seen, string(call.Arguments))
			return Result{Content: "ok"}, nil
		},
	})
	if _, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "read_file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), Call{ID: "2", Name: "read_file", Arguments: json.RawMessage("   ")}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), Call{ID: "3", Name: "read_file", Arguments: json.RawMessage("{ \"path\" : \"a\" }")}); err != nil {
		t.Fatal(err)
	}
	want := []string{`{}`, `{}`, `{"path":"a"}`}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("expected %v, got %v", want, seen)
	}
}

func TestDispatchForwardsUnknownFieldsToStrictTool(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{
		def: readDefinition("strict"),
		exec: func(ctx context.Context, call Call) (Result, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(call.Arguments, &args); err != nil {
				return Result{}, err
			}
			return Result{Content: args.Path}, nil
		},
	})
	_, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "strict", Arguments: json.RawMessage(`{"unknown":1}`)})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments, got %v", err)
	}
}

func TestDispatchApproverAllow(t *testing.T) {
	called := false
	d, reg := newTestDispatcher(t, DispatcherOptions{
		Approver: func(ctx context.Context, def Definition, call Call) error {
			called = true
			if def.Name != "write_file" || call.ID != "1" {
				t.Errorf("unexpected approval inputs: %+v %+v", def, call)
			}
			return nil
		},
	})
	mustRegister(t, reg, &fakeTool{
		def:  writeDefinition("write_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { return Result{Content: "written"}, nil },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "write_file"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("approver was not called")
	}
	if res.Content != "written" || res.IsError {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestDispatchApproverDeny(t *testing.T) {
	deny := errors.New("user denied")
	d, reg := newTestDispatcher(t, DispatcherOptions{
		Approver: func(ctx context.Context, def Definition, call Call) error { return deny },
	})
	executed := false
	mustRegister(t, reg, &fakeTool{
		def:  writeDefinition("write_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { executed = true; return Result{}, nil },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "write_file"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if !errors.Is(err, deny) {
		t.Fatalf("expected original approver error to be preserved, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if executed {
		t.Fatal("denied tool was executed")
	}
}

func TestDispatchApproverNotCalledForReadTools(t *testing.T) {
	called := false
	d, reg := newTestDispatcher(t, DispatcherOptions{
		Approver: func(ctx context.Context, def Definition, call Call) error {
			called = true
			return nil
		},
	})
	mustRegister(t, reg, &fakeTool{def: readDefinition("read_file")})
	if _, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "read_file"}); err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if called {
		t.Fatal("approver must not be called for read-only tools")
	}
}

func TestDispatchCapabilityDeniedBeforeApproval(t *testing.T) {
	dir := t.TempDir()
	policy, err := security.New(dir)
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	approved := false
	d, reg := newTestDispatcher(t, DispatcherOptions{
		Policy: policy,
		Approver: func(ctx context.Context, def Definition, call Call) error {
			approved = true
			return nil
		},
	})
	executed := false
	mustRegister(t, reg, &fakeTool{
		def:  writeDefinition("write_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { executed = true; return Result{}, nil },
	})
	_, err = d.Dispatch(context.Background(), Call{ID: "1", Name: "write_file"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if approved || executed {
		t.Fatal("capability denial must happen before approval or execution")
	}
}

func TestDispatchNilApprover(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{def: readDefinition("read_file")})
	if _, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "read_file"}); err != nil {
		t.Fatalf("read tool should be allowed: %v", err)
	}

	executed := false
	mustRegister(t, reg, &fakeTool{
		def:  writeDefinition("write_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { executed = true; return Result{}, nil },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "2", Name: "write_file"})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected ErrApprovalRequired, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if executed {
		t.Fatal("write tool ran without approval")
	}
}

func TestDispatchTimeout(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{DefaultTimeout: 30 * time.Millisecond})
	mustRegister(t, reg, &fakeTool{
		def: readDefinition("slow"),
		exec: func(ctx context.Context, call Call) (Result, error) {
			<-ctx.Done()
			return Result{Content: "late"}, ctx.Err()
		},
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "slow"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if !strings.Contains(res.Content, "timed out after") {
		t.Fatalf("unexpected content %q", res.Content)
	}
	if res.CallID != "1" {
		t.Fatalf("unexpected call id %q", res.CallID)
	}
}

func TestDispatchToolTimeoutOverride(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{DefaultTimeout: time.Second})
	def := readDefinition("slow")
	def.Timeout = 20 * time.Millisecond
	mustRegister(t, reg, &fakeTool{
		def: def,
		exec: func(ctx context.Context, call Call) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	})
	start := time.Now()
	_, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "slow"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("tool timeout was not applied, elapsed %v", elapsed)
	}
}

func TestDispatchPanicRecovery(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{
		def:  readDefinition("boom"),
		exec: func(ctx context.Context, call Call) (Result, error) { panic("kaboom") },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "boom"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if !strings.Contains(res.Content, "kaboom") {
		t.Fatalf("unexpected content %q", res.Content)
	}
}

func TestDispatchTruncatesOnUTF8Boundary(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{MaxResultBytes: 2})
	mustRegister(t, reg, &fakeTool{
		def:  readDefinition("utf8"),
		exec: func(ctx context.Context, call Call) (Result, error) { return Result{Content: "aé"}, nil },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "utf8"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "a\n[truncated]" {
		t.Fatalf("unexpected content %q", res.Content)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatal("content is not valid utf-8")
	}
}

func TestDispatchFillsCallID(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{
		def:  readDefinition("read_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { return Result{Content: "ok"}, nil },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "call-1", Name: "read_file"})
	if err != nil {
		t.Fatal(err)
	}
	if res.CallID != "call-1" {
		t.Fatalf("unexpected call id %q", res.CallID)
	}
}

func TestDispatchExecuteError(t *testing.T) {
	sentinel := errors.New("execute failed")
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{
		def:  readDefinition("read_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { return Result{}, sentinel },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "read_file"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if !strings.HasPrefix(res.Content, "error (read): ") {
		t.Fatalf("unexpected content %q", res.Content)
	}
	if res.CallID != "1" {
		t.Fatalf("unexpected call id %q", res.CallID)
	}
}

func TestDispatchConcurrent(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{
		def: readDefinition("read_file"),
		exec: func(ctx context.Context, call Call) (Result, error) {
			return Result{Content: "ok:" + call.ID}, nil
		},
	})
	const workers = 50
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("call-%d", i)
			res, err := d.Dispatch(context.Background(), Call{ID: id, Name: "read_file"})
			if err != nil {
				errs <- err
				return
			}
			if res.CallID != id || res.Content != "ok:"+id {
				errs <- fmt.Errorf("unexpected result for %s: %+v", id, res)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestDispatcherDefinitions(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{})
	mustRegister(t, reg, &fakeTool{def: readDefinition("b_tool")})
	mustRegister(t, reg, &fakeTool{def: readDefinition("a_tool")})
	defs := d.Definitions()
	if len(defs) != 2 || defs[0].Name != "a_tool" || defs[1].Name != "b_tool" {
		t.Fatalf("unexpected definitions: %+v", defs)
	}
}

func TestDispatchPolicyWriteDenied(t *testing.T) {
	workspace := t.TempDir()
	policy, err := security.New(workspace)
	if err != nil {
		t.Fatalf("security.New: %v", err)
	}
	executed := false
	d, reg := newTestDispatcher(t, DispatcherOptions{
		Policy:   policy,
		Approver: func(ctx context.Context, def Definition, call Call) error { return nil },
	})
	mustRegister(t, reg, &fakeTool{
		def:  writeDefinition("write_file"),
		exec: func(ctx context.Context, call Call) (Result, error) { executed = true; return Result{}, nil },
	})
	res, err := d.Dispatch(context.Background(), Call{ID: "1", Name: "write_file"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied, got %v", err)
	}
	if executed {
		t.Fatal("write tool executed without write policy")
	}
	if !res.IsError || res.CallID != "1" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestDispatchContextCancellation(t *testing.T) {
	d, reg := newTestDispatcher(t, DispatcherOptions{DefaultTimeout: time.Second})
	mustRegister(t, reg, &fakeTool{
		def: readDefinition("blocked"),
		exec: func(ctx context.Context, call Call) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := d.Dispatch(ctx, Call{ID: "cancel-1", Name: "blocked"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !res.IsError {
		t.Fatal("result should be an error")
	}
	if res.CallID != "cancel-1" {
		t.Fatalf("unexpected call id %q", res.CallID)
	}
}
