package clientui

import (
	"context"
	"strings"
)

type ConversationFreshness uint8

const (
	ConversationFreshnessFresh ConversationFreshness = iota
	ConversationFreshnessEstablished
)

func (f ConversationFreshness) IsFresh() bool {
	return f == ConversationFreshnessFresh
}

type RuntimeContextUsage struct {
	UsedTokens            int
	WindowTokens          int
	CacheHitPercent       int
	HasCacheHitPercentage bool
}

type RuntimeGoal struct {
	ID        string
	Objective string
	Status    RuntimeGoalStatus
	Suspended bool
}

type RuntimeGoalStatus string

const (
	RuntimeGoalStatusActive   RuntimeGoalStatus = "active"
	RuntimeGoalStatusPaused   RuntimeGoalStatus = "paused"
	RuntimeGoalStatusComplete RuntimeGoalStatus = "complete"
)

type RuntimeStatus struct {
	ReviewerFrequency                 string
	ReviewerEnabled                   bool
	AutoCompactionEnabled             bool
	QuestionsEnabled                  bool
	FastModeAvailable                 bool
	FastModeEnabled                   bool
	ConversationFreshness             ConversationFreshness
	ParentSessionID                   string
	LastCommittedAssistantFinalAnswer string
	ThinkingLevel                     string
	CompactionMode                    string
	ContextUsage                      RuntimeContextUsage
	CompactionCount                   int
	Goal                              *RuntimeGoal
	WorkflowActive                    bool
	WorkflowSession                   *WorkflowSessionStatus
	Update                            UpdateStatus
}

type WorkflowSessionStatus struct {
	RunID      string
	TaskID     string
	WorkflowID string
}

type UpdateStatus struct {
	Checked        bool
	Available      bool
	CurrentVersion string
	LatestVersion  string
}

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusInterrupted RunStatus = "interrupted"
	RunStatusFailed      RunStatus = "failed"
)

type RuntimeMainView struct {
	Version             ReadModelVersion
	Status              RuntimeStatus
	Session             RuntimeSessionView
	Activity            RuntimeActivity
	InputReconciliation RuntimeInputReconciliationSnapshot
}

type QueuedUserMessage struct {
	ID              string
	Text            string
	ClientRequestID string
}

type UserTurnSubmission struct {
	Message string
	Queued  QueuedUserMessage
}

type SessionExecutionTarget struct {
	WorkspaceID           string
	WorkspaceName         string
	WorkspaceRoot         string
	WorkspaceAvailability string
	Worktree              *SessionExecutionWorktreeTarget
	CwdRelpath            string
	EffectiveWorkdir      string
}

type SessionExecutionWorktreeTarget struct {
	ID           string
	Name         string
	Root         string
	Availability string
}

func NormalizeSessionExecutionTarget(target SessionExecutionTarget) SessionExecutionTarget {
	var worktree *SessionExecutionWorktreeTarget
	if target.Worktree != nil {
		worktree = &SessionExecutionWorktreeTarget{
			ID:           strings.TrimSpace(target.Worktree.ID),
			Name:         strings.TrimSpace(target.Worktree.Name),
			Root:         strings.TrimSpace(target.Worktree.Root),
			Availability: strings.TrimSpace(target.Worktree.Availability),
		}
	}
	return SessionExecutionTarget{
		WorkspaceID:           strings.TrimSpace(target.WorkspaceID),
		WorkspaceName:         strings.TrimSpace(target.WorkspaceName),
		WorkspaceRoot:         strings.TrimSpace(target.WorkspaceRoot),
		WorkspaceAvailability: strings.TrimSpace(target.WorkspaceAvailability),
		Worktree:              worktree,
		CwdRelpath:            strings.TrimSpace(target.CwdRelpath),
		EffectiveWorkdir:      strings.TrimSpace(target.EffectiveWorkdir),
	}
}

func SessionExecutionTargetIsZero(target SessionExecutionTarget) bool {
	normalized := NormalizeSessionExecutionTarget(target)
	return normalized.WorkspaceID == "" &&
		normalized.WorkspaceName == "" &&
		normalized.WorkspaceRoot == "" &&
		normalized.WorkspaceAvailability == "" &&
		normalized.Worktree == nil &&
		normalized.CwdRelpath == "" &&
		normalized.EffectiveWorkdir == ""
}

func SessionExecutionTargetsEqual(a SessionExecutionTarget, b SessionExecutionTarget) bool {
	normalizedA := NormalizeSessionExecutionTarget(a)
	normalizedB := NormalizeSessionExecutionTarget(b)
	worktreesEqual := normalizedA.Worktree == normalizedB.Worktree
	if normalizedA.Worktree != nil && normalizedB.Worktree != nil {
		worktreesEqual = normalizedA.Worktree.ID == normalizedB.Worktree.ID &&
			normalizedA.Worktree.Name == normalizedB.Worktree.Name &&
			normalizedA.Worktree.Root == normalizedB.Worktree.Root &&
			normalizedA.Worktree.Availability == normalizedB.Worktree.Availability
	}
	return normalizedA.WorkspaceID == normalizedB.WorkspaceID &&
		normalizedA.WorkspaceName == normalizedB.WorkspaceName &&
		normalizedA.WorkspaceRoot == normalizedB.WorkspaceRoot &&
		normalizedA.WorkspaceAvailability == normalizedB.WorkspaceAvailability &&
		worktreesEqual &&
		normalizedA.CwdRelpath == normalizedB.CwdRelpath &&
		normalizedA.EffectiveWorkdir == normalizedB.EffectiveWorkdir
}

type RuntimeSessionView struct {
	SessionID             string
	SessionName           string
	ConversationFreshness ConversationFreshness
	ExecutionTarget       SessionExecutionTarget
}

type RuntimeClient interface {
	MainView() RuntimeMainView
	RefreshMainView() (RuntimeMainView, error)
	Status() RuntimeStatus
	SessionView() RuntimeSessionView
	SetSessionName(name string) error
	SetThinkingLevel(level string) error
	SetFastModeEnabled(enabled bool) (bool, error)
	SetReviewerEnabled(enabled bool) (bool, string, error)
	SetAutoCompactionEnabled(enabled bool) (bool, bool, error)
	SetQuestionsEnabled(enabled bool) (bool, error)
	ShowGoal() (*RuntimeGoal, error)
	SetGoal(objective string) (*RuntimeGoal, error)
	PauseGoal() (*RuntimeGoal, error)
	ResumeGoal() (*RuntimeGoal, error)
	CompleteGoal() (*RuntimeGoal, error)
	ClearGoal() (*RuntimeGoal, error)
	AppendCommittedEntry(role, text string) error
	AppendCommittedEntryWithNoticeID(role, text, noticeID string) error
	SubmitRuntimeInput(ctx context.Context, req RuntimeSubmitRequest) (UserTurnSubmission, error)
	RunUserShell(ctx context.Context, req RuntimeShellRequest) error
	CompactRuntime(ctx context.Context, req RuntimeCompactRequest) error
	HasQueuedUserWork() (bool, error)
	SubmitRuntimeQueued(ctx context.Context, req RuntimeSubmitQueuedRequest) (string, error)
	Interrupt() error
	QueueRuntimeUserMessage(req RuntimeQueueUserMessageRequest) (QueuedUserMessage, error)
	DiscardQueuedUserMessage(queueItemID string) bool
	RecordPromptHistory(text string) error
}
