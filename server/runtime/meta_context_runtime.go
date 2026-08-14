package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/transcript"
)

// Request orchestration calls this coordinator when model-visible runtime
// context must be prepared. Individual prompt families are owned here and by
// meta_context.go; request entry points must not append those prompts directly.
func (e *Engine) ensureMetaContextForRequest(ctx context.Context, stepID string) error {
	if err := e.preflightWorkflowResumeAssignment(); err != nil {
		return err
	}
	if !e.baseMetaInjected {
		return e.steerFreshMetaContext(ctx, stepID)
	}
	if err := e.steerHeadlessModeTransitionIfNeeded(stepID); err != nil {
		return err
	}
	if err := e.steerWorkflowModeIfNeeded(ctx, stepID); err != nil {
		return err
	}
	return e.materializePendingWorktreeReminder(stepID)
}

func (e *Engine) steerFreshMetaContext(ctx context.Context, stepID string) error {
	builder := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy)
	options := baseMetaContextBuildOptions(true)
	options.WorktreeReminder = session.CloneWorktreeReminderState(e.store.Meta().WorktreeReminder)

	if e.workflowPromptActive() {
		delivery := e.currentNodeExecutionSnapshot().delivery
		if delivery == nil {
			return errors.New("workflow prompt delivery state is unavailable")
		}
		return delivery.apply(workflowTaskPromptTriggerTaskDelivery, func(trigger workflowTaskPromptTrigger) error {
			prompt, configured := e.workflowPrompt()
			if !configured {
				return errors.New("workflow prompt is unavailable")
			}
			kind, shouldInject, err := selectWorkflowTaskPrompt(
				e.transcriptRuntimeState().SnapshotItems(),
				prompt.Identity,
				trigger,
			)
			if err != nil {
				return err
			}
			if shouldInject {
				mode, err := e.workflowCompletionMode(ctx)
				if err != nil {
					return err
				}
				awareness, err := e.currentWorkflowTaskAwareness(ctx)
				if err != nil {
					return err
				}
				options.SubagentInvocationContext = config.SubagentInvocationContextWorkflow
				options.IncludeWorkflow = true
				options.WorkflowCompletionMode = mode
				options.WorkflowPrompt = prompt
				options.WorkflowTaskAwareness = awareness
				options.WorkflowTaskPromptKind = kind
			}
			return e.steerFreshMetaContextWithOptions(stepID, builder, options)
		})
	}

	meta := e.store.Meta()
	if e.cfg.HeadlessMode != meta.HeadlessActive {
		if e.cfg.HeadlessMode {
			options.IncludeHeadless = true
		} else {
			options.IncludeHeadlessExit = true
		}
	}
	if goal, ok := e.goalContinuation().activeGoal(); ok {
		options.ActiveGoal = &goal
	}
	if err := e.steerFreshMetaContextWithOptions(stepID, builder, options); err != nil {
		return err
	}
	if e.cfg.HeadlessMode != meta.HeadlessActive {
		return e.store.SetHeadlessActive(e.cfg.HeadlessMode)
	}
	return nil
}

func (e *Engine) steerFreshMetaContextWithOptions(
	stepID string,
	builder metaContextBuilder,
	options metaContextBuildOptions,
) error {
	metaResult, err := builder.Build(options)
	if err != nil {
		return err
	}
	if err := e.steerMetaContextBuildResult(stepID, metaResult); err != nil {
		return err
	}
	e.baseMetaInjected = true
	return nil
}

func (e *Engine) preflightWorkflowResumeAssignment() error {
	if !e.workflowPromptActive() {
		return nil
	}
	delivery := e.currentNodeExecutionSnapshot().delivery
	if delivery == nil {
		return errors.New("workflow prompt delivery state is unavailable")
	}
	trigger := delivery.trigger(workflowTaskPromptTriggerTaskDelivery)
	if trigger != workflowTaskPromptTriggerResumeDelivery {
		return nil
	}
	prompt, configured := e.workflowPrompt()
	if !configured {
		return errors.New("workflow prompt is unavailable")
	}
	_, _, err := selectWorkflowTaskPrompt(
		e.transcriptRuntimeState().SnapshotItems(),
		prompt.Identity,
		trigger,
	)
	return err
}

