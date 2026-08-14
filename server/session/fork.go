package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

var errForkReplayBoundary = errors.New("fork replay boundary reached")

// forkReplayFlushEventCount and forkReplayFlushByteBudget bound how much of the
// parent conversation is buffered in memory before a chunk is flushed to the
// child store. Fork/clone stream the parent event log instead of materializing
// it so arbitrarily large histories never load fully into memory.
const (
	forkReplayFlushEventCount = 512
	forkReplayFlushByteBudget = 1 << 20
)

type forkReplayResourceSnapshot struct {
	BufferedRecords         int
	MaxBufferedRecords      int
	BufferedEncodedBytes    int
	MaxBufferedEncodedBytes int
	LargestRecordBytes      int
}

type forkReplayBatch struct {
	records []EventRecord
	stats   forkReplayResourceSnapshot
}

func newForkReplayBatch() *forkReplayBatch {
	return &forkReplayBatch{
		records: make([]EventRecord, 0, forkReplayFlushEventCount),
	}
}

func (b *forkReplayBatch) shouldFlushBefore(recordBytes int) bool {
	if b == nil || len(b.records) == 0 {
		return false
	}
	return len(b.records) >= forkReplayFlushEventCount ||
		recordBytes > forkReplayFlushByteBudget-b.stats.BufferedEncodedBytes
}

func (b *forkReplayBatch) add(record EventRecord, recordBytes int) error {
	if b == nil {
		return errors.New("fork replay batch is required")
	}
	if recordBytes <= 0 {
		return fmt.Errorf("fork replay record bytes must be positive: %d", recordBytes)
	}
	if b.shouldFlushBefore(recordBytes) {
		return fmt.Errorf(
			"fork replay batch must flush before retaining record %d: records=%d bytes=%d next=%d",
			record.Seq(),
			len(b.records),
			b.stats.BufferedEncodedBytes,
			recordBytes,
		)
	}
	b.records = append(b.records, record)
	b.stats.BufferedRecords = len(b.records)
	b.stats.BufferedEncodedBytes += recordBytes
	if b.stats.BufferedRecords > b.stats.MaxBufferedRecords {
		b.stats.MaxBufferedRecords = b.stats.BufferedRecords
	}
	if b.stats.BufferedEncodedBytes > b.stats.MaxBufferedEncodedBytes {
		b.stats.MaxBufferedEncodedBytes = b.stats.BufferedEncodedBytes
	}
	if recordBytes > b.stats.LargestRecordBytes {
		b.stats.LargestRecordBytes = recordBytes
	}
	return nil
}

func (b *forkReplayBatch) reset() {
	if b == nil {
		return
	}
	clear(b.records)
	b.records = b.records[:0]
	b.stats.BufferedRecords = 0
	b.stats.BufferedEncodedBytes = 0
}

func (b *forkReplayBatch) snapshot() forkReplayResourceSnapshot {
	if b == nil {
		return forkReplayResourceSnapshot{}
	}
	return b.stats
}

func (b *forkReplayBatch) validateDrained() error {
	stats := b.snapshot()
	maxAllowedBytes := forkReplayFlushByteBudget
	if stats.LargestRecordBytes > maxAllowedBytes {
		maxAllowedBytes = stats.LargestRecordBytes
	}
	if stats.BufferedRecords != 0 || stats.BufferedEncodedBytes != 0 {
		return fmt.Errorf("fork replay batch retained resources after replay: %+v", stats)
	}
	if stats.MaxBufferedRecords > forkReplayFlushEventCount ||
		stats.MaxBufferedEncodedBytes > maxAllowedBytes {
		return fmt.Errorf("fork replay resource budget exceeded: %+v", stats)
	}
	return nil
}

// ForkAtUserMessage creates a child session whose history is the parent's
// conversation up to (but excluding) the visible user message persisted at
// userMessageSeq. It returns the forked store and the 1-based ordinal of that
// user message among the parent's visible user messages (for naming/display).
func ForkAtUserMessage(parentLog MaterializedEventLog, userMessageSeq int64, forkName string, category sessioncontract.SessionCategory) (*Store, int, error) {
	if userMessageSeq <= 0 {
		return nil, 0, fmt.Errorf("user message seq must be >= 1")
	}
	return streamChildFromParent(parentLog, forkName, category, ChildContextOptions{
		InheritLockedContract: true,
		InheritContinuation:   true,
		InheritGoal:           true,
	}, userMessageSeq)
}

// CloneSession creates a child session that replays the parent's entire
// conversation history. Workflow compact-and-continue fan-out branches use this
// so each parallel continuation compacts its own isolated copy of the source
// conversation instead of mutating the shared source session.
func CloneSession(parentLog MaterializedEventLog, forkName string, category sessioncontract.SessionCategory) (*Store, error) {
	child, _, err := streamChildFromParent(parentLog, forkName, category, ChildContextOptions{
		InheritLockedContract: true,
		InheritContinuation:   true,
	}, 0)
	return child, err
}

