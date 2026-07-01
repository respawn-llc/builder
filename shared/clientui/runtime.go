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

type TranscriptMetadata struct {
	Revision            int64
	CommittedEntryCount int
}

type SessionExecutionTarget struct {
	WorkspaceID           string
	WorkspaceName         string
	WorkspaceRoot         string
	WorkspaceAvailability string
	WorktreeID            string
	WorktreeName          string
	WorktreeRoot          string
	WorktreeAvailability  string
	CwdRelpath            string
	EffectiveWorkdir      string
}

func NormalizeSessionExecutionTarget(target SessionExecutionTarget) SessionExecutionTarget {
	return SessionExecutionTarget{
		WorkspaceID:           strings.TrimSpace(target.WorkspaceID),
		WorkspaceName:         strings.TrimSpace(target.WorkspaceName),
		WorkspaceRoot:         strings.TrimSpace(target.WorkspaceRoot),
		WorkspaceAvailability: strings.TrimSpace(target.WorkspaceAvailability),
		WorktreeID:            strings.TrimSpace(target.WorktreeID),
		WorktreeName:          strings.TrimSpace(target.WorktreeName),
		WorktreeRoot:          strings.TrimSpace(target.WorktreeRoot),
		WorktreeAvailability:  strings.TrimSpace(target.WorktreeAvailability),
		CwdRelpath:            strings.TrimSpace(target.CwdRelpath),
		EffectiveWorkdir:      strings.TrimSpace(target.EffectiveWorkdir),
	}
}

func SessionExecutionTargetIsZero(target SessionExecutionTarget) bool {
	return NormalizeSessionExecutionTarget(target) == SessionExecutionTarget{}
}

func SessionExecutionTargetsEqual(a SessionExecutionTarget, b SessionExecutionTarget) bool {
	return NormalizeSessionExecutionTarget(a) == NormalizeSessionExecutionTarget(b)
}

type RuntimeSessionView struct {
	SessionID             string
	SessionName           string
	ConversationFreshness ConversationFreshness
	ExecutionTarget       SessionExecutionTarget
	Transcript            TranscriptMetadata
	Chat                  ChatSnapshot
}

type RuntimeClient interface {
	MainView() RuntimeMainView
	RefreshMainView() (RuntimeMainView, error)
	Transcript() TranscriptPage
	RefreshTranscript() (TranscriptPage, error)
	RefreshTranscriptPage(req TranscriptPageRequest) (TranscriptPage, error)
	LoadTranscriptPage(req TranscriptPageRequest) (TranscriptPage, error)
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
