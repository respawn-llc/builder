package runtime

import (
	"errors"
	"sync"

	"core/server/session"
)

type agentStepBoundaryFinalizer struct {
	engine             *Engine
	mu                 sync.Mutex
	open               bool
	dispatched         bool
	capturing          bool
	committed          bool
	aborted            bool
	hadPendingRecovery bool
	stagedPayloads     []session.EventRecordPayload
	transientEntries   []ChatEntry
	detachedManual     []*pendingManualCompaction
	receipt            session.CommitReceipt
	err                error
}

func newAgentStepBoundaryFinalizer(engine *Engine) *agentStepBoundaryFinalizer {
	return &agentStepBoundaryFinalizer{engine: engine}
}

func (f *agentStepBoundaryFinalizer) Open() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.open = true
	f.dispatched = false
	f.capturing = false
	f.committed = false
	f.aborted = false
	f.hadPendingRecovery = false
	f.stagedPayloads = nil
	f.transientEntries = nil
	f.detachedManual = nil
	f.receipt = session.CommitReceipt{}
	f.err = nil
	f.mu.Unlock()
}

func (f *agentStepBoundaryFinalizer) MarkDispatched() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.open && !f.committed && !f.aborted {
		f.dispatched = true
		f.capturing = true
		f.engine.compactionRuntimeState().manualBoundaryCoordinator().beginGeneration()
	}
	f.mu.Unlock()
}

func (f *agentStepBoundaryFinalizer) Capturing() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open && f.capturing && !f.committed && !f.aborted
}

func (f *agentStepBoundaryFinalizer) Stage(payload session.EventRecordPayload) error {
	if f == nil {
		return errors.New("agent step boundary finalizer is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return errors.New("agent step boundary is not accepting durable payloads")
	}
	f.stagedPayloads = append(f.stagedPayloads, payload)
	return nil
}

func (f *agentStepBoundaryFinalizer) StageChatEntries(stepID string, entries []ChatEntry) {
	if f == nil || len(entries) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return
	}
	for _, entry := range entries {
		entry.StepID = stepID
		f.transientEntries = append(f.transientEntries, entry)
	}
}

func (f *agentStepBoundaryFinalizer) TransientChatEntries() []ChatEntry {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return nil
	}
	return append([]ChatEntry(nil), f.transientEntries...)
}

func (f *agentStepBoundaryFinalizer) Dispatched() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dispatched
}

func (f *agentStepBoundaryFinalizer) Committed() (session.CommitReceipt, error, bool) {
	if f == nil {
		return session.CommitReceipt{}, nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.receipt, f.err, f.committed
}

func (f *agentStepBoundaryFinalizer) HadPendingRecovery() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hadPendingRecovery
}

func (f *agentStepBoundaryFinalizer) Abort(err error) error {
	if f == nil {
		return errors.New("agent step boundary finalizer is required")
	}
	f.mu.Lock()
	if !f.open {
		f.mu.Unlock()
		return errors.New("agent step boundary generation is not open")
	}
	if f.committed {
		err := f.err
		f.mu.Unlock()
		return err
	}
	if f.aborted {
		err := f.err
		f.mu.Unlock()
		return err
	}
	hasStagedPayloads := len(f.stagedPayloads) > 0
	f.aborted = true
	f.capturing = false
	f.detachedManual = f.engine.compactionRuntimeState().manualBoundaryCoordinator().sealAndTake()
	f.err = err
	f.mu.Unlock()
	if hasStagedPayloads {
		err = errors.Join(err, f.engine.restoreTransientAgentStepProjection())
		f.mu.Lock()
		f.err = err
		f.mu.Unlock()
	}
	return err
}

func (f *agentStepBoundaryFinalizer) Commit(
	stepID string,
	payloads []session.EventRecordPayload,
) (session.CommitReceipt, error) {
	if f == nil || f.engine == nil {
		return session.CommitReceipt{}, errors.New("agent step boundary finalizer is required")
	}
	f.mu.Lock()
	if !f.open {
		f.mu.Unlock()
		return session.CommitReceipt{}, errors.New("agent step boundary generation is not open")
	}
	if f.committed || f.aborted {
		receipt, err := f.receipt, f.err
		f.mu.Unlock()
		return receipt, err
	}
	f.capturing = false
	stagedPayloads := append([]session.EventRecordPayload(nil), f.stagedPayloads...)
	f.detachedManual = f.engine.compactionRuntimeState().manualBoundaryCoordinator().sealAndTake()
	f.mu.Unlock()

	hadPendingRecovery := f.engine.pendingModelRecoveryForStep(stepID)
	receipt := session.CommitReceipt{}
	allPayloads := append(stagedPayloads, payloads...)
	err := f.engine.steer(stepID, steerAgentStepFinalizationIntent(allPayloads, &receipt, len(stagedPayloads) > 0))

	f.mu.Lock()
	f.hadPendingRecovery = hadPendingRecovery
	f.committed = true
	f.receipt = receipt
	f.err = err
	f.mu.Unlock()
	if !receipt.Committed && len(stagedPayloads) > 0 {
		restoreErr := f.engine.restoreTransientAgentStepProjection()
		err = errors.Join(err, restoreErr)
		f.mu.Lock()
		f.err = err
		f.mu.Unlock()
	}
	return receipt, err
}

func (f *agentStepBoundaryFinalizer) Complete(receipt session.CommitReceipt) {
	if f == nil || f.engine == nil {
		return
	}
	if receipt.Committed {
		f.engine.compactionRuntimeState().SetManualCompactionEligible(true)
	}
}

func (f *agentStepBoundaryFinalizer) TakeDetachedManual() []*pendingManualCompaction {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := f.detachedManual
	f.detachedManual = nil
	return entries
}