// streamChildFromParent creates a child session and streams the parent event
// log into it in bounded chunks. A targetSeq > 0 stops replay just before the
// visible user message persisted at that sequence (fork-at-message); targetSeq
// == 0 clones everything. It returns the 1-based ordinal of the cut user
// message among the parent's visible user messages (0 when cloning).
func streamChildFromParent(
	parentLog MaterializedEventLog,
	forkName string,
	category sessioncontract.SessionCategory,
	contextOptions ChildContextOptions,
	targetSeq int64,
) (_ *Store, _ int, resultErr error) {
	parent, err := materializedForkParent(parentLog)
	if err != nil {
		return nil, 0, err
	}
	parentMeta := parent.Meta()
	containerDir := filepath.Dir(parent.Dir())
	child, err := newLazyWithStoreOptions(containerDir, parentMeta.WorkspaceContainer, parentMeta.WorkspaceRoot, category, parent.options)
	if err != nil {
		return nil, 0, err
	}
	child.eventLogCreationVersion = eventLogVersionPointer(parentLog.log.version)
	keepChild := false
	defer func() {
		if !keepChild {
			resultErr = errors.Join(resultErr, child.RemoveDurable())
		}
	}()
	if err := InitializeCreationContext(child, parent, SessionCreationSourcePreviousSession, contextOptions); err != nil {
		return nil, 0, err
	}

	child.mu.Lock()
	child.meta.Name = strings.TrimSpace(forkName)
	child.mu.Unlock()

	if err := child.EnsureDurable(); err != nil {
		return nil, 0, fmt.Errorf("persist fork child before replay: %w", err)
	}
	childLog, err := child.MaterializeEventLog()
	if err != nil {
		return nil, 0, fmt.Errorf("materialize fork child event log: %w", err)
	}
	derived, cutOrdinal, err := streamReplayIntoChild(parentLog, childLog, targetSeq)
	if err != nil {
		return nil, 0, fmt.Errorf("stream fork replay events: %w", err)
	}
	if targetSeq > 0 && cutOrdinal == 0 {
		return nil, 0, fmt.Errorf("user message seq %d is out of range", targetSeq)
	}
	if err := child.applyForkDerivedState(derived); err != nil {
		return nil, 0, fmt.Errorf("finalize fork replay: %w", err)
	}
	keepChild = true
	return child, cutOrdinal, nil
}

func materializedForkParent(parentLog MaterializedEventLog) (*Store, error) {
	parent := parentLog.store
	if parent == nil {
		return nil, fmt.Errorf("parent materialized event log is required")
	}
	if err := parentLog.ValidateOwner(parent); err != nil {
		return nil, fmt.Errorf("validate parent materialized event log: %w", err)
	}
	return parent, nil
}

