package runtime

import "time"

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusInterrupted RunStatus = "interrupted"
	RunStatusFailed      RunStatus = "failed"
)

type RunSnapshot struct {
	RunID      string
	StepID     string
	Status     RunStatus
	ActiveKind ActiveKind
	GoalLoop   bool
	StartedAt  time.Time
	FinishedAt time.Time
}

type ActiveKind string

const (
	ActiveKindUserTurn            ActiveKind = "user_turn"
	ActiveKindWorkflowTurn        ActiveKind = "workflow_turn"
	ActiveKindGoalLoop            ActiveKind = "goal_loop"
	ActiveKindCompaction          ActiveKind = "compaction"
	ActiveKindPreSubmitCompaction ActiveKind = "pre_submit_compaction"
	ActiveKindUserShell           ActiveKind = "user_shell"
	ActiveKindBackground          ActiveKind = "background"
	ActiveKindRuntimeMaintenance  ActiveKind = "runtime_maintenance"
)

func (k ActiveKind) Valid() bool {
	switch k {
	case ActiveKindUserTurn,
		ActiveKindWorkflowTurn,
		ActiveKindGoalLoop,
		ActiveKindCompaction,
		ActiveKindPreSubmitCompaction,
		ActiveKindUserShell,
		ActiveKindBackground,
		ActiveKindRuntimeMaintenance:
		return true
	default:
		return false
	}
}
