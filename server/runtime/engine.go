package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"core/server/chatcontext"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/jsoncontract"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

const (
	interruptMessage                   = "User interrupted you"
	agentsFileName                     = "AGENTS.md"
	agentsGlobalDirName                = config.ConfigDirName
	systemPromptFileName               = "SYSTEM.md"
	agentsInjectedHeader               = "# Project context and authoritative instructions from the ./AGENTS.md file:"
	agentsInjectedFenceLabel           = "md"
	environmentInjectedHeader          = "# Info about environment:"
	missingAssistantPhaseWarning       = "You sent a message without specifying a channel/phase. It was treated as commentary. If you finished your work and intended to end your turn, use the final channel explicitly. Otherwise continue and use the commentary channel for progress updates with tool calls."
	commentaryWithoutToolCallsWarning  = "You sent a commentary-channel message without tool calls. This is wrong. If you intend to keep working, include tool calls with commentary updates. If you are done, send a final-channel message with no tool calls."
	workflowFinalWithoutContentWarning = "You sent a final-channel message with empty content. Workflow Mode requires a non-empty final answer or a tool-driven continuation. Continue with commentary and tool calls, or provide a valid non-empty final answer."
	reviewerMetaBoundaryMessage        = "End of meta information. Transcript begins starting with next message. Below is NOT YOUR conversation, but another agent's transcript.\n-------"
)

var (
	// ErrModelRequired is returned when engine construction is attempted without a model.
	ErrModelRequired = errors.New("model is required")
	// errUnknownTool is returned when a tool call targets a tool that is not registered.
	errUnknownTool = errors.New("unknown tool")
)

func NormalizeThinkingLevel(level string) (string, bool) {
	return clientui.NormalizeThinkingLevel(level)
}

func NormalizeReviewerFrequency(frequency string) (string, bool) {
	return session.NormalizeReviewerFrequency(frequency)
}

func normalizeCacheWarningMode(mode config.CacheWarningMode) (config.CacheWarningMode, bool) {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "":
		return config.CacheWarningModeDefault, true
	case string(config.CacheWarningModeOff):
		return config.CacheWarningModeOff, true
	case string(config.CacheWarningModeDefault):
		return config.CacheWarningModeDefault, true
	case string(config.CacheWarningModeVerbose):
		return config.CacheWarningModeVerbose, true
	default:
		return "", false
	}
}

type Config struct {
	Model                           string
	Debug                           bool
	Temperature                     float64
	MaxTokens                       int
	ThinkingLevel                   string
	SupportedThinkingValues         []string
	ModelCapabilities               session.LockedModelCapabilities
	FastModeEnabled                 bool
	WebSearchMode                   string
	PromptFacingSnapshotReloader    PromptFacingSnapshotReloader
	ProviderCapabilitiesOverride    *llm.ProviderCapabilities
	EnabledTools                    []toolspec.ID
	SkillPolicy                     config.SkillPolicy
	SubagentCatalogSettings         config.Settings
	SystemPromptFiles               []config.SystemPromptFile
	AutoCompactTokenLimit           int
	WorkflowPreCompactionTokenLimit int
	PreSubmitCompactionLeadTokens   int
	ContextWindowTokens             int
	EffectiveContextWindowPercent   int
	LocalCompactionCarryoverLimit   int
	CompactionMode                  string
	CacheWarningMode                config.CacheWarningMode
	AutoCompactionEnabled           *bool
	QuestionsEnabled                *bool
	Reviewer                        ReviewerConfig
	HeadlessMode                    bool
	ToolPreambles                   bool
	CurrentNodeExecution            *workflowruntime.CurrentNodeExecutionConfig
	WorkflowPrompt                  *workflowruntime.PromptContract
	AskQuestionBatchSkipped         func(tools.AskQuestionBatchMetadata)
	TranscriptWorkingDir            string
	// GlobalConfigDir is the absolute persistence root that owns model-visible
	// global context (global AGENTS.md, system prompt file, skills, generated
	// assets). Empty falls back to ~/.kent so default-root behavior is preserved.
	GlobalConfigDir       string
	OnEvent               func(Event)
	StepLifecycle         StepLifecycleSink
	LifecycleTaskFinished func() error
	LifecycleRuntimeAbort func() error
	DurabilityObserver    ResultGroupDurabilityObserver
}

type ReviewerConfig struct {
	Frequency         string
	Model             string
	ThinkingLevel     string
	ModelCapabilities session.LockedModelCapabilities
	SystemPromptFile  string
	VerboseOutput     bool
	Client            llm.Client
	ClientFactory     func() (llm.Client, error)
}

type ContextUsage struct {
	UsedTokens            int
	WindowTokens          int
	CacheHitPercent       int
	HasCacheHitPercentage bool
}

type Engine struct {
	mu               sync.Mutex
	workflowTerminal WorkflowTerminalState

	lifecycleMu             sync.Mutex
	lifecycleOnce           sync.Once
	lifecycleCtx            context.Context
	lifecycleCancel         context.CancelFunc
	lifecycleWG             sync.WaitGroup
	lifecycleClosed         bool
	closed                  atomic.Bool
	runtimeFIFO             *runtimeOperationFIFO
	pendingWorkSteerOrdinal atomic.Uint64

	store                       *session.Store
	eventLog                    session.MaterializedEventLog
	llm                         *observedModelClient
	registry                    *tools.Registry
	cfg                         Config
	reviewerSuggestionsContract jsoncontract.Structured
	workflowPromptContract      *workflowruntime.CompletionContract
	// outputMutationMu keeps durable transcript writes, runtime projections, and
	// event emission in one order for concurrent steering producers.
	outputMutationMu sync.Mutex
	// queuedUserWorkMu serializes the server-owned continuation that drains
	// pending steering/user injections once a busy run releases.
	queuedUserWorkMu          sync.Mutex
	queuedUserWorkScheduled   bool
	queuedUserWorkCompletion  runtimeDeferred[struct{}]
	queuedUserWorkPauseCount  int
	liveRun                   *liveRunCoordinator
	activeStepGoalMutationsMu sync.Mutex
	activeStepGoalMutations   map[string][]activeStepGoalMutation
	pendingGoalLoopStart      bool
	diagnostics               *diagnosticDedupeStore
	toolCallStarts            *pendingToolCallStartStore

	usageState           *usageTrackingState
	goalLoop             *goalLoopState
	compactionState      *compactionRuntimeState
	handoffState         *handoffRuntimeState
	phaseState           *phaseProtocolState
	reviewerState        *reviewerRuntimeState
	transcriptState      *transcriptRuntimeState
	lockedState          *lockedContractState
	modelRequestsState   *modelRequestRuntimeState
	currentNodeExecution *currentNodeExecutionState
	compactionPlanner    *compactionPlanner
	contextPolicy        chatcontext.Policy
	collaboratorsOnce    sync.Once

	phaseProtocol  phaseProtocolEnforcer
	stepLifecycle  exclusiveStepLifecycle
	backgroundFlow backgroundNoticeScheduler
	compactionFlow contextCompactor
	reviewerFlow   reviewerPipeline
	messageFlow    messageLifecycle
	stepFlow       stepExecutor
	toolFlow       toolExecutor

	// baseMetaInjected guards the single per-conversation injection of base meta
	// context (AGENTS.md, skills, subagents, environment). It is set when a
	// resumed transcript already carries that context, and after the one-time
	// boot injection. It is process-local: the persisted transcript itself is the
	// source of truth across restarts.
	baseMetaInjected bool
}

