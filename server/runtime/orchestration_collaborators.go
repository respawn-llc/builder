package runtime

import (
	"context"

	"core/server/llm"
	"core/server/tools"
)

type exclusiveStepOptions struct {
	EmitRunState        bool
	PersistRunLifecycle bool
	GoalLoop            bool
}

type exclusiveStepLifecycle interface {
	Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error
	Interrupt() error
	IsBusy() bool
	Snapshot() *RunSnapshot
	WithActiveStep(fn func(stepID string) error) (bool, error)
}

type backgroundNoticeScheduler interface {
	HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool)
	QueueDeveloperNotice(msg llm.Message)
	DrainPendingNotices() []steeringIntent
	HasPendingNotices() bool
	ConsumePendingBackgroundNotice(sessionID string) bool
	ScheduleIfIdle()
}

type contextCompactor interface {
	CompactContext(ctx context.Context, args string) error
	CompactContextForPreSubmit(ctx context.Context) error
	TriggerHandoff(ctx context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error)
	AutoCompactIfNeeded(ctx context.Context, stepID string, mode compactionMode) error
	ShouldCompactBeforeUserMessage(ctx context.Context, text string) (bool, error)
}

type stepLoopOptions struct {
	ReviewerFrequency              string
	ReviewerClient                 llm.Client
	EmitAssistantEvent             bool
	RefreshReviewerConfigOnResolve bool
	PendingUserInjectionIDs        map[string]struct{}
}

type stepLoopResult struct {
	Message                    llm.Message
	ExecutedToolCall           bool
	NoopFinalAnswer            bool
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
	ExecuteToolCalls(ctx context.Context, stepID string, calls []llm.ToolCall) ([]tools.Result, error)
}

type messageLifecycle interface {
	RestoreMessages() error
	FlushPendingUserInjections(stepID string, queueItemIDs map[string]struct{}) (int, error)
	DrainPendingUserInjections() []QueuedUserMessage
	QueueUserMessage(text string, clientRequestID string) QueuedUserMessage
	DiscardQueuedUserMessage(queueItemID string) (QueuedUserMessage, bool)
	HasPendingUserInjections() bool
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
}

type phaseProtocolTurn struct {
	Assistant             llm.Message
	LocalToolCalls        []llm.ToolCall
	HostedToolExecutions  []hostedToolExecution
	EnforcePhaseProtocol  bool
	MissingAssistantPhase bool
}

type phaseProtocolEnforcer interface {
	EnabledForModel(ctx context.Context) bool
	Apply(ctx context.Context, resp llm.Response, assistant llm.Message, localToolCalls []llm.ToolCall, hostedToolExecutions []hostedToolExecution) phaseProtocolTurn
}

func (e *Engine) ensureOrchestrationCollaborators() {
	e.collaboratorsOnce.Do(func() {
		if e.stepLifecycle == nil {
			e.stepLifecycle = &defaultExclusiveStepLifecycle{engine: e}
		}
		if e.backgroundFlow == nil {
			e.backgroundFlow = &defaultBackgroundNoticeScheduler{engine: e, steps: e.stepLifecycle}
		}
		if lifecycle, ok := e.stepLifecycle.(*defaultExclusiveStepLifecycle); ok && lifecycle.background == nil {
			lifecycle.background = e.backgroundFlow
		}
		if e.phaseProtocol == nil {
			e.phaseProtocol = &defaultPhaseProtocol{engine: e}
		}
		if e.messageFlow == nil {
			e.messageFlow = newDefaultMessageLifecycle(e, e.backgroundFlow)
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
