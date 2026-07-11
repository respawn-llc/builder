package runtime

import (
	"context"
	"strings"
	"time"

	"core/server/llm"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/transcript"
)

// Request orchestration calls this coordinator when model-visible runtime
// context must be prepared. Individual prompt families are owned here and by
// meta_context.go; request entry points must not append those prompts directly.
func (e *Engine) ensureMetaContextForRequest(ctx context.Context, stepID string) error {
	if err := e.steerBaseMetaContextIfNeeded(stepID); err != nil {
		return err
	}
	if err := e.steerHeadlessModeTransitionIfNeeded(stepID); err != nil {
		return err
	}
	if err := e.steerWorkflowModeIfNeeded(ctx, stepID); err != nil {
		return err
	}
	return e.materializePendingWorktreeReminder(stepID)
}

func (e *Engine) ensureMetaContextForCompaction(ctx context.Context, stepID string) error {
	return e.steerBaseMetaContextIfNeeded(stepID)
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
	builder := newActiveMetaContextBuilder(e.store.Meta(), e.cfg.Model, e.ThinkingLevel(), e.cfg.GlobalConfigDir, e.cfg.DisabledSkills, time.Now()).withSubagents(e.cfg.SubagentCatalogSettings, e.cfg.EnabledTools)
	opts := baseMetaContextBuildOptions(true)
	if e.workflowRunActive() {
		opts.SubagentInvocationContext = config.SubagentInvocationContextWorkflow
	}
	metaResult, err := builder.Build(opts)
	if err != nil {
		return err
	}
	intents := make([]steeringIntent, 0, 2)
	if combined := strings.TrimSpace(strings.Join(metaResult.SkillWarnings, "\n")); combined != "" {
		intents = append(intents, steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       string(transcript.EntryRoleWarning),
			Text:       combined,
		}))
	}
	intents = append(intents, steerMessagesWithPersistenceIntent(steeringPriorityRuntimeContext, steeringMessageEventDefault, true, metaResult.OrderedBaseMessages()))
	if err := e.steer(stepID, intents...); err != nil {
		return err
	}
	e.baseMetaInjected = true
	return nil
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
	if e.workflowRunActive() {
		return nil
	}
	if e.cfg.HeadlessMode == e.store.Meta().HeadlessActive {
		return nil
	}
	builder := newMetaContextBuilder(e.store.Meta().WorkspaceRoot, e.cfg.Model, e.ThinkingLevel(), e.cfg.DisabledSkills, time.Now()).withGlobalConfigDir(e.cfg.GlobalConfigDir)
	if e.cfg.HeadlessMode {
		metaResult, err := builder.Build(metaContextBuildOptions{IncludeHeadless: true})
		if err != nil {
			return err
		}
		if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityRuntimeContext, steeringMessageEventDefault, true, metaResult.Headless)); err != nil {
			return err
		}
		return e.store.SetHeadlessActive(true)
	}
	metaResult, err := builder.Build(metaContextBuildOptions{IncludeHeadlessExit: true})
	if err != nil {
		return err
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityRuntimeContext, steeringMessageEventDefault, true, metaResult.HeadlessExit)); err != nil {
		return err
	}
	return e.store.SetHeadlessActive(false)
}

func (e *Engine) steerWorkflowModeIfNeeded(ctx context.Context, stepID string) error {
	if !e.workflowRunActive() {
		return nil
	}
	runID := strings.TrimSpace(string(e.cfg.WorkflowRun.Contract.RunID))
	for _, msg := range e.transcriptRuntimeState().SnapshotMessages() {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorkflowMode && strings.TrimSpace(msg.SourcePath) == runID {
			return nil
		}
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return err
	}
	commentCount, err := e.currentWorkflowTaskCommentCount(ctx)
	if err != nil {
		return err
	}
	message, ok, renderErr := workflowModeMetaMessage(mode, e.cfg.WorkflowRun, commentCount)
	if renderErr != nil {
		return renderErr
	}
	if !ok {
		return nil
	}
	message.SourcePath = runID
	return e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityRuntimeContext, steeringMessageEventDefault, true, []llm.Message{message}))
}

func (e *Engine) compactionReinjectedMetaMessages(ctx context.Context) ([]llm.Message, error) {
	builder := newActiveMetaContextBuilder(e.store.Meta(), e.currentModel(), e.ThinkingLevel(), e.cfg.GlobalConfigDir, e.cfg.DisabledSkills, time.Now()).withSubagents(e.cfg.SubagentCatalogSettings, e.cfg.EnabledTools)
	opts := baseMetaContextBuildOptions(false)
	if e.workflowRunActive() {
		opts.SubagentInvocationContext = config.SubagentInvocationContextWorkflow
		mode, err := e.workflowCompletionMode(ctx)
		if err != nil {
			return nil, err
		}
		opts.IncludeWorkflow = true
		opts.WorkflowCompletionMode = mode
		opts.WorkflowRun = e.cfg.WorkflowRun
		commentCount, err := e.currentWorkflowTaskCommentCount(ctx)
		if err != nil {
			return nil, err
		}
		opts.WorkflowTaskCommentCount = commentCount
	}
	metaResult, err := builder.Build(opts)
	if err != nil {
		return nil, err
	}
	return metaResult.OrderedMetaMessages(), nil
}

func (e *Engine) currentWorkflowTaskCommentCount(ctx context.Context) (int64, error) {
	if !e.workflowRunActive() || e.cfg.WorkflowRun.TaskCommentCounter == nil {
		return 0, nil
	}
	taskID := strings.TrimSpace(e.cfg.WorkflowRun.Instructions.TaskID)
	if taskID == "" {
		return 0, nil
	}
	return e.cfg.WorkflowRun.TaskCommentCounter.CountTaskComments(ctx, workflow.TaskID(taskID))
}
