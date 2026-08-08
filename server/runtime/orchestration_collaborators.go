package runtime

import (
	"context"

	"core/server/llm"
	"core/server/session"
	"core/server/workflowruntime"
)

type exclusiveStepOptions struct {
	EmitRunState bool
	ActiveKind   ActiveKind
}

type exclusiveStepLifecycle interface {
	Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error
	RunNext(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error
	Interrupt() error
	InterruptCurrent(beforeCancel func(*RunSnapshot)) (*RunSnapshot, error)
	IsBusy() bool
	Snapshot() *RunSnapshot
	WithActiveStep(fn func(stepID string) error) (bool, error)
	ApplyForActiveStep(stepID string, apply func() error) error
}

type backgroundAgendaAdapter interface {
	HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) error
	QueueDeveloperNotice(msg llm.Message) error
}

type contextCompactor interface {
	CompactContextWithActiveHook(ctx context.Context, args string, onActive func()) (session.CommitReceipt, error)
	CompactContextForWorkflowContinuation(ctx context.Context) (session.CommitReceipt, error)
	CompactContextForWorkflowPostCompletion(ctx context.Context) workflowruntime.PostCompletionCompactionResult
	CompactContextForPreSubmitWithActiveHook(ctx context.Context, onActive func()) (session.CommitReceipt, error)
	TriggerHandoff(ctx context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error)
	AutoCompactIfNeeded(ctx context.Context, stepID string, mode compactionMode) error
	ShouldCompactBeforeUserMessage(ctx context.Context, text string) (bool, error)
}

type stepLoopOptions struct {
	ReviewerFrequency              string
	ReviewerClient                 llm.Client
	RefreshReviewerConfigOnResolve bool
	OnQueuedUserFlushCommitted     func(session.CommitReceipt)
}

func observeQueuedUserFlushCommit(options stepLoopOptions, receipt session.CommitReceipt) {
	if receipt.Committed && options.OnQueuedUserFlushCommitted != nil {
		options.OnQueuedUserFlushCommitted(receipt)
	}
}

type userInjectionFlushDisposition uint8

const (
	userInjectionFlushContinue userInjectionFlushDisposition = iota
	userInjectionFlushStopped
)

type userInjectionCommitResult struct {
	flushed      int
	receipt      session.CommitReceipt
	queueItemIDs map[string]struct{}
	disposition  userInjectionFlushDisposition
}

type queuedUserFlushStoppedError struct{}

func (*queuedUserFlushStoppedError) Error() string { return "queued user flush stopped" }

type stepLoopResult struct {
	FinalAnswer                *llm.Message
	ExecutedToolCall           bool
	AssistantCommittedStart    int
	AssistantCommittedStartSet bool
}

type stepExecutor interface {
	RunStepLoopWithOptions(ctx context.Context, stepID string, options stepLoopOptions) (stepLoopResult, error)
}

type stepLoopRunner interface {
	RunStepLoopWithOptions(ctx context.Context, stepID string, options stepLoopOptions) (stepLoopResult, error)
}

type toolExecutor interface {
	ExecuteToolCalls(
		ctx context.Context,
		stepID string,
		calls []executorToolCall,
		collector *resultGroupCollector,
	) error
}

type messageLifecycle interface {
	RestoreMessages() error
}

type reviewerPipeline interface {
	ShouldRunTurn(frequency string, reviewerClient llm.Client, patchEditsApplied bool) bool
	RunFollowUp(ctx context.Context, stepID string, original llm.Message, originalCommittedStart int, originalCommittedStartSet bool, reviewerClient llm.Client) (reviewerFollowUpResult, error)
	RunSuggestions(ctx context.Context, stepID string, reviewerClient llm.Client) (reviewerSuggestionsResult, error)
}

type reviewerFollowUpResult struct {
	Message                    llm.Message
	Completion                 *ReviewerStatus
	AssistantCommittedStart    int
	AssistantCommittedStartSet bool
	AssistantEventEmitted      bool
}

type phaseProtocolTurn struct {
	Assistant             llm.Message
	EffectivePhase        *llm.ProviderPhase
	LocalToolCalls        []llm.ToolCall
	HostedToolExecutions  []hostedToolExecution
	EnforcePhaseProtocol  bool
	MissingAssistantPhase bool
}

type phaseProtocolEnforcer interface {
	EnabledForModel(ctx context.Context) bool
	Apply(ctx context.Context, resp llm.Response, assistant llm.Message, localToolCalls []llm.ToolCall, hostedToolExecutions []hostedToolExecution) (phaseProtocolTurn, error)
}

func (e *Engine) ensureOrchestrationCollaborators() {
	e.collaboratorsOnce.Do(func() {
		if e.liveRun == nil {
			e.liveRun = newLiveRunCoordinator(func(result LiveRunResult) {
				e.publishLiveRunFinished(result)
			})
		}
		if e.stepLifecycle == nil {
			e.stepLifecycle = &defaultExclusiveStepLifecycle{engine: e}
		}
		if e.backgroundFlow == nil {
			e.backgroundFlow = &defaultBackgroundAgendaAdapter{engine: e}
		}
		if e.phaseProtocol == nil {
			e.phaseProtocol = &defaultPhaseProtocol{engine: e}
		}
		if e.messageFlow == nil {
			e.messageFlow = newDefaultMessageLifecycle(e)
		}
		if e.toolFlow == nil {
			e.toolFlow = &defaultToolExecutor{engine: e}
		}
		if e.compactionFlow == nil {
			e.compactionFlow = &defaultContextCompactor{engine: e, steps: e.stepLifecycle}
		}
		if e.reviewerFlow == nil {
			e.reviewerFlow = &defaultReviewerPipeline{engine: e}
		}
		if e.stepFlow == nil {
			e.stepFlow = &defaultStepExecutor{
				engine:   e,
				phase:    e.phaseProtocol,
				reviewer: e.reviewerFlow,
				messages: e.messageFlow,
				tools:    e.toolFlow,
			}
		}
		if reviewer, ok := e.reviewerFlow.(*defaultReviewerPipeline); ok && reviewer.stepRunner == nil {
			reviewer.stepRunner = e.stepFlow
		}
	})
}
