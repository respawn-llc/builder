package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"

	"github.com/google/uuid"
)

func TestBoundaryAgendaCanonicalTransitions(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	firstOrigin := newTestAgentStepOrigin(t)
	secondOrigin := newTestAgentStepOrigin(t)

	tests := []struct {
		name string
		run  func(*testing.T, *boundaryAgenda)
	}{
		{
			name: "scope item is selected only at its matching Step Boundary",
			run: func(t *testing.T, agenda *boundaryAgenda) {
				item := &testBoundaryAgendaItem{
					id:          "steer",
					binding:     scopeBoundaryBinding(scopeID, firstOrigin),
					eligibility: boundaryEligibilityStep,
				}
				if err := agenda.accept(item); err != nil {
					t.Fatalf("accept: %v", err)
				}
				if selected := agenda.selectNext(
					stepBoundarySelection(scopeID, secondOrigin),
				); selected != nil {
					t.Fatalf("selected stale item: %+v", selected)
				}
				if selected := agenda.selectNext(
					stepBoundarySelection(scopeID, firstOrigin),
				); selected != item {
					t.Fatalf("selected = %p, want %p", selected, item)
				}
				if got := agenda.pending(); len(got) != 0 {
					t.Fatalf("pending after selection = %d, want 0", len(got))
				}
			},
		},
		{
			name: "Queue waits for Agent Turn boundary",
			run: func(t *testing.T, agenda *boundaryAgenda) {
				item := &testBoundaryAgendaItem{
					id:          "queue",
					binding:     scopeBoundaryBinding(scopeID, firstOrigin),
					eligibility: boundaryEligibilityTurn,
				}
				if err := agenda.accept(item); err != nil {
					t.Fatalf("accept: %v", err)
				}
				if selected := agenda.selectNext(
					stepBoundarySelection(scopeID, firstOrigin),
				); selected != nil {
					t.Fatalf("Queue selected at Step Boundary: %+v", selected)
				}
				if selected := agenda.selectNext(
					turnBoundarySelection(scopeID, firstOrigin),
				); selected != item {
					t.Fatalf("selected = %p, want %p", selected, item)
				}
			},
		},
		{
			name: "runtime item survives source scope finalization",
			run: func(t *testing.T, agenda *boundaryAgenda) {
				item := &testBoundaryAgendaItem{
					id:          "workflow-assignment",
					binding:     runtimeBoundaryBinding(),
					eligibility: boundaryEligibilityIdle,
				}
				if err := agenda.accept(item); err != nil {
					t.Fatalf("accept: %v", err)
				}
				agenda.finalizeScope(scopeID, errBoundaryScopeFinalized)
				if item.settled != nil {
					t.Fatalf("runtime-bound item settled during source finalization: %v", item.settled)
				}
				if selected := agenda.selectNext(idleBoundarySelection()); selected != item {
					t.Fatalf("selected = %p, want %p", selected, item)
				}
			},
		},
		{
			name: "scope finalization settles only matching items",
			run: func(t *testing.T, agenda *boundaryAgenda) {
				matching := &testBoundaryAgendaItem{
					id:          "matching",
					binding:     scopeBoundaryBinding(scopeID, firstOrigin),
					eligibility: boundaryEligibilityStep,
				}
				otherScope := &testBoundaryAgendaItem{
					id:          "other",
					binding:     scopeBoundaryBinding(runtimeids.NewExecutionScopeID(), firstOrigin),
					eligibility: boundaryEligibilityStep,
				}
				if err := agenda.accept(matching); err != nil {
					t.Fatalf("accept matching: %v", err)
				}
				if err := agenda.accept(otherScope); err != nil {
					t.Fatalf("accept other: %v", err)
				}
				agenda.finalizeScope(scopeID, errBoundaryScopeFinalized)
				if !errors.Is(matching.settled, errBoundaryScopeFinalized) {
					t.Fatalf("matching settlement = %v", matching.settled)
				}
				if otherScope.settled != nil {
					t.Fatalf("other settlement = %v", otherScope.settled)
				}
				if got := agenda.pending(); len(got) != 1 || got[0] != otherScope {
					t.Fatalf("pending = %+v, want other item", got)
				}
			},
		},
		{
			name: "runtime close settles every pending item and rejects acceptance",
			run: func(t *testing.T, agenda *boundaryAgenda) {
				scopeItem := &testBoundaryAgendaItem{
					id:          "scope",
					binding:     scopeBoundaryBinding(scopeID, firstOrigin),
					eligibility: boundaryEligibilityStep,
				}
				runtimeItem := &testBoundaryAgendaItem{
					id:          "runtime",
					binding:     runtimeBoundaryBinding(),
					eligibility: boundaryEligibilityIdle,
				}
				if err := agenda.accept(scopeItem); err != nil {
					t.Fatalf("accept scope: %v", err)
				}
				if err := agenda.accept(runtimeItem); err != nil {
					t.Fatalf("accept runtime: %v", err)
				}
				agenda.close(errBoundaryRuntimeClosed)
				for _, item := range []*testBoundaryAgendaItem{scopeItem, runtimeItem} {
					if !errors.Is(item.settled, errBoundaryRuntimeClosed) {
						t.Fatalf("%s settlement = %v", item.id, item.settled)
					}
				}
				if err := agenda.accept(&testBoundaryAgendaItem{id: "late"}); !errors.Is(err, errBoundaryRuntimeClosed) {
					t.Fatalf("late acceptance = %v, want runtime closed", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, newBoundaryAgenda())
		})
	}
}

func TestHumanBoundaryAgendaPreservesSteerAndQueueEligibility(t *testing.T) {
	scopeID := runtimeids.NewExecutionScopeID()
	origin := newTestAgentStepOrigin(t)
	agenda := newBoundaryAgenda()
	steer := QueuedUserMessage{
		ID:      runtimeids.NewQueueItemID().String(),
		Message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("steer")},
	}
	queue := QueuedUserMessage{
		ID:      runtimeids.NewQueueItemID().String(),
		Message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("queue")},
	}

	settle := func(error) {}
	if err := agenda.acceptHuman(steer, scopeBoundaryBinding(scopeID, origin), boundaryEligibilityStep, settle); err != nil {
		t.Fatalf("accept Steer: %v", err)
	}
	if err := agenda.acceptHuman(queue, scopeBoundaryBinding(scopeID, origin), boundaryEligibilityTurn, settle); err != nil {
		t.Fatalf("accept Queue: %v", err)
	}
	if projected := agenda.pendingHuman(); len(projected) != 2 ||
		projected[0].ID != steer.ID ||
		projected[1].ID != queue.ID {
		t.Fatalf("pending human projection = %+v", projected)
	}

	selectedSteer := agenda.selectHuman(stepBoundarySelection(scopeID, origin))
	if len(selectedSteer) != 1 || selectedSteer[0].ID != steer.ID {
		t.Fatalf("Step Boundary selected = %+v, want Steer only", selectedSteer)
	}
	if projected := agenda.pendingHuman(); len(projected) != 1 || projected[0].ID != queue.ID {
		t.Fatalf("pending after Step Boundary = %+v, want Queue", projected)
	}

	selectedQueue := agenda.selectHuman(turnBoundarySelection(scopeID, origin))
	if len(selectedQueue) != 1 || selectedQueue[0].ID != queue.ID {
		t.Fatalf("Turn Boundary selected = %+v, want Queue", selectedQueue)
	}
	if projected := agenda.pendingHuman(); len(projected) != 0 {
		t.Fatalf("pending after Turn Boundary = %+v", projected)
	}
}