type handoffRequest struct {
	summarizerPrompt   string
	futureAgentMessage string
}

func New(
	store *session.Store,
	eventLog session.MaterializedEventLog,
	client llm.Client,
	registry *tools.Registry,
	cfg Config,
) (*Engine, error) {
	if store == nil || client == nil || registry == nil {
		return nil, errors.New("store, llm client, and tool registry are required")
	}
	if err := eventLog.ValidateOwner(store); err != nil {
		return nil, fmt.Errorf("runtime event log: %w", err)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrModelRequired
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Temperature == 0 {
		cfg.Temperature = 1
	}
	if cfg.MaxTokens < 0 {
		cfg.MaxTokens = 0
	}
	if cfg.PreSubmitCompactionLeadTokens <= 0 {
		cfg.PreSubmitCompactionLeadTokens = config.DefaultPreSubmitRunwayTokens
	}
	if cfg.LocalCompactionCarryoverLimit <= 0 {
		cfg.LocalCompactionCarryoverLimit = 20_000
	}
	if normalized, ok := normalizeCacheWarningMode(cfg.CacheWarningMode); ok {
		cfg.CacheWarningMode = normalized
	} else {
		return nil, fmt.Errorf("invalid cache_warning_mode %q", cfg.CacheWarningMode)
	}
	if cfg.AutoCompactionEnabled == nil {
		enabled := true
		cfg.AutoCompactionEnabled = &enabled
	}
	cfg.SupportedThinkingValues = slices.Clone(cfg.SupportedThinkingValues)
	var workflowPromptContract *workflowruntime.CompletionContract
	if cfg.WorkflowPrompt != nil {
		prepared, err := newWorkflowPromptCompletionContract(cfg.WorkflowPrompt)
		if err != nil {
			return nil, fmt.Errorf("prepare runtime workflow prompt completion contract: %w", err)
		}
		workflowPromptContract = &prepared
	}
	if !cfg.ModelCapabilities.SupportsReasoningEffort && !cfg.ModelCapabilities.SupportsVisionInputs {
		cfg.ModelCapabilities = llm.LockedModelCapabilitiesForModel(cfg.Model)
	}
	reviewerSuggestionsContract, err := prepareReviewerSuggestionsContract(
		jsoncontract.NewPreparer(cfg.Debug),
	)
	if err != nil {
		return nil, fmt.Errorf("prepare reviewer suggestions contract: %w", err)
	}
	reviewerState := newReviewerRuntimeState(cfg.Reviewer.Client, cfg.Reviewer.ClientFactory)
	cfg.Reviewer.Client = nil
	cfg.Reviewer.ClientFactory = nil
	eng := &Engine{
		store:                       store,
		eventLog:                    eventLog,
		llm:                         newObservedModelClient(client),
		registry:                    registry,
		cfg:                         cfg,
		reviewerSuggestionsContract: reviewerSuggestionsContract,
		workflowPromptContract:      workflowPromptContract,
		diagnostics:                 newDiagnosticDedupeStore(),
		toolCallStarts:              newPendingToolCallStartStore(),
		usageState:                  newUsageTrackingState(),
		goalLoop:                    newGoalLoopState(),
		compactionState:             newCompactionRuntimeState(),
		handoffState:                newHandoffRuntimeState(),
		phaseState:                  newPhaseProtocolState(),
		reviewerState:               reviewerState,
		transcriptState:             newTranscriptRuntimeState(transcriptWorkingDir(cfg.TranscriptWorkingDir, store.Meta().WorkspaceRoot)),
		lockedState:                 newLockedContractState(),
		modelRequestsState:          newModelRequestRuntimeState(),
		currentNodeExecution:        newCurrentNodeExecutionState(),
		compactionPlanner:           newCompactionPlanner(),
	}
	eng.compactionRuntimeState().SetContextFacts(store.ContextFacts())
	providerCapabilities, err := eng.providerCapabilities(context.Background())
	if err != nil {
		return nil, fmt.Errorf("resolve provider capabilities during runtime construction: %w", err)
	}
	eng.cfg.ProviderCapabilitiesOverride = &providerCapabilities
	policySettings := config.Settings{
		ModelContextWindow:               eng.cfg.ContextWindowTokens,
		ContextCompactionThresholdTokens: eng.cfg.AutoCompactTokenLimit,
		CompactionMode:                   config.CompactionMode(eng.cfg.CompactionMode),
	}
	eng.contextPolicy = chatcontext.ResolvePolicy(policySettings, providerCapabilities, store.Meta().Locked)
	eng.cfg.ContextWindowTokens = int(eng.contextPolicy.ContextWindowTokens)
	eng.cfg.AutoCompactTokenLimit = int(eng.contextPolicy.AutomaticThresholdTokens)
	eng.cfg.CompactionMode = eng.compactionPlannerState().mode(eng.contextPolicy)
	eng.ensureLifecycle()
	eng.ensureOrchestrationCollaborators()

	reviewerFrequency, ok := NormalizeReviewerFrequency(eng.cfg.Reviewer.Frequency)
	if !ok {
		reviewerFrequency = "off"
	}
	eng.cfg.Reviewer.Frequency = reviewerFrequency
	if reviewerFrequency != "off" {
		if err := eng.initReviewerClient(); err != nil {
			return nil, err
		}
	}

	meta := store.Meta()
	if meta.Locked != nil {
		if meta.Locked.ContextWindow <= 0 || meta.Locked.ContextPercent <= 0 {
			budget := eng.promptContextBudgetFromConfig()
			if err := store.BackfillLockedContextBudget(budget.window, budget.percent); err != nil {
				return nil, err
			}
			meta = store.Meta()
		}
		if strings.TrimSpace(meta.Locked.ProviderContract.ProviderID) == "" {
			caps, err := eng.providerCapabilities(context.Background())
			if err != nil {
				return nil, fmt.Errorf("resolve provider capabilities for locked contract: %w", err)
			}
			if err := store.BackfillLockedProviderContract(llm.LockedProviderCapabilitiesFromContract(caps)); err != nil {
				return nil, err
			}
			meta = store.Meta()
		}
		copyLocked := *meta.Locked
		eng.lockedContractState().Set(copyLocked)
	}

	if err := eng.restoreMessages(); err != nil {
		return nil, err
	}
	// Restoring messages is the runtime's first event-use boundary. Existing
	// stores reconcile event-derived metadata there, so subsequent metadata
	// reads must use the refreshed authoritative snapshot.
	meta = store.Meta()
	_, err = eng.repairMissingToolOutputsByAppending(
		nil,
		missingToolOutputRepairFreshResource,
	)
	if err != nil {
		return nil, fmt.Errorf("repair missing tool outputs during runtime construction: %w", err)
	}
	if dangling := eng.pendingRecoveryDanglingToolCallIDs(); len(dangling) > 0 {
		return nil, fmt.Errorf("runtime construction retained %d dangling tool call(s) after repair", len(dangling))
	}
	eng.restorePersistedUsageState(meta.UsageState)
	eng.runtimeFIFO = newRuntimeOperationFIFO()
	return eng, nil
}

func (e *Engine) pendingRecoveryDanglingToolCallIDs() map[string]struct{} {
	out := map[string]struct{}{}
	chat := e.transcriptRuntimeState().chatProjection()
	if chat == nil {
		return out
	}
	for _, call := range chat.danglingToolCalls() {
		callID := strings.TrimSpace(call.callID)
		if callID != "" {
			out[callID] = struct{}{}
		}
	}
	return out
}

func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.ensureLifecycle()
	interruptErr := e.Interrupt()
	e.lifecycleMu.Lock()
	if e.lifecycleClosed {
		e.lifecycleMu.Unlock()
		return interruptErr
	}
	e.lifecycleClosed = true
	e.closed.Store(true)
	e.runtimeFIFO.beginClose()
	cancel := e.lifecycleCancel
	e.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.lifecycleWG.Wait()
	e.runtimeFIFO.Close()
	abortErr := e.steerLifecycleClose(steerLiveToolAbortIntent("canceled"))
	return errors.Join(interruptErr, abortErr)
}

