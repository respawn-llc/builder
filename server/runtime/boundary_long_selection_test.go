package runtime

import (
	"context"
	"errors"
	"testing"

	"core/shared/runtimeids"
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
