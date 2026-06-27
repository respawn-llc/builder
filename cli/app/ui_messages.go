package app

import (
	"os/exec"
	"strings"
	"time"

	"core/cli/tui"
	"core/shared/clientui"
)

type submitDoneMsg struct {
	token         uint64
	message       string
	submittedText string
	silentFinal   bool
	err           error
}

func newSubmitDoneMsg(token uint64, message string, submittedText string, err error) submitDoneMsg {
	return submitDoneMsg{
		token:         token,
		message:       message,
		submittedText: submittedText,
		silentFinal:   isNoopFinalText(message),
		err:           err,
	}
}

type promptHistoryPersistErrMsg struct {
	err error
}

type committedEntryPersistDoneMsg struct {
	noticeID string
	role     string
	text     string
	err      error
}

type authSlashCommandRefreshedMsg struct {
	token      uint64
	generation uint64
	name       string
	err        error
}

type goalRuntimeOperation string

const (
	goalRuntimeShow       goalRuntimeOperation = "show"
	goalRuntimeCheckSet   goalRuntimeOperation = "check_set"
	goalRuntimeCheckClear goalRuntimeOperation = "check_clear"
	goalRuntimeSet        goalRuntimeOperation = "set"
	goalRuntimePause      goalRuntimeOperation = "pause"
	goalRuntimeResume     goalRuntimeOperation = "resume"
	goalRuntimeClear      goalRuntimeOperation = "clear"
)

type goalRuntimeDoneMsg struct {
	token          uint64
	sessionID      string
	mutationSerial uint64
	operation      goalRuntimeOperation
	objective      string
	goal           *clientui.RuntimeGoal
	err            error
}

type runtimeControlOperation string

const (
	runtimeControlSetSessionName    runtimeControlOperation = "set_session_name"
	runtimeControlSetThinkingLevel  runtimeControlOperation = "set_thinking_level"
	runtimeControlSetFastMode       runtimeControlOperation = "set_fast_mode"
	runtimeControlSetReviewer       runtimeControlOperation = "set_reviewer"
	runtimeControlSetAutoCompaction runtimeControlOperation = "set_auto_compaction"
	runtimeControlSetQuestions      runtimeControlOperation = "set_questions"
	runtimeControlInterrupt         runtimeControlOperation = "interrupt"
)

type runtimeControlDoneMsg struct {
	token          uint64
	sessionID      string
	operation      runtimeControlOperation
	text           string
	enabled        bool
	changed        bool
	mode           string
	compactionMode string
	err            error
}

type injectedQueueCreateDoneMsg struct {
	token                    uint64
	localID                  string
	item                     clientui.QueuedUserMessage
	approvalCommentaryAnswer *clientui.PromptAnswer
	err                      error
}

type injectedQueueDiscardDoneMsg struct {
	token     uint64
	localID   string
	serverID  string
	discarded bool
}

type queuedRuntimeWorkCheckDoneMsg struct {
	token   uint64
	hasWork bool
	err     error
}

type compactDoneMsg struct {
	err error
}

type nativeSurfaceResumeMsg struct{}

type nativeSurfaceResizeRehydrateMsg struct {
	token  uint64
	width  int
	height int
}

// Active submit is the in-flight turn only. uiModel.queued stores future work;
// never mirror active submit there or it can run again after completion.
type activeSubmitState struct {
	token              uint64
	stepID             string
	text               string
	queuedID           string
	restoreOnInterrupt bool
	flushed            bool
}

type spinnerTickMsg struct {
	token uint64
	at    time.Time
}

type processListRefreshTickMsg struct{}

type processListRefreshDoneMsg struct {
	token   uint64
	entries []clientui.BackgroundProcess
	err     error
}

type processActionDoneMsg struct {
	token             uint64
	surfaceGeneration uint64
	inputDraftToken   uint64
	action            string
	id                string
	output            string
	logPath           string
	editorCmd         *exec.Cmd
	err               error
}

type openProcessLogsDoneMsg struct {
	err error
}

type clearTransientStatusMsg struct {
	token uint64
}

type startupUpdateNoticeMsg struct {
	version string
}

type runtimeEventMsg struct {
	event clientui.Event
}

type runtimeEventBatchMsg struct {
	events []clientui.Event
	carry  *clientui.Event
}

