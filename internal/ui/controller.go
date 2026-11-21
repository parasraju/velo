// Package ui implements the interactive terminal interface (TUI) for VELO.
//
// The package is presentation-only: it drives a run through a Controller and
// renders the events that Controller publishes. It deliberately depends on
// just the events and protocol packages, never on the runtime, provider, or
// tool implementations, so the composition root stays in internal/app.
package ui

import (
	"context"

	"velo/internal/events"
	"velo/internal/protocol"
)

// Info describes the active session and environment for the status bar.
type Info struct {
	Version   string
	Workspace string
	Model     string
	Provider  string
	Ready     bool
	SessionID string
	Status    string
}

// ProviderRow is one entry in the provider picker. Model is empty there;
// model choices are a separate list.
type ProviderRow struct {
	Name     string
	Model    string
	Current  bool
	Ready    bool
	NeedsKey bool
	Detail   string
}

// ModelRow is one entry in the model picker for the selected provider.
type ModelRow struct {
	Name    string
	Current bool
}

// SessionRow is one entry in the session picker.
type SessionRow struct {
	ID        string
	Status    string
	Turns     int
	Messages  int
	Updated   string
	Objective string
	Current   bool
}

// Decision answers a pending approval for the tool call identified by ID.
type Decision struct {
	ID      string
	Approve bool
	Trust   bool
}

// Controller is the runtime-facing surface the UI needs. The app package
// supplies a concrete implementation; the UI never reaches past it.
//
// Lifecycle: the UI calls Run from a dedicated goroutine and consumes Events
// concurrently. Run returns when the agent finishes; its completion is also
// signalled by the terminal RunFinished event, so the UI should key off the
// event stream for rendering and use Run's return only for logging.
type Controller interface {
	// Info returns a snapshot of the active session and environment.
	Info() Info
	// Events returns the live event stream. It is delivered once to the UI on
	// startup and closed when the controller shuts down.
	Events() <-chan events.Event
	// Run executes objective against the active session, blocking until the
	// agent stops. It is safe to call once per run; concurrent calls are the
	// caller's responsibility to avoid.
	Run(ctx context.Context, objective string) error
	// Decide resolves a pending approval. It is a no-op for unknown IDs.
	Decide(d Decision)
	// ListSessions returns saved sessions for the picker.
	ListSessions() ([]SessionRow, error)
	// Resume switches the active session to id and reports the new info.
	Resume(id string) (Info, error)
	// Transcript returns the messages of the currently active session, for
	// rendering history after a resume.
	Transcript() ([]protocol.Message, error)
	// NewSession starts a fresh session and reports its info.
	NewSession() (Info, error)
	// Report returns a plain-text view named status, config, models, or tools.
	Report(name string) (string, error)
	// ListProviders returns the registered providers and which one is active.
	ListProviders() ([]ProviderRow, error)
	// UseProvider selects name and connects it when no secret is required.
	UseProvider(name string) (Info, string, error)
	// ConnectKey stores an API key for the current provider in this process.
	ConnectKey(key string) (Info, string, error)
	// ListModels returns models for the selected provider.
	ListModels() ([]ModelRow, error)
	// UseModel switches the selected provider to model and reconnects.
	UseModel(model string) (Info, string, error)
	// Cancel aborts the in-flight run, if any.
	Cancel()
	// Shutdown releases controller resources and closes the Events channel so
	// any pending polls terminate.
	Shutdown()
}
