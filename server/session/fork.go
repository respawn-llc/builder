package session

import (
	"encoding/json"
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
var errDecodeForkHistoryReplacement = errors.New("decode history replacement for fork replay")

// forkReplayFlushEventCount and forkReplayFlushByteBudget bound how much of the
// parent conversation is buffered in memory before a chunk is flushed to the
// child store. Fork/clone stream the parent event log instead of materializing
// it so arbitrarily large histories never load fully into memory.
var (
	forkReplayFlushEventCount = 512
	forkReplayFlushByteBudget = 1 << 20
)

// ForkAtUserMessage creates a child session whose history is the parent's
// conversation up to (but excluding) the visible user message persisted at
// userMessageSeq. It returns the forked store and the 1-based ordinal of that
// user message among the parent's visible user messages (for naming/display).
func ForkAtUserMessage(parent *Store, userMessageSeq int64, forkName string, category sessioncontract.SessionCategory) (*Store, int, error) {
	if parent == nil {
		return nil, 0, fmt.Errorf("parent store is required")
	}
	if userMessageSeq <= 0 {
		return nil, 0, fmt.Errorf("user message seq must be >= 1")
	}
	return streamChildFromParent(parent, parent.Meta(), forkName, category, userMessageSeq)
}

// CloneSession creates a child session that replays the parent's entire
// conversation history. Workflow compact-and-continue fan-out branches use this
// so each parallel continuation compacts its own isolated copy of the source
// conversation instead of mutating the shared source session.
func CloneSession(parent *Store, forkName string, category sessioncontract.SessionCategory) (*Store, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent store is required")
	}
	child, _, err := streamChildFromParent(parent, parent.Meta(), forkName, category, 0)
	return child, err
}

// streamChildFromParent creates a child session and streams the parent event
// log into it in bounded chunks. A targetSeq > 0 stops replay just before the
// visible user message persisted at that sequence (fork-at-message); targetSeq
// == 0 clones everything. It returns the 1-based ordinal of the cut user
// message among the parent's visible user messages (0 when cloning).
func streamChildFromParent(parent *Store, parentMeta Meta, forkName string, category sessioncontract.SessionCategory, targetSeq int64) (*Store, int, error) {
	containerDir := filepath.Dir(parent.Dir())
	child, err := newLazyWithStoreOptions(containerDir, parentMeta.WorkspaceContainer, parentMeta.WorkspaceRoot, category, parent.options)
	if err != nil {
		return nil, 0, err
	}
	if err := InitializeCreationContext(child, parent, SessionCreationSourcePreviousSession, ChildContextOptions{
		InheritLockedContract: true,
		InheritContinuation:   true,
	}); err != nil {
		return nil, 0, err
	}

	child.mu.Lock()
	child.meta.Name = strings.TrimSpace(forkName)
	child.mu.Unlock()

	derived, cutOrdinal, err := streamReplayIntoChild(parent, child, targetSeq)
	if err != nil {
		return nil, 0, removeForkChild(child, fmt.Errorf("stream fork replay events: %w", err))
	}
	if targetSeq > 0 && cutOrdinal == 0 {
		return nil, 0, removeForkChild(child, fmt.Errorf("user message seq %d is out of range", targetSeq))
	}
	if err := child.applyForkDerivedState(derived); err != nil {
		return nil, 0, removeForkChild(child, fmt.Errorf("finalize fork replay: %w", err))
	}
	return child, cutOrdinal, nil
}

