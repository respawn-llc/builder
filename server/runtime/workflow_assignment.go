package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type WorkflowAssignment struct {
	ContextMode    workflow.ContextMode
	CompletionMode workflowruntime.CompletionMode
	Prompt         workflowruntime.PromptContract
}

type WorkflowAssignmentSnapshot struct {
	message  *llm.Message
	thinking *string
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

type WorkflowAssignmentSteer struct {
	state *workflowAssignmentSteerState
}

type workflowAssignmentSteerState struct {
	done    chan struct{}
	once    sync.Once
	receipt session.CommitReceipt
	err     error
}

type queuedWorkflowAssignment struct {
	intent steeringIntent
	steer  WorkflowAssignmentSteer
}

func newWorkflowAssignmentSteer() WorkflowAssignmentSteer {
	return WorkflowAssignmentSteer{state: &workflowAssignmentSteerState{done: make(chan struct{})}}
}

func CompletedWorkflowAssignmentSteer(receipt session.CommitReceipt, err error) WorkflowAssignmentSteer {
	steer := newWorkflowAssignmentSteer()
	steer.complete(receipt, err)
	return steer
}

func (s WorkflowAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	if s.state == nil {
		return session.CommitReceipt{}, errors.New("workflow assignment steer is uninitialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.state.done:
		return s.state.receipt, s.state.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

func (s WorkflowAssignmentSteer) complete(receipt session.CommitReceipt, err error) {
	if s.state == nil {
		return
	}
	if err == nil && !receipt.Committed {
		err = errors.New("workflow assignment message was not committed")
	}
	s.state.once.Do(func() {
		s.state.receipt = receipt
		s.state.err = err
		close(s.state.done)
	})
}

func (e *Engine) SteerWorkflowAssignment(assignment WorkflowAssignment) (WorkflowAssignmentSteer, error) {
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	return e.steerWorkflowAssignmentMessage(message)
}

func (e *Engine) SteerWorkflowAssignmentSnapshot(snapshot WorkflowAssignmentSnapshot) (WorkflowAssignmentSteer, error) {
	if err := validateWorkflowAssignmentSnapshot(snapshot); err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	if err := e.RestoreWorkflowAssignmentSnapshotThinking(snapshot); err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	return e.steerWorkflowAssignmentMessage(snapshot.restorationMessage())
}

func (e *Engine) RestoreWorkflowAssignmentSnapshotThinking(snapshot WorkflowAssignmentSnapshot) error {
	if snapshot.thinking == nil {
		return nil
	}
	return e.setThinkingValue(*snapshot.thinking)
}

func (e *Engine) steerWorkflowAssignmentMessage(message llm.Message) (WorkflowAssignmentSteer, error) {
	steer := newWorkflowAssignmentSteer()
	intent := steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	)
	e.ensureOrchestrationCollaborators()
	active, err := e.stepLifecycle.WithActiveStep(func(string) error {
		e.workflowAssignmentMu.Lock()
		defer e.workflowAssignmentMu.Unlock()
		if e.closed.Load() {
			return ErrEngineClosed
		}
		e.pendingWorkflowAssignments = append(e.pendingWorkflowAssignments, queuedWorkflowAssignment{
			intent: intent,
			steer:  steer,
		})
		return nil
	})
	if err != nil {
		steer.complete(session.CommitReceipt{}, err)
		return steer, err
	}
	if active {
		return steer, nil
	}
	receipt, err := e.steerWithCommitReceipt("", intent)
	steer.complete(receipt, err)
	return steer, nil
}

func CapturePersistedWorkflowAssignment(
	store *session.Store,
) (WorkflowAssignmentSnapshot, bool, error) {
	if store == nil {
		return WorkflowAssignmentSnapshot{}, false, errors.New("session store is required")
	}
	if err := store.EnsureActiveWorkflowAssignmentProjection(); err != nil {
		return WorkflowAssignmentSnapshot{}, false, err
	}
	meta := store.Meta()
	snapshot := WorkflowAssignmentSnapshot{}
	if meta.ActiveWorkflowAssignment != nil {
		message, err := llmMessageFromSessionRecord(*meta.ActiveWorkflowAssignment)
		if err != nil {
			return WorkflowAssignmentSnapshot{}, false, err
		}
		snapshot.message = &message
	}
	if meta.ChatSettings != nil && meta.ChatSettings.Thinking != nil {
		thinking := *meta.ChatSettings.Thinking
		snapshot.thinking = &thinking
	}
	if err := validateWorkflowAssignmentSnapshot(snapshot); err != nil {
		return WorkflowAssignmentSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (s WorkflowAssignmentSnapshot) WithThinkingLevel(level string) WorkflowAssignmentSnapshot {
	value := level
	s.thinking = &value
	return s
}

func SteerPersistedWorkflowAssignmentSnapshot(
	store *session.Store,
	snapshot WorkflowAssignmentSnapshot,
) (WorkflowAssignmentSteer, error) {
	if store == nil {
		return WorkflowAssignmentSteer{}, errors.New("session store is required")
	}
	if err := validateWorkflowAssignmentSnapshot(snapshot); err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	engine, err := newPersistedSteeringEngine(store)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	return completePersistedWorkflowAssignment(engine, snapshot.restorationMessage()), nil
}

func validateWorkflowAssignmentSnapshot(snapshot WorkflowAssignmentSnapshot) error {
	if snapshot.message != nil &&
		(snapshot.message.MessageType == nil || *snapshot.message.MessageType != llm.MessageTypeWorkflowMode) {
		return errors.New("workflow assignment snapshot is required")
	}
	return nil
}

func (s WorkflowAssignmentSnapshot) restorationMessage() llm.Message {
	if s.message != nil {
		return *s.message
	}
	return llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeWorkflowModeExit),
		Content: textutil.Value(
			"The preceding Workflow assignment was discarded. There is no current executable Workflow Node until a later assignment arrives.",
		),
	}
}

func SteerPersistedWorkflowAssignment(
	store *session.Store,
	assignment WorkflowAssignment,
	deliveryContext PersistedWorkflowAssignmentContext,
) (WorkflowAssignmentSteer, error) {
	if store == nil {
		return WorkflowAssignmentSteer{}, errors.New("session store is required")
	}
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	engine, err := newPersistedSteeringEngine(store)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	recent, err := engine.eventLog.ReadRecentRecords(1)
	if err != nil {
		return WorkflowAssignmentSteer{}, err
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
		if err := engine.steerBaseMetaContext("", builder, config.SubagentInvocationContextWorkflow); err != nil {
			return WorkflowAssignmentSteer{}, err
		}
	}
	return completePersistedWorkflowAssignment(engine, message), nil
}

func completePersistedWorkflowAssignment(engine *Engine, message llm.Message) WorkflowAssignmentSteer {
	receipt, err := engine.steerWithCommitReceipt("", steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	))
	return CompletedWorkflowAssignmentSteer(receipt, err)
}

func (e *Engine) flushPendingWorkflowAssignments(stepID string) error {
	e.workflowAssignmentMu.Lock()
	pending := append([]queuedWorkflowAssignment(nil), e.pendingWorkflowAssignments...)
	e.pendingWorkflowAssignments = nil
	e.workflowAssignmentMu.Unlock()
	for index, assignment := range pending {
		receipt, err := e.steerWithCommitReceipt(stepID, assignment.intent)
		assignment.steer.complete(receipt, err)
		if err != nil {
			for _, remaining := range pending[index+1:] {
				remaining.steer.complete(session.CommitReceipt{}, err)
			}
			return err
		}
		if !receipt.Committed {
			err = errors.New("workflow assignment message was not committed")
			for _, remaining := range pending[index+1:] {
				remaining.steer.complete(session.CommitReceipt{}, err)
			}
			return err
		}
	}
	return nil
}

func (e *Engine) failPendingWorkflowAssignments(err error) {
	e.workflowAssignmentMu.Lock()
	pending := append([]queuedWorkflowAssignment(nil), e.pendingWorkflowAssignments...)
	e.pendingWorkflowAssignments = nil
	e.workflowAssignmentMu.Unlock()
	for _, assignment := range pending {
		assignment.steer.complete(session.CommitReceipt{}, err)
	}
}
