package app

import (
	"os/exec"
	"strings"
	"time"

	"core/cli/app/commands"
	"core/shared/clientui"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

type submitDoneMsg struct {
	token         uint64
	message       string
	submittedText string
	resultKind    *clientui.UserTurnResultKind
	queued        clientui.QueuedUserMessage
	err           error
}

func newSubmitDoneMsg(token uint64, message string, submittedText string, err error) submitDoneMsg {
	return submitDoneMsg{
		token:         token,
		message:       message,
		submittedText: submittedText,
		err:           err,
	}
}

type promptHistoryPersistErrMsg struct {
	err error
}

type promptCatalogRefreshDoneMsg struct {
	token   *uuid.UUID
	entries []commands.PromptCommandCatalogEntry
	err     error
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
	kind       authSlashCommandKind
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
	goalRuntimeComplete   goalRuntimeOperation = "complete"
	goalRuntimeClear      goalRuntimeOperation = "clear"
)

type goalRuntimeDoneMsg struct {
	token          uint64
	sessionID      string
	mutationSerial uint64
	operation      goalRuntimeOperation
	objective      string
	goal           *clientui.RuntimeGoal
	mutation       clientui.GoalMutationResult
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
	runtimeTuple   *runtimeTupleCandidate
	err            error
}

type injectedQueueCreateDoneMsg struct {
	token                    uint64
	localID                  string
	item                     clientui.QueuedUserMessage
	completed                bool
	approvalCommentaryAnswer *clientui.PromptAnswer
	err                      error
}

type injectedQueueDiscardDoneMsg struct {
	token     uint64
	localID   string
	serverID  string
	discarded bool
}

type compactDoneMsg struct {
	submittedText string
	origin        uiCompactionOrigin
	invoked       bool
	err           error
}

type activeSubmitOrigin uint8

const (
	activeSubmitOriginDirect activeSubmitOrigin = iota
	activeSubmitOriginQueued
)

// Active submit is the in-flight turn only. uiModel.queued stores future work;
// never mirror active submit there or it can run again after completion.
type activeSubmitState struct {
	token           uint64
	text            string
	queuedID        string
	origin          activeSubmitOrigin
	clientRequestID runtimeids.RuntimeClientRequestID
	submissionOrder inputSubmissionOrder
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

type uiModelProbeMessage interface {
	probeUIModel(*uiModel)
}

type runtimeConnectionStateChangedMsg struct {
	err error
}

type runtimeMainViewRefreshedMsg struct {
	token                    uint64
	req                      runtimeMainViewRefreshRequest
	metadataBaselineRevision *uint64
	view                     clientui.RuntimeMainView
	err                      error
}

type runtimeMainViewRefreshCause string

const (
	runtimeMainViewRefreshCauseWorktreeMutation runtimeMainViewRefreshCause = "worktree_mutation"
	runtimeMainViewRefreshCauseManual           runtimeMainViewRefreshCause = "manual"
)

type detailTranscriptLoadMsg struct {
	requestID uuid.UUID
	page      clientui.TranscriptPage
	err       error
}

type terminalSequenceWriteErrMsg struct {
	err error
}

type clipboardPasteDoneMsg struct {
	Target         uiClipboardPasteTarget
	MainDraftToken uint64
	AskToken       uint64
	Content        uiClipboardContent
	Err            error
}

type clipboardImageDiscardDoneMsg struct {
	Err error
}

type clipboardTextCopyDoneMsg struct {
	operationToken *uint64
	Err            error
}

type askEvent struct {
	prompt           clientui.TranscriptPrompt
	resolvedPromptID clientui.PromptID
}

func (e askEvent) promptID() string {
	if strings.TrimSpace(string(e.resolvedPromptID)) != "" {
		return strings.TrimSpace(string(e.resolvedPromptID))
	}
	return strings.TrimSpace(string(e.prompt.PromptID))
}

func (e askEvent) isResolution() bool {
	return strings.TrimSpace(string(e.resolvedPromptID)) != ""
}

type askEventMsg struct {
	event askEvent
}

type uiStatusNoticeKind uint8

const (
	uiStatusNoticeInfo uiStatusNoticeKind = iota
	uiStatusNoticeSuccess
	uiStatusNoticeWarning
	uiStatusNoticeError
)

type uiStatusNotice struct {
	Text      string
	Kind      uiStatusNoticeKind
	Duration  time.Duration
	NoticeID  string
	RequestID *uuid.UUID
}

type uiStatusNoticeDelivery uint8

const (
	uiStatusNoticeReplace uiStatusNoticeDelivery = iota
	uiStatusNoticeQueue
)
