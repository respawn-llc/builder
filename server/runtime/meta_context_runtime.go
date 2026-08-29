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
	if !e.baseMetaInjected {
		pendingRebind := e.store.Meta().RebindReminder != nil
		if err := e.steerFreshMetaContext(ctx, stepID); err != nil {
			return err
		}
		if pendingRebind {
			return e.store.SetSessionRebindReminder(nil)
		}
		return nil
	}
	if err := e.steerHeadlessModeTransitionIfNeeded(stepID); err != nil {
		return err
	}
	if err := e.steerWorkflowModeIfNeeded(ctx, stepID); err != nil {
		return err
	}
	if err := e.materializePendingWorktreeReminder(stepID); err != nil {
		return err
	}
	return e.materializePendingSessionRebindReminder(stepID)
}

func (e *Engine) InitializeWorkflowAssignment(ctx context.Context) error {
	if e == nil || e.closed.Load() {
		return ErrEngineClosed
	}
	if !e.workflowPromptActive() {
		return errors.New("workflow prompt is unavailable")
	}
	return e.withResolvedWorkflowMetaContext(
		ctx,
		workflowTaskPromptTriggerTaskDelivery,
		workflowMetaContextDeliveryConsume,
		baseMetaContextBuildOptions(true),
		func(options metaContextBuildOptions, shouldInject bool) error {
			if !shouldInject {
				return nil
			}
			metaResult, err := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy).Build(options)
			if err != nil {
				return err
			}
			_, err = e.steerDormantMetaContextBuildResult(metaResult)
			return err
		},
	)
}

func (e *Engine) steerFreshMetaContext(ctx context.Context, stepID string) error {
	builder := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy)
	options := baseMetaContextBuildOptions(true)
	options.WorktreeReminder = session.CloneWorktreeReminderState(e.store.Meta().WorktreeReminder)
	options.SessionRebindReminder = session.CloneSessionRebindReminder(e.store.Meta().RebindReminder)
	steer := func(options metaContextBuildOptions) (session.CommitReceipt, error) {
		metaResult, err := builder.Build(options)
		if err != nil {
			return session.CommitReceipt{}, err
		}
		return e.steerMetaContextBuildResult(stepID, metaResult)
	}
	if e.workflowPromptActive() {
		var committedErr error
		applyErr := e.withResolvedWorkflowMetaContext(ctx, workflowTaskPromptTriggerTaskDelivery, workflowMetaContextDeliveryConsume, options, func(resolved metaContextBuildOptions, _ bool) error {
			receipt, err := steer(resolved)
			if receipt.Committed {
				e.baseMetaInjected = true
				committedErr = err
				return nil
			}
			if err != nil {
				return err
			}
			e.baseMetaInjected = true
			return nil
		})
		return errors.Join(applyErr, committedErr)
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
	receipt, err := steer(options)
	if receipt.Committed {
		e.baseMetaInjected = true
	}
	if err != nil {
		return err
	}
	e.baseMetaInjected = true
	if e.cfg.HeadlessMode != meta.HeadlessActive {
		return e.store.SetHeadlessActive(e.cfg.HeadlessMode)
	}
	return nil
}

func (e *Engine) ensureMetaContextForCompaction(ctx context.Context, stepID string) error {
	return e.steerBaseMetaContextIfNeeded(stepID)
}

func (e *Engine) activeMetaContextBuilder(model string, skillPolicy config.SkillPolicy) metaContextBuilder {
	return newActiveMetaContextBuilder(e.store.Meta(), e.transcriptWorkingDir(), model, e.ThinkingLevel(), e.cfg.GlobalConfigDir, skillPolicy, time.Now()).
		withSubagents(e.cfg.SubagentCatalogSettings, e.cfg.EnabledTools)
}

func (e *Engine) steerMetaContextIfChanged(stepID string, messages []llm.Message) error {
	_, err := e.steerMetaContextIfChangedWithReceipt(stepID, messages)
	return err
}

func (e *Engine) steerMetaContextIfChangedWithReceipt(stepID string, messages []llm.Message) (session.CommitReceipt, error) {
	if len(messages) == 0 {
		return session.CommitReceipt{}, nil
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
		return session.CommitReceipt{}, nil
	}
	intent := steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, pending)
	receipt := session.CommitReceipt{}
	intent.items[len(intent.items)-1].commitReceipt = &receipt
	return receipt, e.steer(stepID, intent)
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