func TestHumanBoundaryAgendaTakesOnlyTheStoppedExactScope(t *testing.T) {
	agenda := newBoundaryAgenda()
	firstScope := runtimeids.NewExecutionScopeID()
	secondScope := runtimeids.NewExecutionScopeID()
	firstOrigin := serverapi.RuntimeStepOrigin{RunID: uuid.NewString(), StepID: uuid.NewString()}
	secondOrigin := serverapi.RuntimeStepOrigin{RunID: uuid.NewString(), StepID: uuid.NewString()}
	first := queuedUserMessageWithID(runtimeids.NewQueueItemID().String(), "first", "")
	second := queuedUserMessageWithID(runtimeids.NewQueueItemID().String(), "second", "")
	idle := queuedUserMessageWithID(runtimeids.NewQueueItemID().String(), "idle", "")

	for _, accepted := range []struct {
		message QueuedUserMessage
		binding boundaryAgendaBinding
	}{
		{message: first, binding: scopeBoundaryBinding(firstScope, firstOrigin)},
		{message: second, binding: scopeBoundaryBinding(secondScope, secondOrigin)},
		{message: idle, binding: runtimeBoundaryBinding()},
	} {
		if err := agenda.acceptHuman(
			accepted.message,
			accepted.binding,
			boundaryEligibilityStep,
			func(error) {},
		); err != nil {
			t.Fatalf("accept human item: %v", err)
		}
	}

	taken := agenda.takeHumanScope(firstScope)
	if len(taken) != 1 || taken[0].message.ID != first.ID {
		t.Fatalf("taken = %+v, want only first scope item %s", taken, first.ID)
	}
	pending := agenda.pendingHuman()
	if len(pending) != 2 || pending[0].ID != second.ID || pending[1].ID != idle.ID {
		t.Fatalf("pending = %+v, want second scope then runtime-bound item", pending)
	}
}