type uiModelProbeMessage interface {
	probeUIModel(*uiModel)
}

type runtimeConnectionStateChangedMsg struct {
	err error
}

type runtimeReconnectWarningMsg struct {
	text       string
	visibility clientui.EntryVisibility
}

type runtimeMainViewRefreshedMsg struct {
	token uint64
	req   runtimeMainViewRefreshRequest
	view  clientui.RuntimeMainView
	err   error
}

type runtimeTranscriptRefreshedMsg struct {
	token         uint64
	req           clientui.TranscriptPageRequest
	syncRequest   runtimeTranscriptSyncRequest
	syncCause     runtimeTranscriptSyncCause
	transcript    clientui.TranscriptPage
	recoveryCause clientui.TranscriptRecoveryCause
	err           error
}

type runtimeTranscriptRetryMsg struct {
	syncCause     runtimeTranscriptSyncCause
	token         uint64
	recoveryCause clientui.TranscriptRecoveryCause
	req           runtimeTranscriptSyncRequest
}

type runtimeTranscriptSyncCause string

const (
	runtimeTranscriptSyncCauseBootstrap               runtimeTranscriptSyncCause = "bootstrap"
	runtimeTranscriptSyncCauseCommittedConversation   runtimeTranscriptSyncCause = "committed_conversation_updated"
	runtimeTranscriptSyncCauseCommittedGap            runtimeTranscriptSyncCause = "committed_gap"
	runtimeTranscriptSyncCauseQueuedDrain             runtimeTranscriptSyncCause = "queued_drain"
	runtimeTranscriptSyncCauseDirtyFollowUp           runtimeTranscriptSyncCause = "dirty_follow_up"
	runtimeTranscriptSyncCauseContinuityRecovery      runtimeTranscriptSyncCause = "continuity_recovery"
	runtimeTranscriptSyncCauseManualTranscriptRefresh runtimeTranscriptSyncCause = "manual_transcript_refresh"
)

type runtimeMainViewRefreshCause string

const (
	runtimeMainViewRefreshCauseWorktreeMutation runtimeMainViewRefreshCause = "worktree_mutation"
	runtimeMainViewRefreshCauseManual           runtimeMainViewRefreshCause = "manual"
	runtimeMainViewRefreshCauseStartupUpdate    runtimeMainViewRefreshCause = "startup_update"
)

type detailTranscriptLoadMsg struct{}

type renderDiagnosticMsg struct {
	diagnostic tui.RenderDiagnostic
}

type deferredProjectedTranscriptTail struct {
	rangeStart int
	rangeEnd   int
	revision   int64
	entries    []clientui.ChatEntry
	pending    []string
}

type runLoggerDiagnosticMsg struct {
	diagnostic runLoggerDiagnostic
}

type clipboardImagePasteDoneMsg struct {
	Target         uiClipboardPasteTarget
	MainDraftToken uint64
	AskToken       uint64
	Path           string
	Err            error
}

type clipboardTextCopyDoneMsg struct {
	Err error
}

type askEvent struct {
	req              clientui.PendingPromptEvent
	reply            chan askReply
	cancel           func()
	resolvedPromptID string
}

func (e askEvent) promptID() string {
	if strings.TrimSpace(e.resolvedPromptID) != "" {
		return strings.TrimSpace(e.resolvedPromptID)
	}
	return strings.TrimSpace(e.req.PromptID)
}

func (e askEvent) isResolution() bool {
	return strings.TrimSpace(e.resolvedPromptID) != ""
}

func (e askEvent) cancelPending() {
	if e.cancel != nil {
		e.cancel()
	}
}

type askReply struct {
	response clientui.PromptAnswer
	err      error
}

type askEventMsg struct {
	event askEvent
}

type uiStatusNoticeKind uint8

const (
	uiStatusNoticeNeutral uiStatusNoticeKind = iota
	uiStatusNoticeSuccess
	uiStatusNoticeError
	uiStatusNoticeUpdateAvailable
)

type uiStatusNotice struct {
	Text     string
	Kind     uiStatusNoticeKind
	Duration time.Duration
	NoticeID string
}

type uiStatusNoticeDelivery uint8

const (
	uiStatusNoticeReplace uiStatusNoticeDelivery = iota
	uiStatusNoticeQueue
)
