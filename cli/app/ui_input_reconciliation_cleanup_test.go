package app

import (
	"errors"
	"testing"

	"core/shared/clientui"
)

type cachedMainViewRuntimeClient struct {
	*sessionRuntimeClient
	view clientui.RuntimeMainView
}

func (c cachedMainViewRuntimeClient) CachedMainView() (clientui.RuntimeMainView, bool) {
	return c.view, true
}

func (c cachedMainViewRuntimeClient) sessionRuntimeBoundary() {}

func (c cachedMainViewRuntimeClient) RefreshMainViewWithPendingRefs([]clientui.RuntimeOperationRef) (clientui.RuntimeMainView, error) {
	return c.view, nil
}

func (c cachedMainViewRuntimeClient) InterruptWithPendingRefs([]clientui.RuntimeOperationRef) error {
	return errors.New("interrupt not used by cleanup test")
}

func TestInterruptCleanupDoesNotRestoreCommittedDirectSubmit(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationCommitted)

	restore, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if restore || ambiguous {
		t.Fatalf("restore=%t ambiguous=%t, want committed input left unrestored", restore, ambiguous)
	}
}

func TestInterruptCleanupIgnoresTranscriptTextFlushWithoutReconciliation(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationUnknown)
	m.activeSubmit.text = "same visible text"
	m.markActiveSubmitFlushed(clientui.Event{
		Kind:        clientui.EventUserMessageFlushed,
		UserMessage: "same visible text",
	})

	restore, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if !restore || !ambiguous {
		t.Fatalf("restore=%t ambiguous=%t, want text flush ignored and typed reconciliation to preserve ambiguous input", restore, ambiguous)
	}
}

func TestInterruptControlResponseIdleAcknowledgesPendingInterrupt(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationCommitted)
	m.setRuntimeActivityBusyForTest(true)
	m.setPendingInterrupt(true)
	m.runtimeControlToken = 1
	m.runtimeControlTokens = map[runtimeControlOperation]uint64{runtimeControlInterrupt: 1}
	m.engine = cachedMainViewRuntimeClient{view: clientui.RuntimeMainView{
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}),
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{
			Version: clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2},
			Operations: []clientui.RuntimeInputReconciliation{{
				Version:      clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2},
				OperationRef: ref,
				State:        clientui.RuntimeInputReconciliationCommitted,
			}},
		},
	}}

	_ = m.applyRuntimeControlDone(runtimeControlDoneMsg{token: 1, operation: runtimeControlInterrupt})

	if m.hasPendingInterrupt() {
		t.Fatal("pending interrupt was not acknowledged from interrupt response idle snapshot")
	}
	if m.isBusy() {
		t.Fatal("runtime stayed busy after interrupt response idle snapshot")
	}
	if m.input != "" {
		t.Fatalf("committed submit restored input = %q, want empty", m.input)
	}
}

func TestRuntimeActivityIdleDoesNotAcknowledgePendingInterruptBeforeInputReconciliation(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationCommitted)
	m.setPendingInterrupt(true)
	m.engine = cachedMainViewRuntimeClient{view: clientui.RuntimeMainView{
		Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}),
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(
			clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1},
		),
	}}

	cmd := (uiRuntimeAdapter{model: m}).reconcileInterruptFromRuntimeActivity(clientui.Event{
		Kind:            clientui.EventRuntimeActivityChanged,
		RuntimeActivity: &clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle},
	})

	if !m.hasPendingInterrupt() {
		t.Fatal("pending interrupt was acknowledged before reconciliation was available")
	}
	if cmd == nil {
		t.Fatal("expected reconciliation main-view refresh command")
	}
}

func TestInterruptCleanupRestoresCanceledQueuedMessage(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: "server-queue-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)

	restore, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if !restore || ambiguous {
		t.Fatalf("restore=%t ambiguous=%t, want exact canceled input restoration", restore, ambiguous)
	}
}

func TestPendingRuntimeOperationRefsIncludesManualCompactUntilDone(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil

	_ = m.inputController().startCompactionWithOrigin("", uiCompactionOriginManual)

	refs := m.pendingRuntimeOperationRefs()
	if len(refs) != 1 {
		t.Fatalf("pending refs = %+v, want one compact ref", refs)
	}
	if refs[0].Kind != clientui.RuntimeOperationKindCompact || refs[0].ClientRequestID == "" {
		t.Fatalf("pending compact ref = %+v, want compact operation identity", refs[0])
	}

	next, _ := m.inputController().handleCompactDone(compactDoneMsg{})
	updated := next.(*uiModel)
	if refs := updated.pendingRuntimeOperationRefs(); len(refs) != 0 {
		t.Fatalf("pending refs after compact done = %+v, want cleared", refs)
	}
}

func TestCompactDoneDoesNotFabricateRuntimeIdle(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setRuntimeActivityBusyForTest(true)
	m.addPendingRuntimeOperation(clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "compact-1"})

	next, _ := m.inputController().handleCompactDone(compactDoneMsg{})
	updated := next.(*uiModel)
	if !updated.runtimeActivityBusy() {
		t.Fatal("compact completion fabricated runtime idle over server-owned running activity")
	}
	if updated.activity == uiActivityIdle {
		t.Fatal("compact completion changed visible activity to idle while server-owned runtime activity is running")
	}
	if refs := updated.pendingRuntimeOperationRefs(); len(refs) != 0 {
		t.Fatalf("pending refs after compact done = %+v, want local compact ref cleared", refs)
	}
}