func (e *Engine) ensureMetaContextForCompaction(ctx context.Context, stepID string) error {
	return e.steerBaseMetaContextIfNeeded(stepID)
}

func (e *Engine) activeMetaContextBuilder(model string, skillPolicy config.SkillPolicy) metaContextBuilder {
	return newActiveMetaContextBuilder(e.store.Meta(), e.transcriptWorkingDir(), model, e.ThinkingLevel(), e.cfg.GlobalConfigDir, skillPolicy, time.Now()).
		withSubagents(e.cfg.SubagentCatalogSettings, e.cfg.EnabledTools)
}

func (e *Engine) steerMetaContextIfChanged(stepID string, priority steeringPriority, messages []llm.Message) error {
	if len(messages) == 0 {
		return nil
	}
	activeItems := e.transcriptRuntimeState().SnapshotItems()
	pending := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if latestActiveMetaContextMatches(activeItems, message) {
			continue
		}
		pending = append(pending, message)
	}
	if len(pending) == 0 {
		return nil
	}
	return e.steer(stepID, steerMessagesWithPersistenceIntent(priority, steeringMessageEventDefault, true, pending))
}

func latestActiveMetaContextMatches(items []llm.ResponseItem, desired llm.Message) bool {
	// MessageType selects the typed context slot. Structured target facts carry
	// worktree identity; rendered prompt text is presentation only.
	desiredClassification, ok := classifyMetaContextMessage(desired)
	if !ok {
		return false
	}
	currentClassification, ok := latestActiveMetaContextForSlot(items, desiredClassification.kind)
	if !ok {
		return false
	}
	return sameMetaContextIdentity(currentClassification, desiredClassification)
}

func latestActiveMetaContextForSlot(items []llm.ResponseItem, kind metaContextKind) (metaContextClassification, bool) {
	for idx := len(items) - 1; idx >= 0; idx-- {
		item := items[idx]
		if item.Type != llm.ResponseItemTypeMessage {
			continue
		}
		classification, classified := classifyMetaContextMessage(llm.Message{
			Role:            roleOrUser(item.Role),
			MessageType:     item.MessageType,
			SourcePath:      item.SourcePath,
			WorktreeContext: item.WorktreeContext,
		})
		if !classified || !sameMetaContextSlot(classification.kind, kind) {
			continue
		}
		return classification, true
	}
	return metaContextClassification{}, false
}

type workflowTaskPromptTrigger uint8

var errWorkflowResumeAssignmentUnavailable = errors.New(
	"workflow Resume requires the current Node assignment in model context",
)

const (
	workflowTaskPromptTriggerUnknown workflowTaskPromptTrigger = iota
	workflowTaskPromptTriggerAssignmentDelivery
	workflowTaskPromptTriggerResumeDelivery
	workflowTaskPromptTriggerTaskDelivery
	workflowTaskPromptTriggerCompaction
)

