package agent

import "fmt"

type State string

const (
	StateCreated              State = "created"
	StateContextBuilding      State = "context_building"
	StateRequestingModel      State = "requesting_model"
	StateAwaitingToolApproval State = "awaiting_tool_approval"
	StateExecutingTools       State = "executing_tools"
	StateProcessingResults    State = "processing_results"
	StateVerifying            State = "verifying"
	StateCompleted            State = "completed"
	StateFailed               State = "failed"
	StateCancelled            State = "cancelled"
	StateInterrupted          State = "interrupted"
	StateRecoveryRequired     State = "recovery_required"
)

func (s State) Valid() bool {
	switch s {
	case StateCreated, StateContextBuilding, StateRequestingModel, StateAwaitingToolApproval,
		StateExecutingTools, StateProcessingResults, StateVerifying, StateCompleted,
		StateFailed, StateCancelled, StateInterrupted, StateRecoveryRequired:
		return true
	default:
		return false
	}
}

func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func CanTransition(from, to State) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if to == StateInterrupted && !from.Terminal() {
		return true
	}
	switch from {
	case StateCreated:
		return to == StateContextBuilding || to == StateFailed || to == StateCancelled
	case StateContextBuilding:
		return to == StateRequestingModel || to == StateFailed || to == StateCancelled
	case StateRequestingModel:
		return to == StateProcessingResults || to == StateFailed || to == StateCancelled
	case StateProcessingResults:
		return to == StateAwaitingToolApproval || to == StateVerifying || to == StateContextBuilding ||
			to == StateFailed || to == StateCancelled
	case StateAwaitingToolApproval:
		return to == StateExecutingTools || to == StateProcessingResults || to == StateCancelled || to == StateFailed
	case StateExecutingTools:
		return to == StateProcessingResults || to == StateFailed || to == StateCancelled || to == StateRecoveryRequired
	case StateVerifying:
		return to == StateCompleted || to == StateFailed || to == StateCancelled
	case StateInterrupted:
		return to == StateRecoveryRequired || to == StateContextBuilding || to == StateFailed || to == StateCancelled
	case StateRecoveryRequired:
		return to == StateContextBuilding || to == StateFailed || to == StateCancelled
	default:
		return false
	}
}

type Machine struct {
	state State
}

func NewMachine() *Machine {
	return &Machine{state: StateCreated}
}

func (m *Machine) State() State {
	if m == nil {
		return ""
	}
	return m.state
}

func (m *Machine) Transition(to State) error {
	if m == nil {
		return fmt.Errorf("agent: nil machine")
	}
	if !to.Valid() {
		return fmt.Errorf("agent: invalid state %q", to)
	}
	from := m.state
	if !CanTransition(from, to) {
		return fmt.Errorf("agent: invalid transition %q -> %q", from, to)
	}
	m.state = to
	return nil
}

func (m *Machine) force(to State) {
	if m == nil {
		return
	}
	m.state = to
}
