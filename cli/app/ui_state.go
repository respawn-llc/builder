package app

import (
	"time"

	"core/cli/app/commands"
	"core/cli/app/internal/runtimestate"
	"core/cli/tui"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type uiModel struct {
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
	worktreeClient        client.WorktreeClient

	runtimeEvents              <-chan clientui.Event
	pendingRuntimeEvents       []clientui.Event
	askEvents                  <-chan askEvent
	pathReferenceEvents        <-chan uiPathReferenceSearchEvent
	runtimeConnectionEvents    <-chan runtimeConnectionStateChangedMsg
	runtimeReconnectWarning    <-chan runtimeReconnectWarningMsg
	runtimeContextUsage        clientui.RuntimeContextUsage
	runtimeContextUsageSession string
	runtimeReadModelVersion    clientui.ReadModelVersion
	runtimeActivityProjection  clientui.RuntimeActivity
	logger                     uiLogger
}

type uiInputFeatureState struct {
	input                    string
	inputCursor              int // rune index; -1 means "track tail"
	inputKillBuffer          string
	mainInputDraftToken      uint64
	promptHistory            []string
	promptHistorySelection   int
	promptHistoryDraft       string
	promptHistoryDraftCursor int
	activity                 uiActivity
	runtimeLifecycle         runtimestate.RuntimeRunState
	reviewerEnabled          bool
	reviewerMode             string
	autoCompactionEnabled    bool
	questionsEnabled         bool
	conversationFreshness    clientui.ConversationFreshness
	localConversationTurn    bool
	runtimeControlToken      uint64
	runtimeControlTokens     map[runtimeControlOperation]uint64
	runtimeControlPending    map[runtimeControlOperation]runtimeControlPendingState

	// UI-side post-turn input queue. It may contain slash commands, shell
	// commands, and other client-only actions; server queues only runtime
	// injected user work.
	queued                                 []queuedInputItem
	compactionOrigin                       uiCompactionOrigin
	queuedRuntimeWorkCheckCompactionOrigin uiCompactionOrigin
	pendingRuntimeOperations               []clientui.RuntimeOperationRef
	submitToken                            uint64
	activeSubmit                           activeSubmitState
	recoveredDraftBuffers                  []serverapi.SessionDraftRecoveryBuffer

	pendingInjected    []clientui.QueuedUserMessage
	injectedQueue      []injectedRuntimeQueueItem
	injectedQueueToken uint64
	interruptLifecycle uiInterruptLifecycle
	currentRunID       string
	currentStepID      string
	interruptRunID     string
	interruptStepID    string
	interruptPreActive bool
	completedRunID     string
	completedStepID    string

	modelName             string
	configuredModelName   string
	thinkingLevel         string
	fastModeAvailable     bool
	fastModeEnabled       bool
	modelContractLocked   bool
	spinnerFrame          int
	spinnerClock          frameAnimationClock
	spinnerTickDue        time.Time
	spinnerGeneration     uint64
	spinnerTickToken      uint64
	commandRegistry       *commands.Registry
	hasOtherSessions      bool
	hasOtherSessionsKnown bool
	authSlashCommand      authSlashCommandKind
	authSlashCommandErr   string
	authSlashSessionOpen  bool
	authSlashLoading      bool
	authSlashToken        uint64
	authSlashGeneration   uint64
	authSlashResolved     uint64
	slashCommandFilter    string
	slashCommandFilterSet bool
	slashCommandSelection int
	pathReferenceSearch   uiPathReferenceSearch
	pathReference         uiPathReferenceState
}

type uiPresentationFeatureState struct {
	theme                            string
	activeSurface                    uiSurface
	altScreenActive                  bool
	terminalFocus                    *terminalFocusState
	terminalCursor                   *uiTerminalCursorState
	rendererOutputGate               *uiRendererOutputGateState
	nativeSurface                    *uiNativeSurface
	nativeLiveAreaError              error
	nativeImmutableTranscriptWritten bool
	nativePendingEmissions           []nativePendingEmission
	nativeScratchHydrationPending    bool
	nativeAssistantStreamIncomplete  bool
	nativeResizeRehydrateToken       uint64
	nativeResizeRehydrateSettled     bool
	nativeResizeRehydrateActive      bool
	termWidth                        int
	termHeight                       int
	windowSizeKnown                  bool
	helpVisible                      bool
	startupCmds                      []tea.Cmd
	uiMainThread                     uiMainThreadState
}

type uiConversationFeatureState struct {
	interaction                        uiInteractionState
	ask                                uiAskState
	initialTranscript                  []UITranscriptEntry
	startupSubmit                      string
	startupSubmitPromptHistoryRecorded bool
}

type uiSessionTransitionFeatureState struct {
	exitAction                              UIAction
	nextSessionInitialPrompt                string
	nextSessionInitialPromptHistoryRecorded bool
	nextSessionInitialInput                 string
	nextSessionID                           string
	nextForkRollbackTargetID                string
	nextParentSessionID                     string
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
	clipboardImagePaster        uiClipboardImagePaster
	clipboardTextCopier         uiClipboardTextCopier

	transientStatus         string
	transientStatusKind     uiStatusNoticeKind
	transientStatusNoticeID string
	transientStatusToken    uint64
	transientStatusQueue    []uiStatusNotice
	localNoticeSequence     uint64
	startupUpdateNotice     bool
	startupUpdateShown      bool
	debugKeys               bool
	debugMode               bool
	transcriptDiagnostics   bool
}

type uiTranscriptFeatureState struct {
	sawAssistantDelta                bool
	activeAssistantStreamSource      string
	activeAssistantStreamIdentity    uiAssistantStreamIdentity
	lastCommittedAssistantStepID     string
	transcriptEntries                []tui.TranscriptEntry
	transcriptBaseOffset             int
	transcriptTotalEntries           int
	transcriptRevision               int64
	deferredCommittedTail            []deferredProjectedTranscriptTail
	runtimeConnection                clientui.RuntimeConnectionLifecycle
	transcriptLiveDirty              bool
	reasoningLiveDirty               bool
	detailTranscript                 uiDetailTranscriptWindow
	runtimeMainViewToken             uint64
	runtimeMainViewBusy              bool
	runtimeMainViewActiveRequest     runtimeMainViewRefreshRequest
	runtimeMainViewPendingSet        bool
	runtimeMainViewPending           runtimeMainViewRefreshRequest
	runtimeTranscriptToken           uint64
	runtimeTranscriptRetry           uint64
	runtimeTranscriptBusy            bool
	runtimeTranscriptActiveRequest   runtimeTranscriptSyncRequest
	runtimeTranscriptPendingSet      bool
	runtimeTranscriptPending         runtimeTranscriptSyncRequest
	pendingQueuedDrainAfterHydration bool
	queuedDrainReadyAfterHydration   bool
	waitRuntimeEventAfterHydration   bool
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
	worktrees uiWorktreeOverlayState
}