func (e *Engine) BeginRetirement() bool {
	if e == nil {
		return true
	}
	e.ensureOrchestrationCollaborators()
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.lifecycleClosed || e.closed.Load() {
		return true
	}
	if e.stepLifecycle.IsBusy() ||
		e.HasQueuedUserWork() ||
		e.HasScheduledQueuedUserWork() ||
		e.CurrentNodeExecutionConfigured() ||
		e.ReviewerActive() ||
		!e.runtimeFIFO.beginCloseIfIdle() {
		return false
	}
	e.closed.Store(true)
	return true
}

func (e *Engine) closeAdmissionAfterRuntimeAbort() {
	if e == nil {
		return
	}
	e.closed.Store(true)
	e.runtimeFIFO.beginClose()
}

func (e *Engine) closeAndRetireAfterRuntimeAbort() {
	if e == nil {
		return
	}
	retire := e.registerRuntimeAbortRetirement()
	e.closeAdmissionAfterRuntimeAbort()
	if retire != nil {
		go retire()
	}
}

func (e *Engine) registerRuntimeAbortRetirement() func() {
	if e.cfg.LifecycleRuntimeAbort == nil {
		return nil
	}
	e.ensureLifecycle()
	e.lifecycleMu.Lock()
	if e.lifecycleClosed {
		e.lifecycleMu.Unlock()
		return nil
	}
	e.lifecycleWG.Add(1)
	callback := e.cfg.LifecycleRuntimeAbort
	e.lifecycleMu.Unlock()
	return func() {
		// Resource retirement may close this Engine and wait for lifecycle work.
		// Leave the wait group before entering the authoritative resource owner.
		e.lifecycleWG.Done()
		e.surfaceRunError(callback())
	}
}

func (e *Engine) ensureLifecycle() {
	if e == nil {
		return
	}
	e.lifecycleOnce.Do(func() {
		e.lifecycleCtx, e.lifecycleCancel = context.WithCancel(context.Background())
	})
}

func (e *Engine) launchLifecycleTask(task func(context.Context) *resultGroupFatal) bool {
	return e.launchLifecycleTaskWithCompletion(task, nil)
}

func (e *Engine) launchLifecycleTaskWithCompletion(
	task func(context.Context) *resultGroupFatal,
	completed func(),
) bool {
	if e == nil || task == nil {
		return false
	}
	if e.closed.Load() {
		return false
	}
	e.ensureLifecycle()
	e.lifecycleMu.Lock()
	if e.lifecycleClosed || e.closed.Load() {
		e.lifecycleMu.Unlock()
		return false
	}
	e.lifecycleWG.Add(1)
	ctx := e.lifecycleCtx
	e.lifecycleMu.Unlock()
	go func(ctx context.Context) {
		var runtimeAbort *resultGroupFatal
		defer func() {
			// Retirement may synchronously close this Engine and wait for lifecycle
			// tasks, so this task must leave the wait group before callbacks run.
			e.lifecycleWG.Done()
			if e.cfg.LifecycleTaskFinished != nil {
				e.surfaceRunError(e.cfg.LifecycleTaskFinished())
			}
			if runtimeAbort != nil && e.cfg.LifecycleRuntimeAbort != nil {
				e.surfaceRunError(e.cfg.LifecycleRuntimeAbort())
			}
			if completed != nil {
				completed()
			}
		}()
		runtimeAbort = task(ctx)
	}(ctx)
	return true
}

type QueuedUserMessage struct {
	ID      string
	Message llm.Message
}

func (e *Engine) QueueUserMessage(ctx context.Context, text string) (QueuedUserMessage, error) {
	return e.queueUserMessage(ctx, text, false, nil)
}