func TestPendingRuntimeOperationRefsIncludesQueuedMessageServerID(t *testing.T) {
	m := &uiModel{
		uiInputFeatureState: uiInputFeatureState{
			injectedQueue: []injectedRuntimeQueueItem{{
				ServerID:        "server-queue-1",
				ClientRequestID: "client-queue-1",
				State:           injectedRuntimeQueueEnqueued,
			}},
		},
	}

	refs := m.pendingRuntimeOperationRefs()
	if len(refs) != 1 {
		t.Fatalf("pending refs = %+v, want queued-message ref", refs)
	}
	if refs[0].Kind != clientui.RuntimeOperationKindQueuedMessage || refs[0].QueueItemID != "server-queue-1" {
		t.Fatalf("queued-message ref = %+v, want server queue identity", refs[0])
	}
}

func TestPendingRuntimeOperationRefsIncludesQueuedMessageClientIDBeforeCreate(t *testing.T) {
	m := &uiModel{
		uiInputFeatureState: uiInputFeatureState{
			injectedQueue: []injectedRuntimeQueueItem{{
				ClientRequestID: "client-queue-1",
				State:           injectedRuntimeQueuePendingCreate,
			}},
		},
	}

	refs := m.pendingRuntimeOperationRefs()
	if len(refs) != 1 {
		t.Fatalf("pending refs = %+v, want queued-message create ref", refs)
	}
	if refs[0].Kind != clientui.RuntimeOperationKindQueuedMessage || refs[0].ClientRequestID != "client-queue-1" || refs[0].QueueItemID != "" {
		t.Fatalf("queued-message ref = %+v, want client request identity before server queue item exists", refs[0])
	}
}

func TestRuntimeInterruptCommandUsesRefsCapturedAtDispatch(t *testing.T) {
	client := &runtimeControlFakeClient{}
	submitRef := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-before"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.activeSubmit = activeSubmitState{token: 1, text: "before", operationRef: submitRef, restoreOnInterrupt: true}

	cmd := m.runtimeControlCommand(runtimeControlInterrupt, "", false, "")
	if cmd == nil {
		t.Fatal("expected interrupt command")
	}
	m.activeSubmit.operationRef = clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-after"}

	_ = collectCmdMessages(t, cmd)
	if client.interruptTargetRef == nil || *client.interruptTargetRef != submitRef {
		t.Fatalf("interrupt target = %+v, want dispatch-time ref %+v", client.interruptTargetRef, submitRef)
	}
	if len(client.interruptPendingRefs) != 1 || client.interruptPendingRefs[0] != submitRef {
		t.Fatalf("pending refs = %+v, want dispatch-time ref %+v", client.interruptPendingRefs, submitRef)
	}
}

func TestActiveRuntimeInterruptDoesNotTargetQueuedMessageRef(t *testing.T) {
	client := &runtimeControlFakeClient{}
	queueRef := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queue-before-server-id"}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setRuntimeActivityBusyForTest(true)
	m.injectedQueue = []injectedRuntimeQueueItem{{
		ClientRequestID: queueRef.ClientRequestID,
		State:           injectedRuntimeQueuePendingCreate,
	}}

	cmd := m.runtimeControlCommand(runtimeControlInterrupt, "", false, "")
	if cmd == nil {
		t.Fatal("expected interrupt command")
	}

	_ = collectCmdMessages(t, cmd)
	if client.interruptTargetRef != nil {
		t.Fatalf("interrupt target = %+v, want untargeted active-run interrupt", *client.interruptTargetRef)
	}
	if len(client.interruptPendingRefs) != 1 || client.interruptPendingRefs[0] != queueRef {
		t.Fatalf("pending refs = %+v, want queued ref for reconciliation", client.interruptPendingRefs)
	}
}

func TestInterruptCleanupPreservesUnknownShellWithDiagnostic(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "shell-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationUnknown)

	restore, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if !restore || !ambiguous {
		t.Fatalf("restore=%t ambiguous=%t, want conservative unknown preservation with diagnostic", restore, ambiguous)
	}
}

func TestInterruptCleanupPreservesEvictedCompactionWithDiagnostic(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "compact-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationEvicted)

	restore, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if !restore || !ambiguous {
		t.Fatalf("restore=%t ambiguous=%t, want conservative evicted preservation with diagnostic", restore, ambiguous)
	}
}

func TestInterruptCleanupRestoresFailedPreSubmitCompaction(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: "pre-compact-1"}
	m := modelWithActiveSubmitReconciliation(ref, clientui.RuntimeInputReconciliationFailedWithRestore)

	restore, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if !restore || ambiguous {
		t.Fatalf("restore=%t ambiguous=%t, want exact failed pre-submit restoration", restore, ambiguous)
	}
}

func modelWithActiveSubmitReconciliation(ref clientui.RuntimeOperationRef, state clientui.RuntimeInputReconciliationState) *uiModel {
	version := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}
	return &uiModel{
		uiRuntimeFeatureState: uiRuntimeFeatureState{
			engine: cachedMainViewRuntimeClient{view: clientui.RuntimeMainView{
				InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{
					Version: version,
					Operations: []clientui.RuntimeInputReconciliation{{
						Version:      version,
						OperationRef: ref,
						State:        state,
					}},
				},
			}},
		},
		uiInputFeatureState: uiInputFeatureState{
			activeSubmit: activeSubmitState{
				token:              1,
				text:               "pending",
				operationRef:       ref,
				restoreOnInterrupt: true,
			},
		},
	}
}