func selectWorkflowTaskPrompt(
	items []llm.ResponseItem,
	currentNodeIdentity string,
	trigger workflowTaskPromptTrigger,
) (prompts.WorkflowTaskPromptKind, bool, error) {
	normalizedCurrentNodeIdentity := strings.TrimSpace(currentNodeIdentity)
	if normalizedCurrentNodeIdentity == "" {
		panic("select workflow task prompt: current node identity is required")
	}
	desired, ok := classifyMetaContextMessage(llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
		SourcePath:  textutil.Value(normalizedCurrentNodeIdentity),
	})
	if !ok {
		panic("select workflow task prompt: workflow-mode message classification failed")
	}
	current, hasWorkflowPrompt := latestActiveMetaContextForSlot(items, metaContextKindWorkflow)
	if trigger == workflowTaskPromptTriggerResumeDelivery {
		if !hasWorkflowPrompt || !sameMetaContextIdentity(current, desired) {
			return prompts.WorkflowTaskPromptInitialAssignment, false, errWorkflowResumeAssignmentUnavailable
		}
		return prompts.WorkflowTaskPromptInitialAssignment, false, nil
	}
	if !hasWorkflowPrompt {
		return prompts.WorkflowTaskPromptInitialAssignment, true, nil
	}
	sameRun := sameMetaContextIdentity(current, desired)
	switch trigger {
	case workflowTaskPromptTriggerAssignmentDelivery:
		return prompts.WorkflowTaskPromptReassignment, true, nil
	case workflowTaskPromptTriggerTaskDelivery:
		if sameRun {
			return prompts.WorkflowTaskPromptInitialAssignment, false, nil
		}
		return prompts.WorkflowTaskPromptReassignment, true, nil
	case workflowTaskPromptTriggerCompaction:
		if sameRun {
			return prompts.WorkflowTaskPromptCompactionReminder, true, nil
		}
		return prompts.WorkflowTaskPromptReassignment, true, nil
	default:
		panic("select workflow task prompt: unknown trigger")
	}
}

func sameMetaContextIdentity(current, desired metaContextClassification) bool {
	if current.kind != desired.kind {
		return false
	}
	switch desired.kind {
	case metaContextKindWorktree, metaContextKindWorktreeExit:
		if current.worktreeContext != nil && desired.worktreeContext != nil {
			return session.WorktreeContextEqual(*current.worktreeContext, *desired.worktreeContext)
		}
		return legacyWorktreeContextMatchesBySourcePath(current, desired)
	default:
		return current.sourcePath == desired.sourcePath
	}
}

// legacyWorktreeContextMatchesBySourcePath prevents one duplicate when an
// active generation was persisted before structured worktree identity existed.
func legacyWorktreeContextMatchesBySourcePath(current, desired metaContextClassification) bool {
	return current.worktreeContext == nil &&
		desired.worktreeContext != nil &&
		desired.worktreeContext.ContextID == nil &&
		current.sourcePath == desired.worktreeContext.EffectiveCwd
}

func sameMetaContextSlot(left, right metaContextKind) bool {
	switch right {
	case metaContextKindHeadless, metaContextKindHeadlessExit:
		return left == metaContextKindHeadless || left == metaContextKindHeadlessExit
	case metaContextKindWorkflow, metaContextKindWorkflowExit:
		return left == metaContextKindWorkflow || left == metaContextKindWorkflowExit
	case metaContextKindWorktree, metaContextKindWorktreeExit:
		return left == metaContextKindWorktree || left == metaContextKindWorktreeExit
	default:
		return left == right
	}
}

// steerBaseMetaContextIfNeeded injects base meta context (AGENTS.md, skills,
// subagents, environment) exactly once, at the first request of a fresh
// session. The guard is deterministic: it is seeded from restored-history
// length at startup and from the replacement length after compaction (which
// reinjects base meta into the history_replaced payload). It never scans the
// conversation to decide whether context is "missing" — every session's active
// list is born carrying base meta, so re-injection cannot occur.
func (e *Engine) steerBaseMetaContextIfNeeded(stepID string) error {
	if e.baseMetaInjected {
		return nil
	}
	builder := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy)
	invocationContext := config.SubagentInvocationContextOrdinary
	if e.workflowPromptActive() {
		invocationContext = config.SubagentInvocationContextWorkflow
	}
	if err := e.steerBaseMetaContext(stepID, builder, invocationContext); err != nil {
		return err
	}
	e.baseMetaInjected = true
	return nil
}