func TestWorkflowAssignmentBoundaryAgendaIsCanonicalAndRuntimeBound(t *testing.T) {
	agenda := newBoundaryAgenda()
	scopeID := runtimeids.NewExecutionScopeID()
	origin := newTestAgentStepOrigin(t)
	steer := newWorkflowAssignmentSteer()
	item := newWorkflowAssignmentAgendaItem(
		steerMessagesWithPersistenceIntent(
			steeringPriorityRuntimeContext,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{
				Role:    llm.RoleDeveloper,
				Content: textutil.Value("next workflow assignment"),
			}},
		),
		steer,
	)

	if err := agenda.accept(item); err != nil {
		t.Fatalf("accept workflow assignment: %v", err)
	}
	if pending := pendingWorkflowAssignmentsForTest(agenda); len(pending) != 1 || pending[0] != item {
		t.Fatalf("pending Workflow assignments = %+v, want canonical item %p", pending, item)
	}

	agenda.finalizeScope(scopeID, errBoundaryScopeFinalized)
	if pending := pendingWorkflowAssignmentsForTest(agenda); len(pending) != 1 || pending[0] != item {
		t.Fatalf("source-scope finalization changed Workflow assignment projection: %+v", pending)
	}

	if selected := agenda.selectNext(stepBoundarySelection(scopeID, origin)); selected != item {
		t.Fatalf("Step Boundary selected = %p, want Workflow assignment %p", selected, item)
	}
	if pending := pendingWorkflowAssignmentsForTest(agenda); len(pending) != 0 {
		t.Fatalf("pending Workflow assignments after selection = %+v", pending)
	}

	closingSteer := newWorkflowAssignmentSteer()
	closingItem := newWorkflowAssignmentAgendaItem(item.intent, closingSteer)
	if err := agenda.accept(closingItem); err != nil {
		t.Fatalf("accept closing Workflow assignment: %v", err)
	}
	agenda.close(errBoundaryRuntimeClosed)
	if receipt, err := closingSteer.Wait(t.Context()); !errors.Is(err, errBoundaryRuntimeClosed) || receipt.Committed {
		t.Fatalf("runtime-close Workflow assignment settlement = %+v, %v", receipt, err)
	}
}

func pendingWorkflowAssignmentsForTest(agenda *boundaryAgenda) []*workflowAssignmentAgendaItem {
	pending := make([]*workflowAssignmentAgendaItem, 0)
	for _, entry := range agenda.pending() {
		assignment, ok := entry.(*workflowAssignmentAgendaItem)
		if ok {
			pending = append(pending, assignment)
		}
	}
	return pending
}

type testBoundaryAgendaItem struct {
	id          boundaryAgendaItemID
	binding     boundaryAgendaBinding
	eligibility boundaryEligibility
	settled     error
	order       uint64
}

func (i *testBoundaryAgendaItem) agendaID() boundaryAgendaItemID {
	return i.id
}

func (i *testBoundaryAgendaItem) agendaBinding() boundaryAgendaBinding {
	return i.binding
}

func (i *testBoundaryAgendaItem) agendaEligibility() boundaryEligibility {
	return i.eligibility
}

func (i *testBoundaryAgendaItem) agendaOrder() uint64 {
	return i.order
}

func (i *testBoundaryAgendaItem) setAgendaOrder(order uint64) {
	i.order = order
}

func (i *testBoundaryAgendaItem) settleBoundaryAgenda(err error) {
	i.settled = err
}

func newTestAgentStepOrigin(t *testing.T) serverapi.RuntimeStepOrigin {
	t.Helper()
	origin := serverapi.RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: uuid.NewString(),
	}
	if err := origin.Validate(); err != nil {
		t.Fatal(err)
	}
	return origin
}
