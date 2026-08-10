package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/toolspec"

	"github.com/google/uuid"
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

type WorkflowAssignmentSteer struct {
	state *workflowAssignmentSteerState
}

type workflowAssignmentSteerState struct {
	done    chan struct{}
	once    sync.Once
	receipt session.CommitReceipt
	err     error
}

type workflowAssignmentAgendaItem struct {
	id     boundaryAgendaItemID
	intent steeringIntent
	steer  WorkflowAssignmentSteer
	order  uint64
}

func newWorkflowAssignmentAgendaItem(
	intent steeringIntent,
	steer WorkflowAssignmentSteer,
) *workflowAssignmentAgendaItem {
	return &workflowAssignmentAgendaItem{
		id:     boundaryAgendaItemID("workflow-assignment:" + uuid.NewString()),
		intent: intent,
		steer:  steer,
	}
}

func (i *workflowAssignmentAgendaItem) agendaID() boundaryAgendaItemID {
	return i.id
}

func (*workflowAssignmentAgendaItem) agendaBinding() boundaryAgendaBinding {
	return runtimeBoundaryBinding()
}

func (*workflowAssignmentAgendaItem) agendaEligibility() boundaryEligibility {
	return boundaryEligibilitySafe
}

func (i *workflowAssignmentAgendaItem) agendaOrder() uint64 {
	return i.order
}

func (i *workflowAssignmentAgendaItem) setAgendaOrder(order uint64) {
	i.order = order
}

func (i *workflowAssignmentAgendaItem) settleBoundaryAgenda(err error) {
	i.steer.complete(session.CommitReceipt{}, err)
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
	steer := newWorkflowAssignmentSteer()
	intent := steerMessagesWithPersistenceIntent(
		steeringPriorityRuntimeContext,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	)
	item := newWorkflowAssignmentAgendaItem(intent, steer)
	acceptedSteer, err := submitRuntimeEvent(
		e,
		item,
		e.acceptWorkflowAssignmentAgendaItem,
	)
	if err != nil {
		steer.complete(session.CommitReceipt{}, err)
		return steer, err
	}
	if acceptedSteer.state != steer.state {
		panic("Workflow assignment Runtime Event returned a different typed steer")
	}
	return steer, nil
}

func (e *Engine) acceptWorkflowAssignmentAgendaItem(
	admission runtimeEventAdmission,
	accepted *workflowAssignmentAgendaItem,
) (WorkflowAssignmentSteer, error) {
	if err := e.boundaryAgenda.accept(accepted); err != nil {
		return WorkflowAssignmentSteer{}, err
	}
	if !e.workflowAssignmentIdleEligible() {
		return accepted.steer, nil
	}
	if err := e.reduceIdleBoundary(admission); err != nil {
		if !e.boundaryAgenda.discard(accepted.id, err) {
			accepted.steer.complete(session.CommitReceipt{}, err)
		}
		return accepted.steer, nil
	}
	return accepted.steer, nil
}

func (e *Engine) workflowAssignmentIdleEligible() bool {
	return e.agentSteps.current == nil &&
		e.agentSteps.boundary == nil &&
		e.agentSteps.reducerGrant == nil
}

func (e *Engine) applyWorkflowAssignmentBoundary(
	admission runtimeEventAdmission,
	stepID *string,
	selection boundarySelection,
) (int, error) {
	applied := 0
	for {
		next := e.boundaryAgenda.peekNext(selection)
		if next == nil {
			return applied, nil
		}
		if _, ok := next.(*workflowAssignmentAgendaItem); !ok {
			return applied, nil
		}
		selected := e.boundaryAgenda.selectNext(selection)
		if selected == nil {
			return applied, nil
		}
		assignment, ok := selected.(*workflowAssignmentAgendaItem)
		if !ok {
			return applied, fmt.Errorf(
				"Workflow assignment reducer selected unexpected Boundary Agenda item %T",
				selected,
			)
		}
		if len(assignment.intent.items) != 1 {
			err := fmt.Errorf(
				"Workflow assignment steering requires exactly one item (items=%d)",
				len(assignment.intent.items),
			)
			assignment.steer.complete(session.CommitReceipt{}, err)
			return applied, err
		}
		receipt := session.CommitReceipt{}
		assignment.intent.items[0].commitReceipt = &receipt
		err := admission.applySteeringOptional(stepID, assignment.intent)
		if err == nil && !receipt.Committed {
			err = errors.New("workflow assignment message was not committed")
		}
		if err != nil {
			assignment.steer.complete(receipt, err)
			return applied, err
		}
		assignment.steer.complete(receipt, nil)
		applied++
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
