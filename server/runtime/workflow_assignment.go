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

type WorkflowAssignmentEnsure struct {
	state *workflowAssignmentEnsureState
}

type workflowAssignmentEnsureState struct {
	done    chan struct{}
	once    sync.Once
	receipt session.CommitReceipt
	err     error
}

type queuedWorkflowAssignment struct {
	identity string
	intent   steeringIntent
	ensure   WorkflowAssignmentEnsure
}

func newWorkflowAssignmentEnsure() WorkflowAssignmentEnsure {
	return WorkflowAssignmentEnsure{state: &workflowAssignmentEnsureState{done: make(chan struct{})}}
}

func CompletedWorkflowAssignmentEnsure(receipt session.CommitReceipt, err error) WorkflowAssignmentEnsure {
	ensure := newWorkflowAssignmentEnsure()
	ensure.complete(receipt, err)
	return ensure
}

func (s WorkflowAssignmentEnsure) Wait(ctx context.Context) (session.CommitReceipt, error) {
	if s.state == nil {
		return session.CommitReceipt{}, errors.New("workflow assignment ensure is uninitialized")
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

func (s WorkflowAssignmentEnsure) complete(receipt session.CommitReceipt, err error) {
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

// EnsureWorkflowAssignment returns a committed no-op when the active Workflow
// meta-context already identifies the assignment. Otherwise it appends the
// assignment through the runtime steering owner.
func (e *Engine) EnsureWorkflowAssignment(assignment WorkflowAssignment) (WorkflowAssignmentEnsure, error) {
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
	return e.ensureWorkflowAssignmentMessage(message)
}

func (e *Engine) ensureWorkflowAssignmentMessage(message llm.Message) (WorkflowAssignmentEnsure, error) {
	identity, ok := textutil.OptionalTrimmed(message.SourcePath)
	if !ok {
		return WorkflowAssignmentEnsure{}, errors.New("workflow assignment identity is required")
	}
	e.workflowAssignmentMu.Lock()
	defer e.workflowAssignmentMu.Unlock()
	ensure := newWorkflowAssignmentEnsure()
	intent := steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	)
	e.ensureOrchestrationCollaborators()
	active, err := e.stepLifecycle.WithActiveStep(func(string) error {
		if e.closed.Load() {
			return ErrEngineClosed
		}
		if activeIdentity, present := activeWorkflowAssignmentIdentity(e.transcriptRuntimeState().SnapshotItems()); present &&
			activeIdentity == identity {
			ensure.complete(session.CommitReceipt{Committed: true}, nil)
			return nil
		}
		for _, pending := range e.pendingWorkflowAssignments {
			if pending.identity == identity {
				ensure = pending.ensure
				return nil
			}
		}
		e.pendingWorkflowAssignments = append(e.pendingWorkflowAssignments, queuedWorkflowAssignment{
			identity: identity,
			intent:   intent,
			ensure:   ensure,
		})
		return nil
	})
	if err != nil {
		ensure.complete(session.CommitReceipt{}, err)
		return ensure, err
	}
	if active {
		return ensure, nil
	}
	if activeIdentity, present := activeWorkflowAssignmentIdentity(e.transcriptRuntimeState().SnapshotItems()); present &&
		activeIdentity == identity {
		ensure.complete(session.CommitReceipt{Committed: true}, nil)
		return ensure, nil
	}
	receipt, err := e.steerWithCommitReceipt("", intent)
	ensure.complete(receipt, err)
	return ensure, nil
}

func EnsurePersistedWorkflowAssignment(
	store *session.Store,
	assignment WorkflowAssignment,
	deliveryContext PersistedWorkflowAssignmentContext,
) (WorkflowAssignmentEnsure, error) {
	if store == nil {
		return WorkflowAssignmentEnsure{}, errors.New("session store is required")
	}
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
	engine, err := newPersistedSteeringEngine(store)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
	if err := engine.restoreMessages(); err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
	recent, err := engine.eventLog.ReadRecentRecords(1)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
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
			return WorkflowAssignmentEnsure{}, err
		}
	}
	if identity, ok := textutil.OptionalTrimmed(message.SourcePath); ok {
		if activeIdentity, present := activeWorkflowAssignmentIdentity(engine.transcriptRuntimeState().SnapshotItems()); present &&
			activeIdentity == identity {
			return CompletedWorkflowAssignmentEnsure(session.CommitReceipt{Committed: true}, nil), nil
		}
	}
	return completePersistedWorkflowAssignment(engine, message), nil
}

func activeWorkflowAssignmentIdentity(items []llm.ResponseItem) (string, bool) {
	current, ok := latestActiveMetaContextForSlot(items, metaContextKindWorkflow)
	if !ok {
		return "", false
	}
	return current.sourcePath, true
}

func completePersistedWorkflowAssignment(engine *Engine, message llm.Message) WorkflowAssignmentEnsure {
	receipt, err := engine.steerWithCommitReceipt("", steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	))
	return CompletedWorkflowAssignmentEnsure(receipt, err)
}

func (e *Engine) flushPendingWorkflowAssignments(stepID string) error {
	e.workflowAssignmentMu.Lock()
	pending := append([]queuedWorkflowAssignment(nil), e.pendingWorkflowAssignments...)
	e.pendingWorkflowAssignments = nil
	e.workflowAssignmentMu.Unlock()
	for index, assignment := range pending {
		receipt, err := e.steerWithCommitReceipt(stepID, assignment.intent)
		assignment.ensure.complete(receipt, err)
		if err != nil {
			for _, remaining := range pending[index+1:] {
				remaining.ensure.complete(session.CommitReceipt{}, err)
			}
			return err
		}
		if !receipt.Committed {
			err = errors.New("workflow assignment message was not committed")
			for _, remaining := range pending[index+1:] {
				remaining.ensure.complete(session.CommitReceipt{}, err)
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
		assignment.ensure.complete(session.CommitReceipt{}, err)
	}
}