// streamReplayIntoChild walks the parent event log and appends each event to the
// child in bounded chunks, folding replay-derived metadata incrementally. When
// targetSeq > 0 it stops just before the visible user message persisted at that
// sequence and returns that message's 1-based visible-user-message ordinal; it
// returns 0 when the target is not found (or when cloning the whole log).
func streamReplayIntoChild(parentLog MaterializedEventLog, childLog MaterializedEventLog, targetSeq int64) (replayDerivedState, int, error) {
	derived := replayDerivedState{}
	visibleUserCount := 0
	cutOrdinal := 0
	batch := newForkReplayBatch()
	var latestRollbackCandidate *rollbacktarget.CandidateLocator
	flush := func() error {
		if len(batch.records) == 0 {
			return nil
		}
		appended, err := childLog.appendReplayRecordsWithEndByteCursor(batch.records)
		if err != nil {
			return err
		}
		for index := range batch.records {
			visible, err := isForkVisibleUserMessage(batch.records[index])
			if err != nil {
				return err
			}
			if !visible {
				continue
			}
			latestRollbackCandidate = &rollbacktarget.CandidateLocator{
				UserMessageSeq:       appended.records[index].Seq(),
				CandidatePageEndByte: *appended.endByteCursor,
			}
		}
		batch.reset()
		return nil
	}
	walkErr := parentLog.WalkRecords(func(record EventRecord) error {
		visible, err := isForkVisibleUserMessage(record)
		if err != nil {
			return err
		}
		if visible {
			visibleUserCount++
			if targetSeq > 0 && record.Seq() == targetSeq {
				cutOrdinal = visibleUserCount
				return errForkReplayBoundary
			}
		}
		payload, err := record.Payload()
		if err != nil {
			return err
		}
		if replacement, ok := payload.(HistoryReplacementRecord); ok {
			if err := flush(); err != nil {
				return err
			}
			rebasedReplacement := rebaseHistoryReplacementRollbackCandidate(
				replacement,
				latestRollbackCandidate,
			)
			rebasedRecord, err := NewEventRecord(
				record.Seq(),
				record.StepID(),
				rebasedReplacement,
			)
			if err != nil {
				return err
			}
			record = rebasedRecord
		}
		recordBytes, err := replayRecordByteSizeForVersion(record, childLog.log.version)
		if err != nil {
			return err
		}
		if batch.shouldFlushBefore(recordBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		if err := derived.apply(record); err != nil {
			return err
		}
		if err := batch.add(record, recordBytes); err != nil {
			return err
		}
		if len(batch.records) >= forkReplayFlushEventCount ||
			batch.stats.BufferedEncodedBytes >= forkReplayFlushByteBudget {
			return flush()
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errForkReplayBoundary) {
		return derived, 0, walkErr
	}
	if err := flush(); err != nil {
		return derived, 0, err
	}
	if err := batch.validateDrained(); err != nil {
		return derived, 0, err
	}
	return derived, cutOrdinal, nil
}

func isForkVisibleUserMessage(record EventRecord) (bool, error) {
	payload, err := record.Payload()
	if err != nil {
		return false, err
	}
	message, ok := payload.(MessageRecord)
	return ok && hasVisibleUserMessageFields(
		message.Role,
		message.MessageType,
		message.Content,
	), nil
}

func rebaseHistoryReplacementRollbackCandidate(
	replacement HistoryReplacementRecord,
	locator *rollbacktarget.CandidateLocator,
) HistoryReplacementRecord {
	if locator == nil {
		replacement.LatestRollbackCandidate = nil
		return replacement
	}
	candidate := *locator
	replacement.LatestRollbackCandidate = &candidate
	return replacement
}

func replayRecordByteSize(record EventRecord) (int, error) {
	return replayRecordByteSizeForVersion(record, EventLogVersionV1)
}

func replayRecordByteSizeForVersion(record EventRecord, version int) (int, error) {
	payload, err := record.Payload()
	if err != nil {
		return 0, err
	}
	payload, err = projectEventPayloadForVersion(version, payload)
	if err != nil {
		return 0, err
	}
	projected, err := newEventRecord(record.Seq(), record.StepID(), payload, record.CommittedAtUnixMs())
	if err != nil && version == EventLogVersionV1 {
		projected, err = newEventRecord(record.Seq(), record.StepID(), payload, nil)
	}
	if err != nil {
		return 0, err
	}
	encoded, err := encodeEventRecordForVersion(version, projected)
	if err != nil {
		return 0, fmt.Errorf("encode replay record %d for bounded chunking: %w", record.Seq(), err)
	}
	return len(encoded) + 1, nil
}

func (s *Store) applyForkDerivedState(derived replayDerivedState) error {
	if err := s.SetHeadlessActive(derived.headlessActive); err != nil {
		return err
	}
	if err := s.SetCompactionSoonReminderIssued(derived.reminderIssued); err != nil {
		return err
	}
	return s.EnsureDurable()
}

func cloneLockedContract(in *LockedContract) *LockedContract {
	if in == nil {
		return nil
	}
	copyLocked := *in
	if len(in.EnabledTools) > 0 {
		copyLocked.EnabledTools = append([]string(nil), in.EnabledTools...)
	}
	if in.ToolPreambles != nil {
		toolPreambles := *in.ToolPreambles
		copyLocked.ToolPreambles = &toolPreambles
	}
	if in.WorkflowCompletionMode != nil {
		mode := *in.WorkflowCompletionMode
		copyLocked.WorkflowCompletionMode = &mode
	}
	copyLocked.ProviderContract.SupportsProviderVerbosity = textutil.Pointer(in.ProviderContract.SupportsProviderVerbosity)
	copyLocked.SystemPrompt = strings.TrimSpace(in.SystemPrompt)
	copyLocked.ReviewerPrompt = strings.TrimSpace(in.ReviewerPrompt)
	return &copyLocked
}

func cloneContinuationContext(in *ContinuationContext) *ContinuationContext {
	if in == nil {
		return nil
	}
	copyContext := *in
	if in.AgentRole != nil {
		role := *in.AgentRole
		copyContext.AgentRole = &role
	}
	return &copyContext
}

type ChildContextOptions struct {
	// InheritLockedContract preserves the parent's model/tool/prompt lock for
	// interactive child sessions. Headless subagent launches leave this false
	// so their first dispatch locks against role/config-provided settings.
	InheritLockedContract bool
	// InheritContinuation preserves parent continuation settings for
	// interactive children. Headless subagent launches leave this false so
	// parent role/base URL state cannot override the selected subagent config.
	InheritContinuation bool
	// InheritGoal preserves the parent's current goal for user-facing
	// Edit/Fork children. Complete-history workflow clones leave this false.
	InheritGoal bool
}

type SessionCreationSourceKind uint8

const (
	SessionCreationSourceIndependent SessionCreationSourceKind = iota
	SessionCreationSourcePreviousSession
	SessionCreationSourceParentAgent
)

// InitializeCreationContext atomically initializes immutable provenance and
// source-owned execution context before a fresh session becomes durable.
func InitializeCreationContext(child *Store, source *Store, kind SessionCreationSourceKind, opts ChildContextOptions) error {
	if child == nil {
		return fmt.Errorf("child store is required")
	}
	switch kind {
	case SessionCreationSourceIndependent:
		if source != nil {
			return fmt.Errorf("independent session creation cannot have a source")
		}
	case SessionCreationSourcePreviousSession, SessionCreationSourceParentAgent:
		if source == nil {
			return fmt.Errorf("session creation source is required")
		}
	default:
		return fmt.Errorf("session creation source kind is invalid")
	}
	var sourceMeta Meta
	if source != nil {
		sourceMeta = source.Meta()
	}
	child.mutationMu.Lock()
	child.mu.Lock()
	if child.persisted {
		child.mu.Unlock()
		child.mutationMu.Unlock()
		return fmt.Errorf("session creation context is immutable after durability")
	}
	child.meta.PreviousSessionID = nil
	child.meta.ParentAgentSessionID = nil
	if kind == SessionCreationSourceIndependent {
		child.meta.UpdatedAt = time.Now().UTC()
		child.mu.Unlock()
		child.mutationMu.Unlock()
		return nil
	}
	if opts.InheritLockedContract {
		child.meta.Locked = cloneLockedContract(sourceMeta.Locked)
	} else {
		child.meta.Locked = nil
	}
	child.meta.WorkspaceRoot = sourceMeta.WorkspaceRoot
	child.meta.WorkspaceContainer = sourceMeta.WorkspaceContainer
	child.meta.WorktreeReminder = CloneWorktreeReminderState(sourceMeta.WorktreeReminder)
	child.meta.UsageState = nil
	if opts.InheritGoal {
		child.meta.Goal = cloneGoalState(sourceMeta.Goal)
	} else {
		child.meta.Goal = nil
	}
	sourceID, err := runtimeids.ParseSessionID(sourceMeta.SessionID)
	if err != nil {
		child.mu.Unlock()
		child.mutationMu.Unlock()
		return fmt.Errorf("invalid creation source session id: %w", err)
	}
	switch kind {
	case SessionCreationSourcePreviousSession:
		child.meta.PreviousSessionID = &sourceID
		if sourceMeta.ParentAgentSessionID != nil {
			parentAgentSessionID := *sourceMeta.ParentAgentSessionID
			child.meta.ParentAgentSessionID = &parentAgentSessionID
		}
	case SessionCreationSourceParentAgent:
		child.meta.ParentAgentSessionID = &sourceID
	}
	if opts.InheritContinuation {
		child.meta.Continuation = cloneContinuationContext(sourceMeta.Continuation)
	} else {
		child.meta.Continuation = nil
	}
	child.meta.UpdatedAt = time.Now().UTC()
	child.initialContextFacts = SessionContextFacts{}
	child.mu.Unlock()
	child.contextFactsMu.Lock()
	child.contextFacts = SessionContextFacts{}
	child.contextFactsMu.Unlock()
	child.mutationMu.Unlock()
	return nil
}

// replayDerivedState folds the fork-derived child metadata over the replayed
// event stream one event at a time so callers never need the full history in
// memory. The derived flags reflect events up to the fork boundary, which can
// differ from the parent's latest state when forking at an earlier message.
type replayDerivedState struct {
	headlessActive bool
	reminderIssued bool
}

func (d *replayDerivedState) apply(record EventRecord) error {
	payload, err := record.Payload()
	if err != nil {
		return err
	}
	switch payload := payload.(type) {
	case MessageRecord:
		if payload.Role == MessageRoleDeveloper && payload.MessageType != nil {
			switch *payload.MessageType {
			case MessageTypeHeadlessMode:
				d.headlessActive = true
			case MessageTypeHeadlessModeExit:
				d.headlessActive = false
			}
		}
		if isCompactionSoonReminderMessage(payload) {
			d.reminderIssued = true
		}
	case HistoryReplacementRecord:
		d.reminderIssued = false
	}
	return nil
}

func isCompactionSoonReminderMessage(message MessageRecord) bool {
	return message.Role == MessageRoleDeveloper &&
		message.MessageType != nil &&
		*message.MessageType == MessageTypeCompactionSoonReminder &&
		message.Content != nil
}
