package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestBoundaryLongSelectionContractTransfersOneImmutableWorkAndSettlesOnce(t *testing.T) {
	agenda := newBoundaryAgenda()
	first := &testLongBoundaryAgendaItem{
		testBoundaryAgendaItem: testBoundaryAgendaItem{
			id:          "first",
			binding:     runtimeBoundaryBinding(),
			eligibility: boundaryEligibilityIdle,
		},
	}
	second := &testLongBoundaryAgendaItem{
		testBoundaryAgendaItem: testBoundaryAgendaItem{
			id:          "second",
			binding:     runtimeBoundaryBinding(),
			eligibility: boundaryEligibilityIdle,
		},
	}
	if err := agenda.accept(first); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	if err := agenda.accept(second); err != nil {
		t.Fatalf("accept second: %v", err)
	}

	orchestrator := &boundaryLongOrchestrator{}
	selected, err := orchestrator.selectNext(agenda, idleBoundarySelection())
	if err != nil {
		t.Fatalf("select first: %v", err)
	}
	if selected != first {
		t.Fatalf("selected = %T %p, want first %p", selected, selected, first)
	}
	if duplicate, err := orchestrator.selectNext(agenda, idleBoundarySelection()); err != nil || duplicate != nil {
		t.Fatalf("duplicate selection = %v, %v", duplicate, err)
	}
	if pending := agenda.pending(); len(pending) != 1 || pending[0] != second {
		t.Fatalf("pending after transfer = %+v, want second only", pending)
	}

	settlement := errors.New("provider failed")
	settled, err := orchestrator.settle(boundaryLongWorkResult{id: first.id, err: settlement})
	if err != nil {
		t.Fatalf("settle first: %v", err)
	}
	if settled != first ||
		first.settlements != 1 ||
		!errors.Is(first.settlement, settlement) {
		t.Fatalf(
			"settled work = %p count=%d error=%v",
			settled,
			first.settlements,
			first.settlement,
		)
	}
	if _, err := orchestrator.settle(boundaryLongWorkResult{id: first.id}); err == nil {
		t.Fatal("duplicate result unexpectedly settled")
	}
	if first.settlements != 1 {
		t.Fatalf("duplicate result settlements = %d, want 1", first.settlements)
	}

	selected, err = orchestrator.selectNext(agenda, idleBoundarySelection())
	if err != nil || selected != second {
		t.Fatalf("select second = %v, %v", selected, err)
	}
}

func TestBoundaryLongSelectionLifecycleContract(t *testing.T) {
	runBoundaryLongSelectionLifecycleContract(t, boundaryLongSelectionContractAdapter{
		newItem: func(id boundaryAgendaItemID) boundaryLongSelectionContractItem {
			return &testLongBoundaryAgendaItem{
				testBoundaryAgendaItem: testBoundaryAgendaItem{
					id:          id,
					binding:     runtimeBoundaryBinding(),
					eligibility: boundaryEligibilityIdle,
				},
			}
		},
	})
}