func (e *Engine) steerBaseMetaContext(
	stepID string,
	builder metaContextBuilder,
	invocationContext config.SubagentInvocationContext,
) error {
	options := baseMetaContextBuildOptions(true)
	options.SubagentInvocationContext = invocationContext
	options.WorktreeReminder = session.CloneWorktreeReminderState(e.store.Meta().WorktreeReminder)
	metaResult, err := builder.Build(options)
	if err != nil {
		return err
	}
	return e.steerMetaContextBuildResult(stepID, metaResult)
}

func (e *Engine) steerMetaContextBuildResult(stepID string, metaResult metaContextBuildResult) error {
	intents := make([]steeringIntent, 0, 2)
	if warning := strings.TrimSpace(strings.Join(metaResult.SkillWarnings, "\n")); warning != "" {
		intents = append(intents, steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       string(transcript.EntryRoleWarning),
			Text:       warning,
		}))
	}
	intents = append(intents, steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		metaResult.Projection().Messages(),
	))
	return e.steer(stepID, intents...)
}

// steerHeadlessModeTransitionIfNeeded reconciles the launch mode with the
// persisted headless state. cfg.HeadlessMode reflects how this process was
// started (true on `--continue`, false on an interactive launch);
// Meta.HeadlessActive reflects the mode the session was last in. A mismatch is
// a real transition: entering headless appends the enter prompt once, returning
// to interactive appends the exit prompt once, and matching states are a no-op
// so repeated `--continue` launches do not duplicate the enter prompt.
// Interactive is the default, so no reminder is injected while both are false.
func (e *Engine) steerHeadlessModeTransitionIfNeeded(stepID string) error {
	if e.workflowPromptActive() {
		return nil
	}
	if e.cfg.HeadlessMode == e.store.Meta().HeadlessActive {
		return nil
	}
	builder := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy)
	if e.cfg.HeadlessMode {
		metaResult, err := builder.Build(metaContextBuildOptions{IncludeHeadless: true})
		if err != nil {
			return err
		}
		if err := e.steerMetaContextIfChanged(stepID, steeringPriorityRuntimeContext, metaResult.Headless); err != nil {
			return err
		}
		return e.store.SetHeadlessActive(true)
	}
	metaResult, err := builder.Build(metaContextBuildOptions{IncludeHeadlessExit: true})
	if err != nil {
		return err
	}
	if err := e.steerMetaContextIfChanged(stepID, steeringPriorityRuntimeContext, metaResult.HeadlessExit); err != nil {
		return err
	}
	return e.store.SetHeadlessActive(false)
}

func (e *Engine) steerWorkflowModeIfNeeded(ctx context.Context, stepID string) error {
	if !e.workflowPromptActive() {
		return nil
	}
	delivery := e.currentNodeExecutionSnapshot().delivery
	if delivery == nil {
		return errors.New("workflow prompt delivery state is unavailable")
	}
	return delivery.apply(workflowTaskPromptTriggerTaskDelivery, func(trigger workflowTaskPromptTrigger) error {
		prompt, configured := e.workflowPrompt()
		if !configured {
			return errors.New("workflow prompt is unavailable")
		}
		kind, shouldInject, err := selectWorkflowTaskPrompt(
			e.transcriptRuntimeState().SnapshotItems(),
			prompt.Identity,
			trigger,
		)
		if err != nil {
			return err
		}
		if !shouldInject {
			return nil
		}
		mode, err := e.workflowCompletionMode(ctx)
		if err != nil {
			return err
		}
		awareness, err := e.currentWorkflowTaskAwareness(ctx)
		if err != nil {
			return err
		}
		metaResult, err := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy).Build(metaContextBuildOptions{
			IncludeWorkflow:        true,
			WorkflowCompletionMode: mode,
			WorkflowPrompt:         prompt,
			WorkflowTaskAwareness:  awareness,
			WorkflowTaskPromptKind: kind,
		})
		if err != nil {
			return err
		}
		return e.steerMetaContextIfChanged(stepID, steeringPriorityRuntimeContext, metaResult.Workflow)
	})
}