// streamReplayIntoChild walks the parent event log and appends each event to the
// child in bounded chunks, folding replay-derived metadata incrementally. When
// targetSeq > 0 it stops just before the visible user message persisted at that
// sequence and returns that message's 1-based visible-user-message ordinal; it
// returns 0 when the target is not found (or when cloning the whole log).
func streamReplayIntoChild(parent *Store, child *Store, targetSeq int64) (replayDerivedState, int, error) {
	derived := replayDerivedState{}
	visibleUserCount := 0
	cutOrdinal := 0
	buffer := make([]ReplayEvent, 0, forkReplayFlushEventCount)
	bufferedBytes := 0
	var committedReplayErr error
	var latestRollbackCandidate *rollbacktarget.CandidateLocator
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		if child.options.filelessEvents {
			if _, receipt, err := child.AppendReplayEvents(buffer); err != nil {
				if !receipt.Committed {
					return err
				}
				committedReplayErr = errors.Join(committedReplayErr, err)
			}
		} else {
			appended, err := child.appendReplayEventsWithEndByteCursor(buffer)
			if err != nil {
				if !appended.Committed {
					return err
				}
				committedReplayErr = errors.Join(committedReplayErr, err)
			}
			for index := range buffer {
				if !hasVisibleUserMessageEvent(buffer[index].Kind, buffer[index].Payload) {
					continue
				}
				latestRollbackCandidate = &rollbacktarget.CandidateLocator{
					UserMessageSeq:       appended.events[index].Seq,
					CandidatePageEndByte: *appended.endByteCursor,
				}
			}
		}
		buffer = buffer[:0]
		bufferedBytes = 0
		return nil
	}
	walkErr := parent.WalkEvents(func(evt Event) error {
		if hasVisibleUserMessageEvent(evt.Kind, evt.Payload) {
			visibleUserCount++
			if targetSeq > 0 && evt.Seq == targetSeq {
				cutOrdinal = visibleUserCount
				return errForkReplayBoundary
			}
		}
		replayEvent := ReplayEvent{StepID: evt.StepID, Kind: evt.Kind, Payload: append([]byte(nil), evt.Payload...)}
		if replayEvent.Kind == "history_replaced" {
			if err := flush(); err != nil {
				return err
			}
			rebasedPayload, err := rebaseHistoryReplacementRollbackCandidate(
				replayEvent.Payload,
				latestRollbackCandidate,
			)
			if err != nil {
				return err
			}
			replayEvent.Payload = rebasedPayload
		}
		derived.apply(replayEvent)
		buffer = append(buffer, replayEvent)
		bufferedBytes += len(replayEvent.Payload)
		if len(buffer) >= forkReplayFlushEventCount || bufferedBytes >= forkReplayFlushByteBudget {
			return flush()
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errForkReplayBoundary) {
		return derived, 0, errors.Join(committedReplayErr, walkErr)
	}
	if err := flush(); err != nil {
		return derived, 0, err
	}
	return derived, cutOrdinal, committedReplayErr
}

func rebaseHistoryReplacementRollbackCandidate(
	payload json.RawMessage,
	locator *rollbacktarget.CandidateLocator,
) (json.RawMessage, error) {
	var engine historyReplacementEngine
	if err := json.Unmarshal(payload, &engine); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecodeForkHistoryReplacement, err)
	}
	if IsLegacyReviewerRollbackHistoryReplacementEngine(engine.Engine) {
		return append(json.RawMessage(nil), payload...), nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecodeForkHistoryReplacement, err)
	}
	const locatorField = "latest_rollback_candidate"
	_, carriesLocator := fields[locatorField]
	if locator == nil {
		if !carriesLocator {
			return append(json.RawMessage(nil), payload...), nil
		}
		delete(fields, locatorField)
	} else {
		encoded, err := json.Marshal(locator)
		if err != nil {
			return nil, fmt.Errorf("encode rebased rollback candidate locator: %w", err)
		}
		fields[locatorField] = encoded
	}
	rebased, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode rebased history replacement: %w", err)
	}
	return rebased, nil
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

func removeForkChild(child *Store, primary error) error {
	if child == nil {
		return primary
	}
	return errors.Join(primary, child.RemoveDurable())
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
	defer child.mutationMu.Unlock()
	child.mu.Lock()
	defer child.mu.Unlock()
	if child.persisted {
		return fmt.Errorf("session creation context is immutable after durability")
	}
	child.meta.PreviousSessionID = nil
	child.meta.ParentAgentSessionID = nil
	if kind == SessionCreationSourceIndependent {
		child.meta.UpdatedAt = time.Now().UTC()
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
	sourceID, err := runtimeids.ParseSessionID(sourceMeta.SessionID)
	if err != nil {
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

func (d *replayDerivedState) apply(evt ReplayEvent) {
	switch evt.Kind {
	case "message":
		var msg reminderEventMessage
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			return
		}
		if strings.TrimSpace(msg.Role) == "developer" {
			switch strings.TrimSpace(msg.MessageType) {
			case "headless_mode":
				d.headlessActive = true
			case "headless_mode_exit":
				d.headlessActive = false
			}
		}
		if isCompactionSoonReminderMessage(msg) {
			d.reminderIssued = true
		}
	case "history_replaced":
		var replacement historyReplacementEngine
		if err := json.Unmarshal(evt.Payload, &replacement); err != nil {
			return
		}
		if IsLegacyReviewerRollbackHistoryReplacementEngine(replacement.Engine) {
			return
		}
		d.reminderIssued = false
	}
}

func isCompactionSoonReminderMessage(msg reminderEventMessage) bool {
	return strings.TrimSpace(msg.Role) == "developer" && strings.TrimSpace(msg.MessageType) == "compaction_soon_reminder" && strings.TrimSpace(msg.Content) != ""
}

type reminderEventMessage struct {
	Role        string `json:"role"`
	MessageType string `json:"message_type"`
	Content     string `json:"content"`
}