const (
	workflowTaskPromptTriggerUnknown workflowTaskPromptTrigger = iota
	workflowTaskPromptTriggerAssignmentDelivery
	workflowTaskPromptTriggerResumeDelivery
	workflowTaskPromptTriggerTaskDelivery
	workflowTaskPromptTriggerCompaction
)

type workflowMetaContextDeliveryMode uint8

const (
	workflowMetaContextDeliveryObserve workflowMetaContextDeliveryMode = iota
	workflowMetaContextDeliveryConsume
)

func (e *Engine) withResolvedWorkflowMetaContext(
	ctx context.Context,
	defaultTrigger workflowTaskPromptTrigger,
	deliveryMode workflowMetaContextDeliveryMode,
	options metaContextBuildOptions,
	fn func(metaContextBuildOptions, bool) error,
) error {
	if !e.workflowPromptActive() {
		return nil
	}
	delivery := e.currentNodeExecutionSnapshot().delivery
	if delivery == nil {
		return errors.New("workflow prompt delivery state is unavailable")
	}
	resolve := func(trigger workflowTaskPromptTrigger) error {
		resolved, shouldInject, err := e.resolveWorkflowMetaContext(ctx, trigger)
		if err != nil {
			return err
		}
		if shouldInject {
			options.SubagentInvocationContext = resolved.SubagentInvocationContext
			options.IncludeWorkflow = resolved.IncludeWorkflow
			options.WorkflowCompletionMode = resolved.WorkflowCompletionMode
			options.WorkflowPrompt = resolved.WorkflowPrompt
			options.WorkflowTaskAwareness = resolved.WorkflowTaskAwareness
			options.WorkflowTaskPromptKind = resolved.WorkflowTaskPromptKind
		}
		return fn(options, shouldInject)
	}
	if deliveryMode == workflowMetaContextDeliveryConsume {
		return delivery.apply(defaultTrigger, resolve)
	}
	return resolve(delivery.trigger(defaultTrigger))
}
func (e *Engine) resolveWorkflowMetaContext(
	ctx context.Context,
	trigger workflowTaskPromptTrigger,
) (metaContextBuildOptions, bool, error) {
	prompt, configured := e.workflowPrompt()
	if !configured {
		return metaContextBuildOptions{}, false, errors.New("workflow prompt is unavailable")
	}
	kind, shouldInject, err := selectWorkflowTaskPrompt(
		e.transcriptRuntimeState().SnapshotItems(),
		prompt.Identity,
		trigger,
	)
	if err != nil {
		return metaContextBuildOptions{}, false, err
	}
	if !shouldInject {
		return metaContextBuildOptions{}, false, nil
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return metaContextBuildOptions{}, false, err
	}
	awareness, err := e.currentWorkflowTaskAwareness(ctx)
	if err != nil {
		return metaContextBuildOptions{}, false, err
	}
	return metaContextBuildOptions{
		SubagentInvocationContext: config.SubagentInvocationContextWorkflow,
		IncludeWorkflow:           true,
		WorkflowCompletionMode:    mode,
		WorkflowPrompt:            prompt,
		WorkflowTaskAwareness:     awareness,
		WorkflowTaskPromptKind:    kind,
	}, true, nil
}
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
		if !hasWorkflowPrompt {
			return prompts.WorkflowTaskPromptInitialAssignment, true, nil
		}
		if sameMetaContextIdentity(current, desired) {
			return prompts.WorkflowTaskPromptInitialAssignment, false, nil
		}
		return prompts.WorkflowTaskPromptReassignment, true, nil
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
	options.SessionRebindReminder = session.CloneSessionRebindReminder(e.store.Meta().RebindReminder)
	metaResult, err := builder.Build(options)
	if err != nil {
		return err
	}
	_, err = e.steerMetaContextBuildResult(stepID, metaResult)
	return err
}

func (e *Engine) steerRuntimeBaseMetaContext(
	builder metaContextBuilder,
	invocationContext config.SubagentInvocationContext,
) error {
	metaResult, err := buildBaseMetaContext(builder, invocationContext, e.store.Meta().WorktreeReminder)
	if err != nil {
		return err
	}
	return e.steerRuntime(metaContextSteeringIntents(metaResult)...)
}

