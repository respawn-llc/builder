package runtime

import (
	"context"
	"errors"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/toolspec"
)

var ErrWorkflowStartUnavailable = errors.New("Workflow start is unavailable")

type WorkflowAssignment struct {
	ContextMode    workflow.ContextMode
	CompletionMode workflowruntime.CompletionMode
	Prompt         workflowruntime.PromptContract
}

func (e *Engine) ApplyWorkflowAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	assignment WorkflowAssignment,
	persisted bool,
) (session.CommitReceipt, error) {
	if e == nil || e.steering == nil {
		return session.CommitReceipt{}, ErrSteeringUnavailable
	}
	if err := reference.Validate(); err != nil {
		return session.CommitReceipt{}, err
	}
	if err := e.workflowControl.validateSteering(steeringAdmissionWorkflowAssignment); err != nil {
		return session.CommitReceipt{}, err
	}
	entry := newWorkflowAssignmentQueueEntry(reference, assignment, persisted)
	wake, err := e.steering.append(entry)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	if wake {
		e.wakeSteeringDrain()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case reply := <-entry.start.reply:
		return reply.receipt, reply.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

// PersistedWorkflowAssignmentContext supplies the runtime-owned context needed
// to seed a fresh dormant Session before its first workflow assignment.
type PersistedWorkflowAssignmentContext struct {
	Workdir                 string
	GlobalConfigDir         string
	Model                   string
	ThinkingLevel           string
	SkillPolicy             config.SkillPolicy
	SubagentCatalogSettings config.Settings
	EnabledTools            []toolspec.ID
}

func PersistWorkflowAssignment(
	store *session.Store,
	assignment WorkflowAssignment,
	deliveryContext PersistedWorkflowAssignmentContext,
) (session.CommitReceipt, error) {
	if store == nil {
		return session.CommitReceipt{}, errors.New("session store is required")
	}
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	engine, err := newPersistedSteeringEngine(store)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	recent, err := engine.eventLog.ReadRecentRecords(1)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	// Dormant admission must call this before appending any other event. A
	// pre-existing record is the durable witness that base meta context has
	// already been seeded; unrelated callback writes would invalidate that
	// freshness test.
	if len(recent.Records) == 0 {
		builder := newActiveMetaContextBuilder(
			engine.store.Meta(),
			deliveryContext.Workdir,
			deliveryContext.Model,
			deliveryContext.ThinkingLevel,
			deliveryContext.GlobalConfigDir,
			deliveryContext.SkillPolicy,
			time.Now(),
		).withSubagents(deliveryContext.SubagentCatalogSettings, deliveryContext.EnabledTools)
		if err := engine.steerRuntimeBaseMetaContext(builder, config.SubagentInvocationContextWorkflow); err != nil {
			return session.CommitReceipt{}, err
		}
	}
	return engine.steerRuntimeWithCommitReceipt(steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	))
}