func TestRuntimeBoundLongHandoffFailureSettlesSelectionAndRunsLaterAgendaWork(t *testing.T) {
	for _, test := range []struct {
		name     string
		launcher RuntimeBoundExecutionLauncher
		wantErr  error
	}{
		{
			name:     "execution launch failure",
			launcher: abortingRuntimeBoundTestLauncher{cause: ErrEngineClosed},
			wantErr:  ErrEngineClosed,
		},
		{
			name:     "cancellation before runner delivery",
			launcher: abortingRuntimeBoundTestLauncher{cause: context.Canceled},
			wantErr:  context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := mustNewTestEngine(
				t,
				mustCreateTestSession(t),
				&fakeClient{},
				tools.NewRegistry(),
				Config{Model: "gpt-5"},
			)
			first := newRuntimeBoundTestLongItem("first")
			second, err := newBackgroundNoticeAgendaItem(llm.Message{
				Role:    llm.RoleDeveloper,
				Content: textutil.Value("later background work"),
			})
			if err != nil {
				t.Fatalf("create second item: %v", err)
			}
			if err := engine.boundaryAgenda.accept(first); err != nil {
				t.Fatalf("accept first: %v", err)
			}
			if err := engine.boundaryAgenda.accept(second); err != nil {
				t.Fatalf("accept second: %v", err)
			}
			_, submitErr := submitRuntimeEvent(
				engine,
				struct{}{},
				func(admission runtimeEventAdmission, _ struct{}) (struct{}, error) {
					return struct{}{}, engine.launchNextRuntimeBoundLongWork(
						admission,
						test.launcher,
					)
				},
			)
			if submitErr != nil {
				t.Fatalf("launch first: %v", submitErr)
			}
			settlement := first.awaitSettlement(t)
			if !errors.Is(settlement, test.wantErr) {
				t.Fatalf("first settlement = %v, want %v", settlement, test.wantErr)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				settled, err := submitRuntimeEvent(
					engine,
					struct{}{},
					func(runtimeEventAdmission, struct{}) (bool, error) {
						return second.settled && engine.longBoundary.selected == nil, nil
					},
				)
				if err != nil {
					t.Fatalf("inspect later Agenda work: %v", err)
				}
				if settled {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("later Agenda work did not run after terminal handoff failure")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if engine.longBoundary.selected != nil {
				t.Fatalf("terminal selection = %T, want none", engine.longBoundary.selected)
			}
		})
	}
}

type boundaryLongSelectionContractItem interface {
	boundaryLongAgendaItem
	contractSettlementCount() int
	contractSettlementError() error
}

type boundaryLongSelectionContractAdapter struct {
	newItem func(boundaryAgendaItemID) boundaryLongSelectionContractItem
}

func runBoundaryLongSelectionLifecycleContract(
	t *testing.T,
	adapter boundaryLongSelectionContractAdapter,
) {
	t.Helper()
	t.Run("source scope finalization survival", func(t *testing.T) {
		agenda := newBoundaryAgenda()
		item := adapter.newItem("survivor")
		if err := agenda.accept(item); err != nil {
			t.Fatalf("accept: %v", err)
		}
		orchestrator := &boundaryLongOrchestrator{}
		if selected, err := orchestrator.selectNext(
			agenda,
			idleBoundarySelection(),
		); err != nil || selected.longWorkID() != item.agendaID() {
			t.Fatalf("selection after source cleanup = %v, %v", selected, err)
		}
		agenda.finalizeScope(runtimeids.NewExecutionScopeID(), errBoundaryScopeFinalized)
		if item.contractSettlementCount() != 0 ||
			orchestrator.selected.longWorkID() != item.agendaID() {
			t.Fatalf(
				"source cleanup = selected:%v settlements:%d",
				orchestrator.selected,
				item.contractSettlementCount(),
			)
		}
		orchestrator.close(context.Canceled)
	})
	for _, test := range []struct {
		name       string
		settlement error
	}{
		{name: "success"},
		{name: "failure", settlement: errors.New("long work failed")},
		{name: "selected Stop", settlement: errBoundaryScopeStopped},
		{name: "selected cancellation", settlement: context.Canceled},
		{name: "selected runtime close", settlement: errBoundaryRuntimeClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			agenda := newBoundaryAgenda()
			item := adapter.newItem(boundaryAgendaItemID(test.name))
			if err := agenda.accept(item); err != nil {
				t.Fatalf("accept: %v", err)
			}
			orchestrator := &boundaryLongOrchestrator{}
			if selected, err := orchestrator.selectNext(
				agenda,
				idleBoundarySelection(),
			); err != nil || selected.longWorkID() != item.agendaID() {
				t.Fatalf("select = %v, %v", selected, err)
			}
			if test.name == "success" || test.name == "failure" {
				if _, err := orchestrator.settle(boundaryLongWorkResult{
					id:  item.agendaID(),
					err: test.settlement,
				}); err != nil {
					t.Fatalf("settle: %v", err)
				}
			} else {
				orchestrator.close(test.settlement)
			}
			if orchestrator.selected != nil ||
				item.contractSettlementCount() != 1 ||
				!errors.Is(item.contractSettlementError(), test.settlement) {
				t.Fatalf(
					"terminal state = selected:%v settlements:%d error:%v",
					orchestrator.selected,
					item.contractSettlementCount(),
					item.contractSettlementError(),
				)
			}
		})
	}
	t.Run("pending runtime close", func(t *testing.T) {
		agenda := newBoundaryAgenda()
		item := adapter.newItem("pending-close")
		if err := agenda.accept(item); err != nil {
			t.Fatalf("accept: %v", err)
		}
		agenda.close(errBoundaryRuntimeClosed)
		if item.contractSettlementCount() != 1 ||
			!errors.Is(item.contractSettlementError(), errBoundaryRuntimeClosed) {
			t.Fatalf(
				"pending close settlements=%d error=%v",
				item.contractSettlementCount(),
				item.contractSettlementError(),
			)
		}
	})
}