func (e *Engine) queueUserMessage(ctx context.Context, text string, forceAutoDrain bool, accept CommandAcceptance) (QueuedUserMessage, error) {
	return awaitEngineRuntimeOperation(ctx, e, func(context.Context) (QueuedUserMessage, error) {
		return e.queueUserMessageRaw(text, forceAutoDrain, accept)
	})
}

func (e *Engine) queueUserMessageRaw(text string, forceAutoDrain bool, accept CommandAcceptance) (QueuedUserMessage, error) {
	e.ensureOrchestrationCollaborators()
	if err := e.requirePendingWorkCapacity(); err != nil {
		return QueuedUserMessage{}, err
	}
	if !forceAutoDrain {
		var item QueuedUserMessage
		committed, err := runCommandAcceptance(accept, func() (bool, error) {
			e.outputMutationMu.Lock()
			queued, queueErr := e.messageFlow.QueueUserMessage(text)
			if queueErr == nil {
				item = queued
				e.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
			}
			e.outputMutationMu.Unlock()
			if queueErr != nil {
				return false, queueErr
			}
			e.publishPendingWorkChanged()
			return true, nil
		})
		if err := commandAcceptanceResult(committed, err); err != nil {
			return QueuedUserMessage{}, err
		}
		return item, nil
	}
	liveItem := QueuedUserMessage{
		ID:      runtimeids.NewQueueItemID().String(),
		Message: llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)},
	}
	for {
		var item QueuedUserMessage
		livePublication := false
		committed, err := runCommandAcceptance(accept, func() (bool, error) {
			if !e.liveRun.beginQueueItemPublication(mustQueueItemID(liveItem.ID)) {
				return false, nil
			}
			livePublication = true
			admission := e.nextPendingWorkSteerAdmission()
			e.outputMutationMu.Lock()
			queuedItem, queueErr := e.messageFlow.QueueUserMessageWithID(liveItem, queuedUserMessageAssociation{
				steerAdmission: admission,
			})
			if queueErr == nil {
				item = queuedItem
				e.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
			}
			e.outputMutationMu.Unlock()
			if queueErr != nil {
				queueItemID := mustQueueItemID(liveItem.ID)
				e.liveRun.finishQueueItemPublication(queueItemID)
				e.completeLiveRunQueueItems(map[string]struct{}{liveItem.ID: {}})
				return false, queueErr
			}
			e.publishPendingWorkChanged()
			return true, nil
		})
		if err != nil {
			return QueuedUserMessage{}, err
		}
		if committed {
			queueItemID := mustQueueItemID(item.ID)
			if e.liveRun.finishQueueItemPublication(queueItemID) {
				e.failStoppedLiveRunQueueItems(map[runtimeids.QueueItemID]struct{}{queueItemID: {}})
			} else {
				e.scheduleQueuedUserInjectionsIfIdle()
			}
			return item, nil
		}
		if livePublication {
			return QueuedUserMessage{}, context.Canceled
		}
		if e.waitingForLiveRunStepStart() {
			if accept != nil {
				return QueuedUserMessage{}, context.Canceled
			}
			e.outputMutationMu.Lock()
			e.emitInterruptedHumanInputs([]QueuedUserMessage{liveItem})
			e.outputMutationMu.Unlock()
			return liveItem, nil
		}
		break
	}
	var item QueuedUserMessage
	committed, err := runCommandAcceptance(accept, func() (bool, error) {
		admission := e.nextPendingWorkSteerAdmission()
		e.outputMutationMu.Lock()
		queued, queueErr := e.messageFlow.QueueUserMessage(text, queuedUserMessageAssociation{
			steerAdmission: admission,
		})
		if queueErr == nil {
			item = queued
			e.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
		}
		e.outputMutationMu.Unlock()
		if queueErr != nil {
			return false, queueErr
		}
		e.publishPendingWorkChanged()
		return true, nil
	})
	if err := commandAcceptanceResult(committed, err); err != nil {
		return QueuedUserMessage{}, err
	}
	e.scheduleQueuedUserInjectionsIfIdle()
	return item, nil
}

func (e *Engine) waitingForLiveRunStepStart() bool {
	if e == nil || e.stepLifecycle == nil {
		return false
	}
	snapshot := e.stepLifecycle.Snapshot()
	if snapshot == nil {
		return false
	}
	return activeKindUsesLiveRun(snapshot.ActiveKind)
}

func activeKindUsesLiveRun(kind ActiveKind) bool {
	switch kind {
	case ActiveKindCompaction, ActiveKindPreSubmitCompaction, ActiveKindRuntimeMaintenance:
		return false
	default:
		return true
	}
}

func activeKindInterruptibleByLiveStop(kind ActiveKind) bool {
	return kind.Valid() && kind != ActiveKindRuntimeMaintenance
}

func (e *Engine) Interrupt() error {
	e.ensureOrchestrationCollaborators()
	goalLoopInterruptPending := false
	interrupted, err := e.stepLifecycle.InterruptCurrent(func(snapshot *RunSnapshot) {
		if e.goalActive() && snapshot != nil && snapshot.ActiveKind == ActiveKindGoalLoop {
			e.goalLoopState().MarkInterruptPending()
			goalLoopInterruptPending = true
		}
	})
	if err != nil {
		if goalLoopInterruptPending {
			e.goalLoopState().ClearInterruptPending()
		}
		return err
	}
	if goalLoopInterruptPending {
		if e.goalActive() && interrupted != nil && interrupted.ActiveKind == ActiveKindGoalLoop {
			e.goalLoopState().CommitInterrupt()
		} else {
			e.goalLoopState().ClearInterruptPending()
		}
	}
	return nil
}

func (e *Engine) PersistInterruption() error {
	return e.steerInterruption(steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeInterruption),
			Content:     textutil.Value(interruptMessage),
		}},
	))
}

func (e *Engine) SubmitUserMessage(ctx context.Context, text string) (assistant llm.Message, err error) {
	return e.submitUserMessage(ctx, text, nil, nil, nil)
}

func (e *Engine) SubmitUserMessageWithFlushHook(ctx context.Context, text string, onFlushed func()) (assistant llm.Message, err error) {
	return e.submitUserMessage(ctx, text, nil, onFlushed, nil)
}

func (e *Engine) SubmitUserMessageWithHooks(ctx context.Context, text string, onActive func(), onFlushed func()) (assistant llm.Message, err error) {
	return e.submitUserMessage(ctx, text, onActive, onFlushed, nil)
}

