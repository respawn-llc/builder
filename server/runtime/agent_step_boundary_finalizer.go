package runtime

import (
	"errors"
	"sync"

	"core/server/llm"
	"core/server/session"
	"github.com/google/uuid"
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
	stagedFinalPayload *session.EventRecordPayload
	stagedFinalMessage *llm.Message
	deferredStream     *deferredAssistantStreamCleanup
	detachedManual     []*pendingManualCompaction
	receipt            session.CommitReceipt
	err                error
}

type deferredAssistantStreamCleanup struct {
	metadata            *AssistantStreamMetadata
	streamID            *uuid.UUID
	abortReason         *AssistantStreamAbortReason
	finalizeAssistant   bool
	committedEntryStart int
}

func cloneAssistantStreamAbortReason(reason *AssistantStreamAbortReason) *AssistantStreamAbortReason {
	if reason == nil {
		return nil
	}
	copyReason := *reason
	return &copyReason
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
	f.stagedFinalPayload = nil
	f.stagedFinalMessage = nil
	f.deferredStream = nil
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

func (f *agentStepBoundaryFinalizer) ArmGeneration() {
	if f == nil || f.engine == nil {
		return
	}
	f.mu.Lock()
	if f.open && !f.committed && !f.aborted {
		f.engine.compactionRuntimeState().manualBoundaryCoordinator().armNextGeneration()
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

func (f *agentStepBoundaryFinalizer) StageFinalAssistant(message llm.Message, payload session.EventRecordPayload) error {
	if f == nil {
		return errors.New("agent step boundary finalizer is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return errors.New("agent step boundary is not accepting final assistant message")
	}
	if f.stagedFinalMessage != nil {
		return errors.New("agent step boundary already has a final assistant message")
	}
	copyMessage := message
	f.stagedFinalMessage = &copyMessage
	copyPayload := payload
	f.stagedFinalPayload = &copyPayload
	return nil
}

func (f *agentStepBoundaryFinalizer) StagedFinalAssistantMessage() *llm.Message {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stagedFinalMessage == nil {
		return nil
	}
	copyMessage := *f.stagedFinalMessage
	return &copyMessage
}

func (f *agentStepBoundaryFinalizer) HasStagedFinalAssistant() bool {
	return f.StagedFinalAssistantMessage() != nil
}

func (f *agentStepBoundaryFinalizer) DeferStreamingAssistantCleanup(
	metadata *AssistantStreamMetadata,
	streamID *uuid.UUID,
	abortReason *AssistantStreamAbortReason,
	finalizeAssistant bool,
	committedEntryStart int,
) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.open && f.capturing && !f.committed && !f.aborted {
		f.deferredStream = &deferredAssistantStreamCleanup{
			metadata:            cloneAssistantStreamMetadata(metadata),
			streamID:            cloneTranscriptStreamID(streamID),
			abortReason:         cloneAssistantStreamAbortReason(abortReason),
			finalizeAssistant:   finalizeAssistant,
			committedEntryStart: committedEntryStart,
		}
	}
	f.mu.Unlock()
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
	f.aborted = true
	f.capturing = false
	coordinator := f.engine.compactionRuntimeState().manualBoundaryCoordinator()
	coordinator.abortArmedGeneration(err)
	f.detachedManual = coordinator.sealAndTake()
	f.err = err
	f.mu.Unlock()
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
	var stagedPayloads []session.EventRecordPayload
	if f.stagedFinalPayload != nil {
		stagedPayloads = []session.EventRecordPayload{*f.stagedFinalPayload}
	}
	deferredStream := f.deferredStream
	f.detachedManual = f.engine.compactionRuntimeState().manualBoundaryCoordinator().sealAndTake()
	f.mu.Unlock()

	hadPendingRecovery := f.engine.pendingModelRecoveryForStep(stepID)
	receipt := session.CommitReceipt{}
	allPayloads := append(stagedPayloads, payloads...)
	err := f.engine.steer(stepID, steerAgentStepFinalizationIntent(allPayloads, &receipt, deferredStream))

	f.mu.Lock()
	f.hadPendingRecovery = hadPendingRecovery
	f.committed = true
	f.receipt = receipt
	f.err = err
	f.mu.Unlock()
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
