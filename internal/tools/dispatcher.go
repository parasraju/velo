package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"velo/internal/security"
)

const (
	defaultDispatchTimeout = 30 * time.Second
	defaultMaxResultBytes  = 64 << 10
)

type Approver func(ctx context.Context, def Definition, call Call) error

type Dispatcher struct {
	registry       *Registry
	policy         *security.Policy
	approver       Approver
	defaultTimeout time.Duration
	maxResultBytes int
}

type DispatcherOptions struct {
	Registry       *Registry
	Policy         *security.Policy
	Approver       Approver
	DefaultTimeout time.Duration
	MaxResultBytes int
}

func NewDispatcher(opts DispatcherOptions) (*Dispatcher, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("tools: new dispatcher: %w: registry is required", ErrInvalidArguments)
	}
	defaultTimeout := opts.DefaultTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = defaultDispatchTimeout
	}
	maxResultBytes := opts.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMaxResultBytes
	}
	return &Dispatcher{
		registry:       opts.Registry,
		policy:         opts.Policy,
		approver:       opts.Approver,
		defaultTimeout: defaultTimeout,
		maxResultBytes: maxResultBytes,
	}, nil
}

func (d *Dispatcher) Definitions() []Definition {
	return d.registry.List()
}

// Definition returns the registered definition for name.
func (d *Dispatcher) Definition(name string) (Definition, bool) {
	tool, ok := d.registry.Get(name)
	if !ok {
		return Definition{}, false
	}
	return tool.Definition(), true
}

type dispatchOutcome struct {
	res Result
	err error
}

func (d *Dispatcher) Dispatch(ctx context.Context, call Call) (Result, error) {
	callID := strings.TrimSpace(call.ID)
	name := strings.TrimSpace(call.Name)
	if callID == "" {
		return dispatchFailure(call.ID, "", fmt.Errorf("%w: call id is required", ErrInvalidArguments))
	}
	if name == "" {
		return dispatchFailure(call.ID, "", fmt.Errorf("%w: call name is required", ErrInvalidArguments))
	}
	tool, ok := d.registry.Get(name)
	if !ok {
		return dispatchFailure(call.ID, "", fmt.Errorf("%w: %q", ErrNotFound, name))
	}
	def := tool.Definition()

	args, err := normalizeArguments(call.Arguments)
	if err != nil {
		return dispatchFailure(call.ID, def.Category, err)
	}
	call.ID = callID
	call.Name = name
	call.Arguments = args

	if d.policy != nil {
		switch def.Category {
		case CategoryWrite:
			if !d.policy.AllowsWrite() {
				return dispatchFailure(call.ID, def.Category, fmt.Errorf("%w: file writes are disabled", ErrDenied))
			}
		case CategoryExec:
			if !d.policy.AllowsExecute() {
				return dispatchFailure(call.ID, def.Category, fmt.Errorf("%w: command execution is disabled", ErrDenied))
			}
		case CategoryNet:
			if !d.policy.AllowsNetwork() {
				return dispatchFailure(call.ID, def.Category, fmt.Errorf("%w: network access is disabled", ErrDenied))
			}
		}
	}

	if !def.ReadOnly {
		if d.approver != nil {
			if err := d.approver(ctx, def, call); err != nil {
				return dispatchFailure(call.ID, def.Category, fmt.Errorf("%w: %w", ErrDenied, err))
			}
		} else {
			return dispatchFailure(call.ID, def.Category, fmt.Errorf("%w: %q", ErrApprovalRequired, def.Name))
		}
	}

	timeout := def.Timeout
	if timeout <= 0 {
		timeout = d.defaultTimeout
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outcome := make(chan dispatchOutcome, 1)
	go func() {
		var out dispatchOutcome
		defer func() {
			if p := recover(); p != nil {
				out.err = fmt.Errorf("tools: panic in %q: %v", def.Name, p)
				out.res = Result{CallID: call.ID, Content: fmt.Sprintf("panic: %v", p), IsError: true}
			}
			outcome <- out
		}()
		out.res, out.err = tool.Execute(runCtx, call)
	}()

	var res Result
	var execErr error
	if timeout > 0 {
		select {
		case out := <-outcome:
			res, execErr = out.res, out.err
		case <-runCtx.Done():
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				terr := fmt.Errorf("%w: %s after %s", ErrTimeout, def.Name, timeout)
				return d.finish(call.ID, Result{
					CallID:  call.ID,
					Content: "timed out after " + timeout.String(),
					IsError: true,
				}, terr)
			}
			cerr := runCtx.Err()
			return d.finish(call.ID, Result{
				CallID:  call.ID,
				Content: cerr.Error(),
				IsError: true,
			}, cerr)
		}
	} else {
		out := <-outcome
		res, execErr = out.res, out.err
	}

	if execErr != nil {
		res = Result{
			CallID:  call.ID,
			Content: "error (" + string(def.Category) + "): " + execErr.Error(),
			IsError: true,
		}
		return d.finish(call.ID, res, execErr)
	}
	if res.CallID == "" {
		res.CallID = call.ID
	}
	return d.finish(call.ID, res, nil)
}

func (d *Dispatcher) finish(callID string, res Result, err error) (Result, error) {
	if res.CallID == "" {
		res.CallID = callID
	}
	res.Content = truncateContent(res.Content, d.maxResultBytes)
	return res, err
}

func dispatchFailure(callID string, category Category, err error) (Result, error) {
	content := "error: " + err.Error()
	if category != "" {
		content = "error (" + string(category) + "): " + err.Error()
	}
	return Result{CallID: callID, Content: content, IsError: true}, err
}

func normalizeArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%w: arguments are not valid json", ErrInvalidArguments)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: arguments must be a json object", ErrInvalidArguments)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after arguments", ErrInvalidArguments)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	return json.RawMessage(buf.Bytes()), nil
}

func truncateContent(content string, max int) string {
	if max <= 0 || len(content) <= max {
		return content
	}
	cut := content[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n[truncated]"
}