func (e *Engine) SubmitUserMessageWithOutcomeWithHooks(ctx context.Context, text string, onActive func(), onFlushed func()) (UserTurnResult, error) {
	return e.submitUserMessageWithOutcome(ctx, text, onActive, onFlushed, nil)
}

func (e *Engine) SubmitAgentSteerWithHooks(ctx context.Context, steer AgentSteer, onActive func(), onFlushed func()) (assistant llm.Message, err error) {
	if e.closed.Load() {
		return llm.Message{}, ErrEngineClosed
	}
	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
		if onActive != nil {
			onActive()
		}
		if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}
		if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventDefault, true, []llm.Message{steer.Message()})); err != nil {
			return err
		}
		if onFlushed != nil {
			onFlushed()
		}
		msg, runErr := e.runStepLoop(stepCtx, stepID)
		assistant = msg
		return runErr
	})
	e.surfaceRunError(err)
	return assistant, err
}

func (e *Engine) submitUserMessage(ctx context.Context, text string, onActive func(), onFlushed func(), accept CommandAcceptance) (assistant llm.Message, err error) {
	outcome, err := e.submitUserMessageWithOutcome(ctx, text, onActive, onFlushed, accept)
	if outcome.FinalAnswer != nil {
		assistant = *outcome.FinalAnswer
	}
	return assistant, err
}

func (e *Engine) submitUserMessageWithOutcome(ctx context.Context, text string, onActive func(), onFlushed func(), accept CommandAcceptance) (outcome UserTurnResult, err error) {
	if text == "" {
		return UserTurnResult{}, errors.New("empty message")
	}
	if e.closed.Load() {
		return UserTurnResult{}, ErrEngineClosed
	}

	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
		if onActive != nil {
			onActive()
		}
		if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}
		userMessage := llm.Message{Role: llm.RoleUser, Content: textutil.Value(text)}
		committed, steerErr := runCommandAcceptance(accept, func() (bool, error) {
			receipt, err := e.steerWithCommitReceipt(stepID, steerUserMessageWithFlushIntent(userMessage))
			return receipt.Committed, err
		})
		if err := commandAcceptanceResult(committed, steerErr); err != nil {
			return err
		}
		if onFlushed != nil {
			onFlushed()
		}
		result, runErr := e.runStepLoopWithPendingUserInjectionOutcomeObserver(stepCtx, stepID, nil)
		outcome = userTurnResultFromStepLoop(result)
		return runErr
	})
	e.surfaceRunError(err)
	return outcome, err
}

func (e *Engine) SubmitWorkflowTurn(ctx context.Context) (result WorkflowTurnResult, err error) {
	if !e.currentNodeExecutionActive() {
		return WorkflowTurnResult{}, errors.New("workflow turn requires an active Current Node execution")
	}
	if e.closed.Load() {
		return WorkflowTurnResult{}, ErrEngineClosed
	}

	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.RunNext(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindWorkflowTurn}, func(stepCtx context.Context, stepID string) error {
		if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}
		msg, runErr := e.runStepLoop(stepCtx, stepID)
		result.Assistant = msg
		return runErr
	})
	if terminal := e.WorkflowTerminalState(); terminal.Completed {
		completion := terminal.Completion
		result.Completion = &completion
	}
	e.surfaceRunError(err)
	return result, err
}

func (e *Engine) SubmitUserShellCommand(ctx context.Context, command string) (result tools.Result, err error) {
	return e.submitUserShellCommand(ctx, command, nil, nil)
}

func (e *Engine) SubmitUserShellCommandWithActiveHook(ctx context.Context, command string, onActive func()) (result tools.Result, err error) {
	return e.submitUserShellCommand(ctx, command, onActive, nil)
}

func (e *Engine) SubmitUserShellCommandWithAcceptance(ctx context.Context, command string, accept CommandAcceptance) (result tools.Result, err error) {
	return e.submitUserShellCommand(ctx, command, nil, accept)
}

func (e *Engine) submitUserShellCommand(ctx context.Context, command string, onActive func(), accept CommandAcceptance) (result tools.Result, err error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return tools.Result{}, errors.New("empty command")
	}
	terminal, err := awaitEngineRuntimeOperation(ctx, e, func(context.Context) (runtimeDeferred[tools.Result], error) {
		deferred := newRuntimeDeferred[tools.Result]()
		launched := e.launchLifecycleTask(func(lifecycleCtx context.Context) *resultGroupFatal {
			result, runErr := e.runUserShellCommand(lifecycleCtx, command, onActive, accept)
			deferred.complete(result, runErr)
			fatal, abort := resultGroupFatalFromError(runErr)
			if abort {
				return fatal
			}
			return nil
		})
		if !launched {
			deferred.complete(tools.Result{}, ErrEngineClosed)
		}
		return deferred, nil
	})
	if err != nil {
		return tools.Result{}, err
	}
	return terminal.Await(ctx)
}

func (e *Engine) runUserShellCommand(ctx context.Context, command string, onActive func(), accept CommandAcceptance) (result tools.Result, err error) {
	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.RunNext(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserShell}, func(stepCtx context.Context, stepID string) error {
		if onActive != nil {
			onActive()
		}
		if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}

		call := llm.ToolCall{
			ID:   uuid.NewString(),
			Name: string(toolspec.ToolExecCommand),
			Input: mustJSON(map[string]any{
				"cmd":            command,
				"user_initiated": true,
			}),
		}
		committed, steerErr := runCommandAcceptance(accept, func() (bool, error) {
			receipt, err := e.steerWithCommitReceipt(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}}))
			return receipt.Committed, err
		})
		if err := commandAcceptanceResult(committed, steerErr); err != nil {
			return err
		}
		_, registered := e.registry.Get(toolspec.ToolExecCommand)
		results, execErr := e.executeToolCalls(stepCtx, stepID, []llm.ToolCall{call})
		if len(results) == 0 {
			return errors.Join(execErr, errors.New("shell tool execution returned no result"))
		}
		result = results[0]
		if !registered {
			return errors.Join(execErr, errUnknownTool)
		}
		return execErr
	})
	return result, err
}

func (e *Engine) runStepLoop(ctx context.Context, stepID string) (llm.Message, error) {
	return e.runStepLoopWithPendingUserInjectionObserver(ctx, stepID, nil)
}

