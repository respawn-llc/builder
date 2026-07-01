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
	GoalLoop   bool
	StartedAt  time.Time
	FinishedAt time.Time
}
