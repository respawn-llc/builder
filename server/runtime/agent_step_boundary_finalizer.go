package runtime

import (
	"errors"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"github.com/google/uuid"
)

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

type agentStepBoundaryFinalizer struct {
	engine                   *Engine
	mu                       sync.Mutex
	open                     bool
	dispatched               bool
	capturing                bool
	committed                bool
	aborted                  bool
	hadPendingRecovery       bool
	stagedPayloads           []session.EventRecordPayload
	stagedMessages           []llm.Message
	transientEntries         []ChatEntry
	deferredToolStarts       []Event
	stagedToolResults        []tools.Result
	stagedQueuedFlushes      []steeringQueuedUserMessageFlush
	deferredTranscriptUpdate bool
	deferredStreamCleanup    *deferredAssistantStreamCleanup
	detachedManual           []*pendingManualCompaction
	receipt                  session.CommitReceipt
	err                      error
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
	f.stagedMessages = nil
	f.transientEntries = nil
	f.deferredToolStarts = nil
	f.stagedToolResults = nil
	f.stagedQueuedFlushes = nil
	f.deferredTranscriptUpdate = false
	f.deferredStreamCleanup = nil
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

func (f *agentStepBoundaryFinalizer) StageMessage(message llm.Message) error {
	if f == nil {
		return errors.New("agent step boundary finalizer is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return errors.New("agent step boundary is not accepting messages")
	}
	f.stagedMessages = append(f.stagedMessages, message)
	return nil
}

func (f *agentStepBoundaryFinalizer) StagedMessages() []llm.Message {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Message(nil), f.stagedMessages...)
}

func (f *agentStepBoundaryFinalizer) StageChatEntries(stepID string, entries []ChatEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return
	}
	for _, entry := range entries {
		entry.StepID = stepID
		duplicate := false
		for _, existing := range f.transientEntries {
			if samePendingChatEntry(existing, entry) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		f.transientEntries = append(f.transientEntries, entry)
	}
}

func samePendingChatEntry(a, b ChatEntry) bool {
	return a.Role == b.Role &&
		a.ToolCallID == b.ToolCallID &&
		a.Text == b.Text &&
		a.CondensedText == b.CondensedText &&
		a.StepID == b.StepID
}

func (f *agentStepBoundaryFinalizer) StagedVisibleChatEntryCount() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, message := range f.stagedMessages {
		count += len(VisibleChatEntriesFromMessage(message))
	}
	return count
}

func (f *agentStepBoundaryFinalizer) TransientChatEntries() []ChatEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.open || !f.capturing || f.committed || f.aborted {
		return nil
	}
	return append([]ChatEntry(nil), f.transientEntries...)
}

func (f *agentStepBoundaryFinalizer) DeferCommittedTranscriptUpdate() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.open && f.capturing && !f.committed && !f.aborted {
		f.deferredTranscriptUpdate = true
	}
	f.mu.Unlock()
}

func (f *agentStepBoundaryFinalizer) DeferCommittedToolStart(event Event) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.open && f.capturing && !f.committed && !f.aborted {
		f.deferredToolStarts = append(f.deferredToolStarts, event)
	}
	f.mu.Unlock()
}

func (f *agentStepBoundaryFinalizer) StageToolCompletionResult(result tools.Result) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.open && f.capturing && !f.committed && !f.aborted {
		f.stagedToolResults = append(f.stagedToolResults, cloneToolResult(result))
	}
	f.mu.Unlock()
}

func (f *agentStepBoundaryFinalizer) StageQueuedUserFlush(flush steeringQueuedUserMessageFlush) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.open && f.capturing && !f.committed && !f.aborted {
		flush.payloadIndex = len(f.stagedPayloads) - 1
		f.stagedQueuedFlushes = append(f.stagedQueuedFlushes, steeringQueuedUserMessageFlush{
			payloadIndex: flush.payloadIndex,
			text:         flush.text,
			batch:        append([]string(nil), flush.batch...),
			queueItems:   append([]QueuedUserMessage(nil), flush.queueItems...),
		})
	}
	f.mu.Unlock()
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
		f.deferredStreamCleanup = &deferredAssistantStreamCleanup{
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
	queuedFlushes := append([]steeringQueuedUserMessageFlush(nil), f.stagedQueuedFlushes...)
	coordinator := f.engine.compactionRuntimeState().manualBoundaryCoordinator()
	coordinator.abortArmedGeneration(err)
	f.detachedManual = coordinator.sealAndTake()
	f.err = err
	f.mu.Unlock()
	f.restoreStagedQueuedFlushes(queuedFlushes)
	return err
}

func (f *agentStepBoundaryFinalizer) restoreStagedQueuedFlushes(flushes []steeringQueuedUserMessageFlush) {
	if f == nil || f.engine == nil || len(flushes) == 0 {
		return
	}
	f.engine.ensureOrchestrationCollaborators()
	for _, flush := range flushes {
		for _, item := range flush.queueItems {
			f.engine.unmarkQueuedUserInjectionForAutoDrain(item.ID)
			f.engine.messageFlow.QueueUserMessageWithID(item)
		}
	}
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
	deferredToolStarts := append([]Event(nil), f.deferredToolStarts...)
	stagedToolResults := append([]tools.Result(nil), f.stagedToolResults...)
	stagedQueuedFlushes := append([]steeringQueuedUserMessageFlush(nil), f.stagedQueuedFlushes...)
	deferredStreamCleanup := f.deferredStreamCleanup
	deferredTranscriptUpdate := f.deferredTranscriptUpdate
	f.detachedManual = f.engine.compactionRuntimeState().manualBoundaryCoordinator().sealAndTake()
	f.mu.Unlock()

	hadPendingRecovery := f.engine.pendingModelRecoveryForStep(stepID)
	receipt := session.CommitReceipt{}
	allPayloads := append(stagedPayloads, payloads...)
	err := f.engine.steer(stepID, steerAgentStepFinalizationIntent(allPayloads, &receipt, deferredToolStarts, stagedToolResults, stagedQueuedFlushes, deferredStreamCleanup, deferredTranscriptUpdate))
	if !receipt.Committed {
		f.restoreStagedQueuedFlushes(stagedQueuedFlushes)
	}

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