func (e *Engine) runStepLoopWithPendingUserInjectionObserver(ctx context.Context, stepID string, onQueuedUserFlushCommitted func(session.CommitReceipt)) (llm.Message, error) {
	result, err := e.runStepLoopWithPendingUserInjectionOutcomeObserver(ctx, stepID, onQueuedUserFlushCommitted)
	if result.FinalAnswer == nil {
		return llm.Message{}, err
	}
	return *result.FinalAnswer, err
}

func (e *Engine) runStepLoopWithPendingUserInjectionOutcomeObserver(ctx context.Context, stepID string, onQueuedUserFlushCommitted func(session.CommitReceipt)) (stepLoopResult, error) {
	reviewerFrequency := e.ReviewerFrequency()
	reviewerClient := e.reviewerRuntimeState().Client()
	result, err := e.runStepLoopWithQueuedUserFlushObserver(ctx, stepID, reviewerFrequency, reviewerClient, true, onQueuedUserFlushCommitted)
	outcome := userTurnResultFromStepLoop(result)
	if outcome.Kind == UserTurnResultAssistantFinal && outcome.FinalAnswer != nil {
		e.recordLiveRunAssistantFinalAnswer(stepID, *outcome.FinalAnswer)
	}
	return result, err
}

// runStepLoopWithOptions executes a single assistant/tool loop.
// reviewerFrequency/reviewerClient are used as the baseline reviewer policy for
// this run. When refreshReviewerConfigOnResolve is true, the final assistant
// resolution re-reads current runtime reviewer config so busy-time toggles (for
// example from /supervisor) affect the currently running step at completion.
func (e *Engine) runStepLoopWithOptions(ctx context.Context, stepID string, reviewerFrequency string, reviewerClient *observedModelClient, refreshReviewerConfigOnResolve bool) (stepLoopResult, error) {
	return e.runStepLoopWithQueuedUserFlushObserver(ctx, stepID, reviewerFrequency, reviewerClient, refreshReviewerConfigOnResolve, nil)
}

func (e *Engine) runStepLoopWithQueuedUserFlushObserver(ctx context.Context, stepID string, reviewerFrequency string, reviewerClient *observedModelClient, refreshReviewerConfigOnResolve bool, onQueuedUserFlushCommitted func(session.CommitReceipt)) (stepLoopResult, error) {
	e.ensureOrchestrationCollaborators()
	return e.stepFlow.RunStepLoopWithOptions(ctx, stepID, stepLoopOptions{
		ReviewerFrequency:              reviewerFrequency,
		ReviewerClient:                 reviewerClient,
		RefreshReviewerConfigOnResolve: refreshReviewerConfigOnResolve,
		OnQueuedUserFlushCommitted:     onQueuedUserFlushCommitted,
	})
}

func (e *Engine) ensureLocked() (session.LockedContract, error) {
	if locked, ok := e.lockedContractState().Snapshot(); ok {
		return locked, nil
	}
	var providerContract llm.ProviderCapabilities
	hasProviderContract := false
	if e.cfg.ProviderCapabilitiesOverride != nil {
		providerContract = *e.cfg.ProviderCapabilitiesOverride
		hasProviderContract = true
	} else if e.llm != nil {
		if caps, err := e.llm.capabilities(context.Background()); err == nil {
			providerContract = caps
			hasProviderContract = true
		}
	}

	contextBudget := e.promptContextBudgetFromConfig()
	lock := session.LockedContract{
		Model:             e.cfg.Model,
		Temperature:       e.cfg.Temperature,
		MaxOutputToken:    e.cfg.MaxTokens,
		ContextWindow:     contextBudget.window,
		ContextPercent:    contextBudget.percent,
		EnabledTools:      toolspec.IDStrings(e.cfg.EnabledTools),
		WebSearchMode:     strings.TrimSpace(e.cfg.WebSearchMode),
		ModelCapabilities: e.cfg.ModelCapabilities,
		ToolPreambles: func() *bool {
			enabled := !e.cfg.HeadlessMode && e.cfg.ToolPreambles
			return &enabled
		}(),
	}
	if prompt, configured := e.workflowPrompt(); configured {
		mode, err := workflowruntime.ParseCompletionMode(string(prompt.CompletionMode))
		if err != nil {
			return session.LockedContract{}, err
		}
		lock.WorkflowCompletionMode = &mode
	}
	if hasProviderContract {
		lock.ProviderContract = llm.LockedProviderCapabilitiesFromContract(providerContract)
	}
	systemPrompt, err := e.buildSystemPromptSnapshotForRoot(lock, e.systemPromptWorkspaceRootLocked())
	if err != nil {
		return session.LockedContract{}, err
	}
	lock.SystemPrompt = systemPrompt
	lock.HasSystemPrompt = true
	if err := e.store.MarkModelDispatchLocked(lock); err != nil {
		return session.LockedContract{}, err
	}
	e.lockedContractState().Set(lock)
	return lock, nil
}

// generateWithMissingToolOutputRepair runs a model turn, and on a provider HTTP
// 400 attempts an append-only repair that closes any interrupted tool calls that
// lack outputs, then rebuilds the request and retries. The request is rebuilt
// each iteration so the retry observes the appended synthetic outputs. When the
// 400 is unrelated to missing outputs (nothing to repair), the original error is
// returned unchanged.
func (e *Engine) generateWithMissingToolOutputRepair(ctx context.Context, stepID string, rebuild func() (llm.Request, error), onDelta func(llm.AssistantDelta), onReasoningDelta func(llm.ReasoningSummaryDelta), onAttemptReset func()) (successfulRequestCandidate, error) {
	for {
		req, err := rebuild()
		if err != nil {
			return successfulRequestCandidate{}, err
		}
		var emitted atomic.Bool
		wrappedDelta := onDelta
		if onDelta != nil {
			wrappedDelta = func(delta llm.AssistantDelta) {
				if delta.Text != "" {
					emitted.Store(true)
				}
				onDelta(delta)
			}
		}
		wrappedReasoningDelta := onReasoningDelta
		if onReasoningDelta != nil {
			wrappedReasoningDelta = func(delta llm.ReasoningSummaryDelta) {
				if strings.TrimSpace(delta.Text) != "" {
					emitted.Store(true)
				}
				onReasoningDelta(delta)
			}
		}
		resp, err := e.generateWithRetryClient(ctx, stepID, e.llm, req, wrappedDelta, wrappedReasoningDelta, onAttemptReset)
		if err == nil {
			return newSuccessfulRequestCandidate(req, resp), nil
		}
		if !llm.HasHTTPStatus(err, 400) {
			return successfulRequestCandidate{}, err
		}
		if emitted.Load() && onAttemptReset != nil {
			onAttemptReset()
		}
		repaired, repairErr := e.repairMissingToolOutputsByAppending(
			textutil.OptionalTrimmedString(stepID),
			missingToolOutputRepairLiveProvider400,
		)
		if repairErr != nil {
			return successfulRequestCandidate{}, errors.Join(err, repairErr)
		}
		if repaired == 0 {
			return successfulRequestCandidate{}, err
		}
	}
}