func (e *Engine) steerDormantBaseMetaContext(
	builder metaContextBuilder,
	invocationContext config.SubagentInvocationContext,
) error {
	metaResult, err := buildBaseMetaContext(builder, invocationContext, e.store.Meta().WorktreeReminder)
	if err != nil {
		return err
	}
	_, err = e.steerDormantMetaContextBuildResult(metaResult)
	return err
}

func buildBaseMetaContext(
	builder metaContextBuilder,
	invocationContext config.SubagentInvocationContext,
	worktreeReminder *session.WorktreeReminderState,
) (metaContextBuildResult, error) {
	options := baseMetaContextBuildOptions(true)
	options.SubagentInvocationContext = invocationContext
	options.WorktreeReminder = session.CloneWorktreeReminderState(worktreeReminder)
	return builder.Build(options)
}

func (e *Engine) steerMetaContextBuildResult(stepID string, metaResult metaContextBuildResult) (session.CommitReceipt, error) {
	if e == nil || e.closed.Load() {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	if warning := strings.TrimSpace(strings.Join(metaResult.SkillWarnings, "\n")); warning != "" {
		if err := e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       string(transcript.EntryRoleWarning),
			Text:       warning,
		})); err != nil {
			return session.CommitReceipt{}, err
		}
	}
	return e.steerMetaContextIfChangedWithReceipt(
		stepID,
		metaResult.Projection().Messages(),
	)
}

func (e *Engine) steerDormantMetaContextBuildResult(metaResult metaContextBuildResult) (session.CommitReceipt, error) {
	if e == nil || e.closed.Load() {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	intents := metaContextSteeringIntents(metaResult)
	if len(intents) == 0 {
		return session.CommitReceipt{}, nil
	}
	provenance := sessionSteeringProvenance()
	for _, intent := range intents[:len(intents)-1] {
		if err := e.steerOrdered(provenance, intent); err != nil {
			return session.CommitReceipt{}, err
		}
	}
	last := intents[len(intents)-1]
	receipt := session.CommitReceipt{}
	last.items[len(last.items)-1].commitReceipt = &receipt
	return receipt, e.steerOrdered(provenance, last)
}

func metaContextSteeringIntents(metaResult metaContextBuildResult) []steeringIntent {
	intents := make([]steeringIntent, 0, 2)
	if warning := strings.TrimSpace(strings.Join(metaResult.SkillWarnings, "\n")); warning != "" {
		intents = append(intents, steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       string(transcript.EntryRoleWarning),
			Text:       warning,
		}))
	}
	if messages := metaResult.Projection().Messages(); len(messages) > 0 {
		intents = append(intents, steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			messages,
		))
	}
	return intents
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
		if err := e.steerMetaContextIfChanged(stepID, metaResult.Headless); err != nil {
			return err
		}
		return e.store.SetHeadlessActive(true)
	}
	metaResult, err := builder.Build(metaContextBuildOptions{IncludeHeadlessExit: true})
	if err != nil {
		return err
	}
	if err := e.steerMetaContextIfChanged(stepID, metaResult.HeadlessExit); err != nil {
		return err
	}
	return e.store.SetHeadlessActive(false)
}

func (e *Engine) steerWorkflowModeIfNeeded(ctx context.Context, stepID string) error {
	if !e.workflowPromptActive() {
		return nil
	}
	return e.withResolvedWorkflowMetaContext(ctx, workflowTaskPromptTriggerTaskDelivery, workflowMetaContextDeliveryConsume, metaContextBuildOptions{}, func(options metaContextBuildOptions, shouldInject bool) error {
		if !shouldInject {
			return nil
		}
		metaResult, err := e.activeMetaContextBuilder(e.cfg.Model, e.cfg.SkillPolicy).Build(options)
		if err != nil {
			return err
		}
		return e.steerMetaContextIfChanged(stepID, metaResult.Workflow)
	})
}

func roleOrUser(role *llm.Role) llm.Role {
	if role == nil {
		return llm.RoleUser
	}
	return *role
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
		err := e.withResolvedWorkflowMetaContext(ctx, workflowTaskPromptTriggerCompaction, workflowMetaContextDeliveryObserve, opts, func(resolved metaContextBuildOptions, shouldInject bool) error {
			if !shouldInject {
				panic("build compaction meta context: active workflow did not select a workflow task prompt")
			}
			opts = resolved
			return nil
		})
		if err != nil {
			return metaContextProjection{}, err
		}
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