type testLongBoundaryAgendaItem struct {
	testBoundaryAgendaItem
	settlements int
	settlement  error
}

func (i *testLongBoundaryAgendaItem) selectLongWork() boundaryLongWork {
	return i
}

func (i *testLongBoundaryAgendaItem) longWorkID() boundaryAgendaItemID {
	return i.id
}

func (*testLongBoundaryAgendaItem) runLongWork(context.Context, *Engine) error {
	return nil
}

func (i *testLongBoundaryAgendaItem) settleLongWork(err error) {
	i.settlements++
	i.settlement = err
}

func (i *testLongBoundaryAgendaItem) settleBoundaryAgenda(err error) {
	i.settleLongWork(err)
}

func (i *testLongBoundaryAgendaItem) contractSettlementCount() int {
	return i.settlements
}

func (i *testLongBoundaryAgendaItem) contractSettlementError() error {
	return i.settlement
}

type abortingRuntimeBoundTestLauncher struct {
	cause error
}

func (l abortingRuntimeBoundTestLauncher) LaunchRuntimeBoundExecution(
	_ runtimecommand.Admission,
	_ func(context.Context, *Engine) error,
	abort func(error),
) error {
	go abort(l.cause)
	return nil
}

type runtimeBoundTestLongItem struct {
	testBoundaryAgendaItem
	settled chan error
}

func newRuntimeBoundTestLongItem(id boundaryAgendaItemID) *runtimeBoundTestLongItem {
	return &runtimeBoundTestLongItem{
		testBoundaryAgendaItem: testBoundaryAgendaItem{
			id:          id,
			binding:     runtimeBoundaryBinding(),
			eligibility: boundaryEligibilityIdle,
		},
		settled: make(chan error, 1),
	}
}

func (i *runtimeBoundTestLongItem) selectLongWork() boundaryLongWork {
	return i
}

func (i *runtimeBoundTestLongItem) longWorkID() boundaryAgendaItemID {
	return i.id
}

func (*runtimeBoundTestLongItem) runLongWork(context.Context, *Engine) error {
	return nil
}

func (i *runtimeBoundTestLongItem) settleLongWork(err error) {
	i.settled <- err
}

func (i *runtimeBoundTestLongItem) settleBoundaryAgenda(err error) {
	i.settleLongWork(err)
}

func (i *runtimeBoundTestLongItem) completeRuntimeBoundLongWork(
	engine *Engine,
	err error,
) {
	engine.submitBoundaryLongWorkResult(i.id, err)
}

func (i *runtimeBoundTestLongItem) awaitSettlement(t *testing.T) error {
	t.Helper()
	select {
	case err := <-i.settled:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %q settlement", i.id)
		return nil
	}
}