func (e *Engine) generateWithRetryClient(ctx context.Context, stepID string, client *observedModelClient, req llm.Request, onDelta func(llm.AssistantDelta), onReasoningDelta func(llm.ReasoningSummaryDelta), onAttemptReset func()) (llm.Response, error) {
	observed, err := e.prepareCacheObservedRequest(stepID, req, cacheResponseObservationExactStep)
	if err != nil {
		return llm.Response{}, err
	}
	publishedProviderDiagnostics := make(map[llm.CodexTurnStateDiagnosticCategory]struct{}, 2)
	resp, err := generateWithRetryClient(
		ctx,
		client,
		observed,
		onDelta,
		onReasoningDelta,
		onAttemptReset,
		func() {
			e.publishProviderTurnStateDiagnostics(stepID, req.CodexDispatch, publishedProviderDiagnostics)
		},
	)
	if err != nil {
		return llm.Response{}, err
	}
	return resp, nil
}

func generateWithRetryClient(
	ctx context.Context,
	client *observedModelClient,
	observed cacheObservedRequest,
	onDelta func(llm.AssistantDelta),
	onReasoningDelta func(llm.ReasoningSummaryDelta),
	onAttemptReset func(),
	onAttemptFinished func(),
) (llm.Response, error) {
	var lastErr error
	for i := 0; ; i++ {
		var (
			resp                    llm.Response
			attemptErr              error
			attemptEmitted          bool
			reasoningEmitted        bool
			attemptOnDelta          func(llm.AssistantDelta)
			attemptOnReasoningDelta func(llm.ReasoningSummaryDelta)
			attemptDone             atomic.Bool
		)
		if onDelta != nil {
			attemptOnDelta = func(delta llm.AssistantDelta) {
				if attemptDone.Load() {
					return
				}
				if delta.Text == "" {
					return
				}
				attemptEmitted = true
				onDelta(delta)
			}
		}
		if onReasoningDelta != nil {
			attemptOnReasoningDelta = func(delta llm.ReasoningSummaryDelta) {
				if attemptDone.Load() {
					return
				}
				if strings.TrimSpace(delta.Text) == "" {
					return
				}
				reasoningEmitted = true
				onReasoningDelta(delta)
			}
		}
		resp, attemptErr = client.generateObserved(
			ctx,
			observed,
			llm.StreamCallbacks{
				OnAssistantDelta:        attemptOnDelta,
				OnReasoningSummaryDelta: attemptOnReasoningDelta,
			},
			func() {
				attemptDone.Store(true)
				if onAttemptFinished != nil {
					onAttemptFinished()
				}
			},
		)
		if attemptErr != nil && ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		if attemptErr == nil {
			return resp, nil
		}
		var observationErr *cacheObservationDispatchError
		if errors.As(attemptErr, &observationErr) {
			return llm.Response{}, attemptErr
		}
		resetAttempt := func() {
			if (attemptEmitted || reasoningEmitted) && onAttemptReset != nil {
				onAttemptReset()
			}
		}
		if errors.Is(attemptErr, context.Canceled) || errors.Is(attemptErr, context.DeadlineExceeded) {
			resetAttempt()
			return llm.Response{}, attemptErr
		}
		if llm.IsNonRetriableModelError(attemptErr) || llm.IsContextLengthOverflowError(attemptErr) {
			if !llm.HasHTTPStatus(attemptErr, 400) {
				resetAttempt()
			}
			return llm.Response{}, attemptErr
		}
		resetAttempt()
		lastErr = attemptErr
		delays := generateRetryDelays
		if errors.Is(attemptErr, llm.ErrModelStreamStalled) {
			delays = idleStallRetryDelays
		}
		if i >= len(delays) {
			break
		}
		if err := rpcwire.WaitForRetry(ctx, delays[i]); err != nil {
			return llm.Response{}, err
		}
	}
	return llm.Response{}, fmt.Errorf("model generation failed after retries: %w", lastErr)
}

func (e *Engine) executeToolCalls(ctx context.Context, stepID string, calls []llm.ToolCall) ([]tools.Result, error) {
	accepted := acceptedResponseCalls{
		local: append([]llm.ToolCall(nil), calls...),
		order: make([]acceptedResponseCallRef, len(calls)),
	}
	for index := range calls {
		accepted.order[index] = acceptedResponseCallRef{
			source: acceptedResponseCallLocal,
			index:  index,
		}
	}
	return e.executeAcceptedToolCalls(ctx, stepID, accepted)
}

func (e *Engine) executeAcceptedToolCalls(
	ctx context.Context,
	stepID string,
	calls acceptedResponseCalls,
) ([]tools.Result, error) {
	results, _, err := e.executeAcceptedToolCallsCoordinated(ctx, stepID, calls)
	return results, err
}

