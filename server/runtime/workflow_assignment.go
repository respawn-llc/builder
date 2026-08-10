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
	"core/shared/toolspec"
)

type WorkflowAssignment struct {
	ContextMode    workflow.ContextMode
	CompletionMode workflowruntime.CompletionMode
	Prompt         workflowruntime.PromptContract
}

type WorkflowAssignmentEnsure struct {
	Receipt  session.CommitReceipt
	Appended bool
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

func completedWorkflowAssignmentSteer(receipt session.CommitReceipt, err error) WorkflowAssignmentSteer {
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

func (e *Engine) EnsureWorkflowAssignment(
	ctx context.Context,
	assignment WorkflowAssignment,
) (WorkflowAssignmentEnsure, error) {
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
	if latestActiveMetaContextMatches(e.transcriptRuntimeState().SnapshotItems(), message) {
		return WorkflowAssignmentEnsure{
			Receipt: session.CommitReceipt{Committed: true},
		}, nil
	}
	steer, err := e.SteerWorkflowAssignment(assignment)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
	receipt, err := steer.Wait(ctx)
	if err != nil {
		return WorkflowAssignmentEnsure{Receipt: receipt, Appended: receipt.Committed}, err
	}
	if !receipt.Committed {
		return WorkflowAssignmentEnsure{}, errors.New("workflow assignment message was not committed")
	}
	return WorkflowAssignmentEnsure{Receipt: receipt, Appended: true}, nil
}

func EnsurePersistedWorkflowAssignment(
	ctx context.Context,
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
	recent, err := engine.eventLog.ReadRecentRecords(1)
	if err != nil {
		return WorkflowAssignmentEnsure{}, err
	}
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
	} else {
		items, err := activePersistedMetaContextItems(engine.eventLog)
		if err != nil {
			return WorkflowAssignmentEnsure{}, err
		}
		if latestActiveMetaContextMatches(items, message) {
			return WorkflowAssignmentEnsure{
				Receipt: session.CommitReceipt{Committed: true},
			}, nil
		}
	}
	steer := completePersistedWorkflowAssignment(engine, message)
	receipt, err := steer.Wait(ctx)
	if err != nil {
		return WorkflowAssignmentEnsure{Receipt: receipt, Appended: receipt.Committed}, err
	}
	return WorkflowAssignmentEnsure{Receipt: receipt, Appended: true}, nil
}

func activePersistedMetaContextItems(eventLog session.MaterializedEventLog) ([]llm.ResponseItem, error) {
	var matchErr error
	window, err := eventLog.ReadNewestSegmentBackward(compactionBoundaryMatcher(&matchErr))
	if err != nil {
		return nil, err
	}
	if matchErr != nil {
		return nil, matchErr
	}
	items := make([]llm.ResponseItem, 0, len(window.Records))
	for _, record := range window.Records {
		payload, err := record.Payload()
		if err != nil {
			return nil, err
		}
		switch payload := payload.(type) {
		case session.MessageRecord:
			restored, err := llmMessageFromSessionRecord(payload)
			if err != nil {
				return nil, err
			}
			items = append(items, llm.ItemsFromMessages([]llm.Message{restored})...)
		case session.HistoryReplacementRecord:
			replacement, err := historyReplacementPayloadFromSessionRecord(payload)
			if err != nil {
				return nil, err
			}
			items = append(items[:0], replacement.Items...)
		}
	}
	return items, nil
}

func completePersistedWorkflowAssignment(engine *Engine, message llm.Message) WorkflowAssignmentSteer {
	receipt, err := engine.steerWithCommitReceipt("", steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	))
	return completedWorkflowAssignmentSteer(receipt, err)
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
