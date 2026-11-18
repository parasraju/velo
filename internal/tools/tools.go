package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound         = errors.New("tools: tool not found")
	ErrDuplicate        = errors.New("tools: tool already registered")
	ErrInvalidArguments = errors.New("tools: invalid arguments")
	ErrDenied           = errors.New("tools: call denied")
	ErrApprovalRequired = errors.New("tools: approval required")
	ErrTimeout          = errors.New("tools: timed out")
	ErrConflict         = errors.New("tools: conflict with current state")
)

type Category string

const (
	CategoryRead  Category = "read"
	CategoryWrite Category = "write"
	CategoryExec  Category = "exec"
	CategoryNet   Category = "net"
)

func (c Category) Valid() bool {
	switch c {
	case CategoryRead, CategoryWrite, CategoryExec, CategoryNet:
		return true
	default:
		return false
	}
}

func (c Category) IsReadOnly() bool {
	return c == CategoryRead
}

type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, call Call) (Result, error)
}

type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Category    Category        `json:"category"`
	ReadOnly    bool            `json:"read_only"`
	Timeout     time.Duration   `json:"timeout,omitempty"`
}

type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type Result struct {
	CallID   string         `json:"call_id,omitempty"`
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}