func (e *Engine) executeAcceptedToolCallsCoordinated(
	ctx context.Context,
	stepID string,
	calls acceptedResponseCalls,
) ([]tools.Result, bool, error) {
	e.ensureOrchestrationCollaborators()
	prepared, err := prepareExecutorToolCalls(
		e,
		stepID,
		activeRunIDForStep(e, stepID),
		e.currentNodeExecutionActive(),
		calls.local,
	)
	if err != nil {
		return nil, false, err
	}
	executionCalls := calls
	executionCalls.local = make([]llm.ToolCall, len(prepared))
	for index := range prepared {
		executionCalls.local[index] = prepared[index].call
	}
	collector, err := newResultGroupCollector(resultGroupRosterFromAcceptedCalls(executionCalls))
	if err != nil {
		return nil, false, err
	}
	abortBeforeLocalExecution := func(cause error) ([]tools.Result, bool, error) {
		fatal := e.abortResultGroupForOperationalFailure(
			stepID,
			collector,
			cause,
		)
		postJoin, coordinateErr := e.coordinateAcceptedResponsePostJoin(
			stepID,
			prepared,
			collector,
			fatal,
		)
		return postJoin.results, false, coordinateErr
	}
	for _, ref := range executionCalls.order {
		if ref.source != acceptedResponseCallHosted {
			continue
		}
		hosted := executionCalls.hosted[ref.index]
		normalized, normalizeErr := normalizeToolCallForTranscriptChecked(
			hosted.Call,
			e.transcriptWorkingDir(),
		)
		if normalizeErr != nil {
			return abortBeforeLocalExecution(fmt.Errorf(
				"normalize hosted tool call presentation (call_id=%s tool=%s): %w",
				hosted.Call.ID,
				hosted.Call.Name,
				normalizeErr,
			))
		}
		if err := e.steer(stepID, steerEventIntent(Event{
			Kind:                       EventToolCallStarted,
			StepID:                     exactStepIDPointer(stepID),
			ToolCall:                   &normalized,
			CommittedTranscriptChanged: true,
		})); err != nil {
			return abortBeforeLocalExecution(
				fmt.Errorf(
					"persist hosted tool started (call_id=%s tool=%s): %w",
					hosted.Call.ID,
					hosted.Call.Name,
					err,
				),
			)
		}
		var outcome *resultGroupReportOutcome
		if err := e.steer(stepID, steerResultGroupReportIntent(
			collector,
			hosted.Call.ID,
			resultGroupUnit{result: hosted.Result},
			&outcome,
		)); err != nil {
			return abortBeforeLocalExecution(
				fmt.Errorf(
					"report hosted tool result (call_id=%s tool=%s): %w",
					hosted.Call.ID,
					hosted.Call.Name,
					err,
				),
			)
		}
		if fatal := collector.fatalSnapshot(); fatal != nil {
			return abortBeforeLocalExecution(fatal)
		}
		if outcome == nil || *outcome != resultGroupReportAccepted {
			return abortBeforeLocalExecution(
				fmt.Errorf(
					"result group ignored hosted result without fatal (call_id=%s tool=%s)",
					hosted.Call.ID,
					hosted.Call.Name,
				),
			)
		}
	}
	executeErr := e.toolFlow.ExecuteToolCalls(ctx, stepID, prepared, collector)
	postJoin, err := e.coordinateAcceptedResponsePostJoinForOwner(
		ctx,
		stepID,
		prepared,
		collector,
		executeErr,
	)
	if err != nil {
		return postJoin.results, false, err
	}
	return postJoin.results, e.WorkflowTerminalState().Completed, postJoin.semanticErr
}

func (e *Engine) coordinateAcceptedResponsePostJoinForOwner(
	ctx context.Context,
	stepID string,
	prepared []executorToolCall,
	collector *resultGroupCollector,
	executeErr error,
) (acceptedResponsePostJoinOutcome, error) {
	active := e.ActiveRun()
	if active == nil || active.StepID != stepID || active.ActiveKind != ActiveKindUserShell {
		return e.coordinateAcceptedResponsePostJoin(stepID, prepared, collector, executeErr)
	}
	deferred := submitEngineRuntimeOperation(e, func(context.Context) (acceptedResponsePostJoinOutcome, error) {
		return e.coordinateAcceptedResponsePostJoin(stepID, prepared, collector, executeErr)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	outcome, err := deferred.Await(context.WithoutCancel(ctx))
	if err != nil {
		if fatal := collector.fatalSnapshot(); fatal != nil {
			return outcome, errors.Join(fatal, err)
		}
	}
	return outcome, err
}

type acceptedResponsePostJoinOutcome struct {
	results     []tools.Result
	semanticErr error
}

func (e *Engine) coordinateAcceptedResponsePostJoin(
	stepID string,
	prepared []executorToolCall,
	collector *resultGroupCollector,
	executeErr error,
) (acceptedResponsePostJoinOutcome, error) {
	if collector.fatalSnapshot() == nil {
		for _, preparedCall := range prepared {
			call := preparedCall.call
			if _, completed, resultErr := collector.result(call.ID); resultErr != nil {
				e.abortResultGroupForOperationalFailure(stepID, collector, resultErr)
				break
			} else if completed {
				continue
			}
			interrupted := missingToolOutputInterruptedResult(
				call.ID,
				toolspec.ID(call.Name),
			)
			var outcome *resultGroupReportOutcome
			if err := e.steer(stepID, steerResultGroupReportIntent(
				collector,
				call.ID,
				resultGroupUnit{result: interrupted},
				&outcome,
			)); err != nil {
				if fatal := collector.fatalSnapshot(); fatal != nil {
					break
				}
				e.abortResultGroupForOperationalFailure(stepID, collector, fmt.Errorf(
					"semantic close failed to report interrupted tool result (call_id=%s tool=%s): %w",
					call.ID,
					call.Name,
					err,
				))
				break
			}
			if outcome == nil || *outcome != resultGroupReportAccepted {
				e.abortResultGroupForOperationalFailure(stepID, collector, fmt.Errorf(
					"semantic close result group ignored interrupted tool result without fatal (call_id=%s tool=%s outcome=%v)",
					call.ID,
					call.Name,
					outcome,
				))
				break
			}
		}
	}
	closeErr := e.steerRuntimeClose(
		stepID,
		steerResultGroupCloseIntent(collector),
	)
	var goalErr error
	if fatal := collector.fatalSnapshot(); fatal != nil {
		return acceptedResponsePostJoinOutcome{}, fatal
	}
	results, resultsErr := resultGroupResultsForPreparedCalls(collector, prepared)
	if resultsErr != nil {
		return acceptedResponsePostJoinOutcome{}, resultsErr
	}
	if closeErr != nil {
		return acceptedResponsePostJoinOutcome{results: results}, closeErr
	}
	goalErr = e.drainActiveStepGoalMutations(stepID)
	if goalErr != nil {
		return acceptedResponsePostJoinOutcome{results: results}, goalErr
	}
	return acceptedResponsePostJoinOutcome{
		results:     results,
		semanticErr: executeErr,
	}, nil
}

func resultGroupResultsForPreparedCalls(
	collector *resultGroupCollector,
	prepared []executorToolCall,
) ([]tools.Result, error) {
	results := make([]tools.Result, len(prepared))
	for index, preparedCall := range prepared {
		result, found, err := collector.result(preparedCall.call.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf(
				"result group call %q has no completed result after close",
				preparedCall.call.ID,
			)
		}
		results[index] = result
	}
	return results, nil
}