func roleOrUser(role *llm.Role) llm.Role {
	if role == nil {
		return llm.RoleUser
	}
	return *role
}

func (e *Engine) compactionReinjectedMetaMessages(ctx context.Context) ([]llm.Message, error) {
	return e.compactionReinjectedMetaMessagesForMode(ctx, compactionModeManual)
}

func (e *Engine) compactionReinjectedMetaMessagesForMode(ctx context.Context, mode compactionMode) ([]llm.Message, error) {
	projection, err := e.compactionReinjectedMetaContextProjection(ctx, mode)
	if err != nil {
		return nil, err
	}
	return projection.Messages(), nil
}

func (e *Engine) compactionReinjectedMetaContextProjection(ctx context.Context, mode compactionMode) (metaContextProjection, error) {
	meta := e.store.Meta()
	skillPolicy, err := e.reconstructionSkillPolicy(ctx)
	if err != nil {
		return metaContextProjection{}, err
	}
	builder := e.activeMetaContextBuilder(e.currentModel(), skillPolicy)
	opts := baseMetaContextBuildOptions(false)
	opts.IncludeHeadless = meta.HeadlessActive
	opts.WorktreeReminder = session.CloneWorktreeReminderState(meta.WorktreeReminder)
	if mode == compactionModeWorkflowPostCompletion {
		opts.SubagentInvocationContext = config.SubagentInvocationContextWorkflow
	} else if e.currentNodeExecutionActive() {
		delivery := e.currentNodeExecutionSnapshot().delivery
		if delivery == nil {
			return metaContextProjection{}, errors.New("workflow prompt delivery state is unavailable")
		}
		opts.SubagentInvocationContext = config.SubagentInvocationContextWorkflow
		prompt, configured := e.workflowPrompt()
		if !configured {
			return metaContextProjection{}, errors.New("workflow prompt is unavailable")
		}
		kind, shouldInject, err := selectWorkflowTaskPrompt(
			e.transcriptRuntimeState().SnapshotItems(),
			prompt.Identity,
			delivery.trigger(workflowTaskPromptTriggerCompaction),
		)
		if err != nil {
			return metaContextProjection{}, err
		}
		if !shouldInject {
			panic("build compaction meta context: active workflow did not select a workflow task prompt")
		}
		mode, err := e.workflowCompletionMode(ctx)
		if err != nil {
			return metaContextProjection{}, err
		}
		opts.IncludeWorkflow = true
		opts.WorkflowCompletionMode = mode
		opts.WorkflowPrompt = prompt
		opts.WorkflowTaskPromptKind = kind
		awareness, err := e.currentWorkflowTaskAwareness(ctx)
		if err != nil {
			return metaContextProjection{}, err
		}
		opts.WorkflowTaskAwareness = awareness
	} else if goal, ok := e.goalContinuation().activeGoal(); ok {
		opts.ActiveGoal = &goal
	}
	metaResult, err := builder.Build(opts)
	if err != nil {
		return metaContextProjection{}, err
	}
	return metaResult.Projection(), nil
}

func (e *Engine) currentWorkflowTaskAwareness(ctx context.Context) (workflowruntime.TaskAwareness, error) {
	execution, active := e.currentNodeExecutionConfig()
	if !active {
		if prompt, configured := e.workflowPrompt(); configured {
			return prompt.TaskAwareness, nil
		}
		return workflowruntime.TaskAwareness{}, nil
	}
	if execution.TaskAwarenessSource == nil {
		return workflowruntime.TaskAwareness{}, nil
	}
	taskID := execution.Instructions.CurrentNode.TaskID
	if taskID == "" {
		return workflowruntime.TaskAwareness{}, nil
	}
	return execution.TaskAwarenessSource.TaskAwareness(ctx, taskID)
}
