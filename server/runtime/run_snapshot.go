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
	ActiveKindUserTurn     ActiveKind = "user_turn"
	ActiveKindWorkflowTurn ActiveKind = "workflow_turn"
	ActiveKindGoalLoop     ActiveKind = "goal_loop"
	ActiveKindCompaction   ActiveKind = "compaction"
	ActiveKindBackground   ActiveKind = "background"
	ActiveKindInspection   ActiveKind = "inspection"
)

func (k ActiveKind) Valid() bool {
	switch k {
	case ActiveKindUserTurn,
		ActiveKindWorkflowTurn,
		ActiveKindGoalLoop,
		ActiveKindCompaction,
		ActiveKindBackground,
		ActiveKindInspection:
		return true
	default:
		return false
	}
}
