package app

import (
	"time"

	"core/cli/app/commands"
	"core/cli/tui"
	tuiinput "core/cli/tui/input"
	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type uiModel struct {
	eventDispatcher *uiEventDispatcher
	uiRuntimeFeatureState
	uiInputFeatureState
	uiPresentationFeatureState
	uiConversationFeatureState
	uiSessionTransitionFeatureState
	uiStatusFeatureState
	uiTranscriptFeatureState
	uiKeyboardFeatureState
	uiRollbackFeatureState
	uiWorktreeFeatureState
}

type uiRuntimeFeatureState struct {
	engine clientui.RuntimeClient
	view   tui.Model

	processClient         clientui.ProcessClient
	processClientExplicit bool
	worktreeClient        apicontract.WorktreeService

	pathReferenceEvents        <-chan uiPathReferenceSearchEvent
	runtimeConnectionEvents    chan runtimeConnectionStateChangedMsg
	runtimeReconnectWarning    <-chan runtimeReconnectWarningMsg
	runtimeContextUsage        clientui.RuntimeContextUsage
	runtimeContextUsageSession string
	runtimeActivityProjection  clientui.RuntimeActivity
	logger                     uiLogger
}

type uiInputFeatureState struct {
	mainEditor             tuiinput.Editor
	mainInputDraftToken    uint64
	promptHistory          []string
	promptHistorySelection *int
	promptHistoryDraft     *tuiinput.EditorSnapshot
	activity               uiActivity
	runtimeLifecycle       uiRuntimeLifecycle
	reviewerEnabled        bool
	reviewerMode           string
	autoCompactionEnabled  bool
	questionsEnabled       bool
	conversationFreshness  clientui.ConversationFreshness
	localConversationTurn  bool
	runtimeControlToken    uint64
	runtimeControlTokens   map[runtimeControlOperation]uint64
	runtimeControlPending  map[runtimeControlOperation]runtimeControlPendingState

	// UI-side post-turn input queue. It may contain slash commands, shell
	// commands, and other client-only actions; server queues only runtime
	// injected user work.
	queued                      []queuedInputItem
	pendingCompactionRequestIDs map[runtimeids.CompactionRequestID]struct{}
	pendingWork                 runtimeinput.PendingWork
	submitToken                 uint64
	activeSubmit                activeSubmitState

	injectedQueue               []injectedRuntimeQueueItem
	injectedQueueToken          uint64
	unownedQueuedTerminalStates map[string]clientui.TranscriptQueuedMessageState
	pendingInputSubmissionOrder uint64
	interruptLifecycle          uiInterruptLifecycle
	currentRunID                string
	currentStepID               string
	interruptRunID              string
	interruptStepID             string
	completedRunID              string
	completedStepID             string

	modelName                 string
	configuredModelName       *string
	thinkingLevel             string
	fastModeAvailable         bool
	fastModeEnabled           bool
	modelContractLocked       bool
	spinnerFrame              int
	spinnerClock              frameAnimationClock
	spinnerTickDue            time.Time
	spinnerGeneration         uint64
	spinnerTickToken          uint64
	commandRegistry           *commands.Registry
	promptCatalog             apicontract.PromptCommandCatalogService
	promptCatalogEntries      []commands.PromptCommandCatalogEntry
	promptCatalogRefreshToken *uuid.UUID
	finalAnswerOperation      *uiFinalAnswerOperation
	finalAnswerOperationToken uint64
	authSlashCommand          authSlashCommandKind
	authSlashCommandErr       string
	authSlashSessionOpen      bool
	authSlashLoading          bool
	authSlashToken            uint64
	authSlashGeneration       uint64
	authSlashResolved         uint64
	slashCommandFilter        string
	slashCommandFilterSet     bool
	slashCommandSelection     int
	pathReferenceSearch       uiPathReferenceSearch
	pathReference             uiPathReferenceState
}

type uiPresentationFeatureState struct {
	theme                       string
	tuiNativeProgressBar        bool
	terminalOutput              *uiTerminalOutput
	nativeProgress              uiNativeProgressState
	markdownLinks               transcriptrender.MarkdownLinkPresentation
	activeSurface               uiSurface
	altScreenActive             bool
	terminalFocus               *terminalFocusState
	terminalCursor              *uiTerminalCursorState
	rendererOutputGate          *uiRendererOutputGateState
	ongoingSurface              *ongoing.Surface
	ongoingTranscript           *ongoingTranscriptController
	requestOngoingOpen          func()
	ongoingWidthToken           uint64
	pendingOngoingScratchReset  *ongoing.RehydrateReason
	pendingOngoingWidthReset    bool
	pendingOngoingResizeRepaint bool
	terminalGeometry            terminalGeometry
	ownershipReconciler         *ongoingOwnershipReconciler
	pendingOwnershipCmd         tea.Cmd
	helpVisible                 bool
	startupCmds                 []tea.Cmd
	uiMainThread                uiMainThreadState
}

type uiConversationFeatureState struct {
	interaction                        uiInteractionState
	ask                                uiAskState
	questionProjector                  questionProjector
	promptAnswers                      *transcriptPromptAnswerer
	promptAttention                    promptAttentionSink
	startupSubmit                      string
	startupSubmitPromptHistoryRecorded bool
}

type uiSessionTransitionFeatureState struct {
	exitAction                              UIAction
	nextSessionInitialPrompt                string
	nextSessionInitialPromptHistoryRecorded bool
	nextSessionInitialInput                 *string
	nextSessionID                           string
	nextForkRollbackTargetID                string
	nextPreviousSessionID                   *runtimeids.SessionID
	sessionExecutionTarget                  *clientui.SessionExecutionTarget
	sessionRetargeted                       bool
	sessionName                             string
	sessionID                               string
	forcedLocalExit                         bool
}

type uiStatusFeatureState struct {
	processList                 uiProcessListState
	reasoningStatusHeader       string
	turnQueueHook               turnQueueHook
	statusConfig                uiStatusConfig
	statusCollector             uiStatusCollector
	statusRepository            uiStatusRepository
	status                      uiStatusOverlayState
	goal                        uiGoalOverlayState
	goalRuntimeToken            uint64
	goalRuntimeMutationSerial   uint64
	goalRuntimePending          goalRuntimePendingState
	statusGitBackgroundInFlight bool
	clipboardPaster             uiClipboardPaster
	clipboardTextCopier         uiClipboardTextCopier

	transientStatus          string
	transientStatusKind      uiStatusNoticeKind
	transientStatusNoticeID  string
	transientStatusRequestID *uuid.UUID
	transientStatusToken     uint64
	transientStatusQueue     []uiStatusNotice
	localNoticeSequence      uint64
	debugKeys                bool
	debugMode                bool
}

type uiTranscriptFeatureState struct {
	runtimeConnection            clientui.RuntimeConnectionLifecycle
	runtimeMainViewToken         uint64
	runtimeMainViewBusy          bool
	runtimeMainViewActiveRequest runtimeMainViewRefreshRequest
	runtimeMainViewPendingSet    bool
	runtimeMainViewPending       runtimeMainViewRefreshRequest
	detailTranscript             uiDetailTranscriptWindow
	pendingDetailTranscript      *uiPendingDetailTranscriptRequest
}

type uiKeyboardFeatureState struct {
	lastEscAt              time.Time
	pendingCSIShiftEnterAt time.Time
	pendingCSIShiftEnter   bool
}

type uiRollbackFeatureState struct {
	rollback uiRollbackState
}

type uiWorktreeFeatureState struct {
	worktrees                        uiWorktreeOverlayState
	worktreeListGeneration           uint64
	deleteTargetResolutionGeneration uint64
}
