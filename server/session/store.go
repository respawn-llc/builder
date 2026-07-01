package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"core/shared/sessioncontract"
	"github.com/google/uuid"
)

const (
	sessionFile = "session.json"
	eventsFile  = "events.jsonl"
)

var ErrSessionNotFound = sessioncontract.ErrSessionNotFound

var ErrGoalAgentOverwriteBlocked = errors.New("agent goal set cannot overwrite an active or paused goal")

type GoalAgentOverwriteBlockedError struct {
	Goal GoalState
}

func (e GoalAgentOverwriteBlockedError) Error() string {
	return ErrGoalAgentOverwriteBlocked.Error()
}

func (e GoalAgentOverwriteBlockedError) Unwrap() error {
	return ErrGoalAgentOverwriteBlocked
}

type Store struct {
	mu                    sync.Mutex
	sessionDir            string
	sessionFP             string
	eventsFP              string
	meta                  Meta
	conversationFreshness ConversationFreshness
	persisted             bool
	metadataVersion       uint64
	persistedMetaVersion  uint64
	options               storeOptions
	eventsFileSizeBytes   int64
	pendingFsyncWrites    int
}

type persistenceObservation struct {
	snapshot *PersistedStoreSnapshot
	version  uint64
}

func Create(workspaceContainerDir, workspaceContainerName, workspaceRoot string, options ...StoreOption) (*Store, error) {
	s, err := NewLazy(workspaceContainerDir, workspaceContainerName, workspaceRoot, options...)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePersistedLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func NewLazy(workspaceContainerDir, workspaceContainerName, workspaceRoot string, options ...StoreOption) (*Store, error) {
	storeOpts := normalizeStoreOptions(options...)
	return newLazyWithStoreOptions(workspaceContainerDir, workspaceContainerName, workspaceRoot, storeOpts)
}

func newLazyWithStoreOptions(workspaceContainerDir, workspaceContainerName, workspaceRoot string, storeOpts storeOptions) (*Store, error) {
	sid := uuid.NewString()
	sessionDir := filepath.Join(workspaceContainerDir, sid)
	now := storeTimestamp(storeOpts)
	return &Store{
		sessionDir: sessionDir,
		sessionFP:  filepath.Join(sessionDir, sessionFile),
		eventsFP:   filepath.Join(sessionDir, eventsFile),
		options:    storeOpts,
		meta: Meta{
			SessionID:          sid,
			WorkspaceRoot:      workspaceRoot,
			WorkspaceContainer: workspaceContainerName,
			CreatedAt:          now,
			UpdatedAt:          now,
		},
		conversationFreshness: ConversationFreshnessFresh,
		persisted:             false,
	}, nil
}

func Open(sessionDir string, options ...StoreOption) (*Store, error) {
	storeOpts := normalizeStoreOptions(options...)
	return openPersistedSession(sessionDir, nil, storeOpts)
}

func OpenByID(persistenceRoot, sessionID string, options ...StoreOption) (*Store, error) {
	storeOpts := normalizeStoreOptions(options...)
	record, err := resolvePersistedSessionRecord(persistenceRoot, sessionID, storeOpts)
	if err != nil {
		return nil, err
	}
	return openPersistedSession(record.SessionDir, record.Meta, storeOpts)
}

func openPersistedSession(sessionDir string, resolvedMeta *Meta, storeOpts storeOptions) (*Store, error) {
	s := &Store{
		sessionDir: sessionDir,
		sessionFP:  filepath.Join(sessionDir, sessionFile),
		eventsFP:   filepath.Join(sessionDir, eventsFile),
		persisted:  true,
		options:    storeOpts,
	}
	if resolvedMeta != nil {
		s.meta = *resolvedMeta
	} else if err := s.loadMetaLocked(); err != nil {
		return nil, err
	}
	s.metadataVersion = 1
	s.persistedMetaVersion = 1
	if err := s.bootstrapEventLogStateLocked(); err != nil {
		return nil, err
	}
	if err := s.observePersistence(&persistenceObservation{snapshot: s.persistenceSnapshotLocked(), version: s.metadataVersion}); err != nil {
		return nil, err
	}
	return s, nil
}

func resolvePersistedSessionRecord(persistenceRoot, sessionID string, storeOpts storeOptions) (PersistedSessionRecord, error) {
	root := strings.TrimSpace(persistenceRoot)
	id := strings.TrimSpace(sessionID)
	if root == "" {
		return PersistedSessionRecord{}, errors.New("persistence root is required")
	}
	if id == "" {
		return PersistedSessionRecord{}, errors.New("session id is required")
	}
	if storeOpts.resolver == nil {
		return PersistedSessionRecord{}, errPersistedSessionResolverRequired
	}
	record, err := storeOpts.resolver.ResolvePersistedSession(context.Background(), id)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	if strings.TrimSpace(record.SessionDir) == "" {
		return PersistedSessionRecord{}, fmt.Errorf("session %q: %w", id, errResolverRecordMissingSessionDir)
	}
	if !filepath.IsAbs(record.SessionDir) || filepath.Clean(record.SessionDir) != record.SessionDir {
		return PersistedSessionRecord{}, fmt.Errorf("session %q: %w", id, errResolverRecordRelativeSessionDir)
	}
	if record.Meta == nil {
		return PersistedSessionRecord{}, fmt.Errorf("session %q: %w", id, errResolverRecordMissingMetadata)
	}
	return record, nil
}

func hasSessionMeta(sessionDir string) bool {
	if strings.TrimSpace(sessionDir) == "" {
		return false
	}
	fp, err := openRegularSessionFile(filepath.Join(sessionDir, sessionFile), "session meta")
	if err != nil {
		return false
	}
	return fp.Close() == nil
}

func ListSessions(workspaceContainerDir string) ([]Summary, error) {
	entries, err := os.ReadDir(workspaceContainerDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace container: %w", err)
	}

	out := make([]Summary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		sessionPath := filepath.Join(workspaceContainerDir, sessionID)
		data, err := readRegularSessionFile(filepath.Join(sessionPath, sessionFile), "session meta")
		if err != nil {
			continue
		}
		var m Meta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		out = append(out, Summary{
			SessionID:          m.SessionID,
			Name:               strings.TrimSpace(m.Name),
			FirstPromptPreview: strings.TrimSpace(m.FirstPromptPreview),
			UpdatedAt:          m.UpdatedAt,
			Path:               sessionPath,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *Store) Dir() string {
	return s.sessionDir
}

// RemoveDurable deletes this session's on-disk artifacts after a failed
// creation flow and returns the store to a non-durable state.
func (s *Store) RemoveDurable() error {
	if s == nil {
		return errors.New("session store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.sessionDir) == "" {
		return errors.New("session dir is required")
	}
	if filepath.Base(s.sessionDir) != strings.TrimSpace(s.meta.SessionID) {
		return fmt.Errorf("session dir %q does not match session id %q", s.sessionDir, s.meta.SessionID)
	}
	if err := os.RemoveAll(s.sessionDir); err != nil {
		return fmt.Errorf("remove session dir: %w", err)
	}
	s.persisted = false
	s.eventsFileSizeBytes = 0
	s.pendingFsyncWrites = 0
	s.persistedMetaVersion = 0
	return nil
}

func (s *Store) Meta() Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

func (s *Store) ConversationFreshness() ConversationFreshness {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversationFreshness
}

func (s *Store) mutateAndPersist(mutator func() error) error {
	s.mu.Lock()
	if err := mutator(); err != nil {
		s.mu.Unlock()
		return err
	}
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) unlockAndObservePersistence(observation *persistenceObservation, err error) error {
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.observePersistence(observation)
}

func (s *Store) mutateLockedContractWithCommitStatus(mutator func(*LockedContract)) (LockedContractMutationResult, error) {
	if mutator == nil {
		return LockedContractMutationResult{}, nil
	}
	s.mu.Lock()
	if s.meta.Locked == nil {
		s.mu.Unlock()
		return LockedContractMutationResult{}, nil
	}
	previousMeta := s.meta
	previousMetadataVersion := s.metadataVersion
	previousPersistedMetaVersion := s.persistedMetaVersion
	next := *s.meta.Locked
	mutator(&next)
	s.meta.Locked = &next
	s.meta.UpdatedAt = time.Now().UTC()
	observation, persistErr := s.persistMetaLocked()
	if persistErr != nil {
		s.meta = previousMeta
		s.metadataVersion = previousMetadataVersion
		s.persistedMetaVersion = previousPersistedMetaVersion
		s.mu.Unlock()
		return LockedContractMutationResult{Committed: false, Locked: cloneLockedContract(previousMeta.Locked)}, persistErr
	}
	committed := cloneLockedContract(s.meta.Locked)
	fileless := s.options.filelessMeta
	s.mu.Unlock()
	observeErr := s.observePersistence(observation)
	if observeErr != nil && fileless {
		s.mu.Lock()
		s.meta = previousMeta
		s.metadataVersion = previousMetadataVersion
		s.persistedMetaVersion = previousPersistedMetaVersion
		s.mu.Unlock()
		return LockedContractMutationResult{Committed: false, Locked: cloneLockedContract(previousMeta.Locked)}, observeErr
	}
	return LockedContractMutationResult{Committed: true, Locked: committed}, observeErr
}

func (s *Store) EnsureDurable() error {
	return s.mutateAndPersist(func() error { return nil })
}

func (s *Store) MarkInFlight(inFlight bool) error {
	return s.mutateAndPersist(func() error {
		s.meta.InFlightStep = inFlight
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) SetName(name string) error {
	return s.mutateAndPersist(func() error {
		s.meta.Name = strings.TrimSpace(name)
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) SetListingMetadata(name string, firstPromptPreview string) error {
	return s.mutateAndPersist(func() error {
		s.meta.Name = strings.TrimSpace(name)
		s.meta.FirstPromptPreview = normalizeFirstPromptPreview(firstPromptPreview)
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) SetParentSessionID(parentSessionID string) error {
	return s.mutateAndPersist(func() error {
		s.meta.ParentSessionID = strings.TrimSpace(parentSessionID)
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) SetWorkspaceRoot(workspaceRoot string) error {
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return errors.New("workspace root is required")
	}
	return s.mutateAndPersist(func() error {
		s.meta.WorkspaceRoot = trimmedWorkspaceRoot
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) SetInputDraft(inputDraft string) error {
	s.mu.Lock()

	if s.meta.InputDraft == inputDraft && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	s.meta.InputDraft = inputDraft
	s.meta.UpdatedAt = time.Now().UTC()
	if !s.persisted && inputDraft == "" {
		s.mu.Unlock()
		return nil
	}
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetHeadlessActive(active bool) error {
	s.mu.Lock()
	if s.meta.HeadlessActive == active && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	s.meta.HeadlessActive = active
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetCompactionSoonReminderIssued(issued bool) error {
	s.mu.Lock()
	if s.meta.CompactionSoonReminderIssued == issued && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	s.meta.CompactionSoonReminderIssued = issued
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetWorktreeReminderState(state *WorktreeReminderState) error {
	nextState := cloneWorktreeReminderState(state)
	s.mu.Lock()
	statesEqual := s.meta.WorktreeReminder == nil && nextState == nil
	if s.meta.WorktreeReminder != nil && nextState != nil {
		statesEqual = *s.meta.WorktreeReminder == *nextState
	}
	if statesEqual && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	s.meta.WorktreeReminder = nextState
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetGoal(objective string, actor GoalActor) (GoalState, error) {
	return s.SetGoalWithEvents(objective, actor, nil)
}

func (s *Store) SetGoalWithEvents(objective string, actor GoalActor, extraEvents []EventInput) (GoalState, error) {
	return s.SetActiveGoalWithEvents(GoalState{Objective: objective}, actor, extraEvents)
}

func (s *Store) SetActiveGoalWithEvents(goal GoalState, actor GoalActor, extraEvents []EventInput) (GoalState, error) {
	goal.Objective = strings.TrimSpace(goal.Objective)
	if goal.Objective == "" {
		return GoalState{}, errors.New("goal objective is required")
	}
	goal.ID = strings.TrimSpace(goal.ID)
	if goal.ID == "" {
		goal.ID = uuid.NewString()
	}
	goal.Status = GoalStatusActive
	normalizedActor, err := normalizeGoalActor(actor)
	if err != nil {
		return GoalState{}, err
	}
	s.mu.Lock()
	now := storeTimestamp(s.options)
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = now
	}
	if goal.UpdatedAt.IsZero() {
		goal.UpdatedAt = goal.CreatedAt
	}
	goal.CreatedAt = goal.CreatedAt.UTC().Round(0)
	goal.UpdatedAt = goal.UpdatedAt.UTC().Round(0)
	replacedGoalID := ""
	previousGoal := cloneGoalState(s.meta.Goal)
	if normalizedActor == GoalActorAgent && previousGoal != nil && previousGoal.Status != GoalStatusComplete {
		s.mu.Unlock()
		return GoalState{}, GoalAgentOverwriteBlockedError{Goal: *previousGoal}
	}
	if s.meta.Goal != nil {
		replacedGoalID = strings.TrimSpace(s.meta.Goal.ID)
	}
	events, err := s.buildGoalEventsLocked("goal_set", GoalSetEvent{Goal: goal, Actor: normalizedActor, ReplacedGoalID: replacedGoalID}, extraEvents, now)
	if err != nil {
		s.mu.Unlock()
		return GoalState{}, err
	}
	s.meta.Goal = cloneGoalState(&goal)
	if err := s.appendGoalEventsLocked(events, func() {
		s.meta.Goal = previousGoal
	}); err != nil {
		return GoalState{}, err
	}
	return goal, nil
}

func (s *Store) SetGoalStatus(status GoalStatus, actor GoalActor) (GoalState, error) {
	return s.SetGoalStatusWithEventBuilder(status, actor, func(GoalState) ([]EventInput, error) {
		return nil, nil
	})
}

func (s *Store) SetGoalStatusWithEventBuilder(status GoalStatus, actor GoalActor, buildExtraEvents func(GoalState) ([]EventInput, error)) (GoalState, error) {
	goal, _, err := s.transitionGoalStatus(status, actor, nil, buildExtraEvents)
	return goal, err
}

func (s *Store) CompleteGoalIfActive(expectedID string, actor GoalActor, buildExtraEvents func(GoalState) ([]EventInput, error)) (GoalState, bool, error) {
	return s.transitionGoalStatus(GoalStatusComplete, actor, func(current GoalState) bool {
		return current.ID == expectedID && current.Status == GoalStatusActive
	}, buildExtraEvents)
}

func (s *Store) transitionGoalStatus(status GoalStatus, actor GoalActor, allow func(GoalState) bool, buildExtraEvents func(GoalState) ([]EventInput, error)) (GoalState, bool, error) {
	normalizedStatus, err := normalizeGoalStatus(status)
	if err != nil {
		return GoalState{}, false, err
	}
	normalizedActor, err := normalizeGoalActor(actor)
	if err != nil {
		return GoalState{}, false, err
	}
	s.mu.Lock()
	if s.meta.Goal == nil {
		s.mu.Unlock()
		if allow != nil {
			return GoalState{}, false, nil
		}
		return GoalState{}, false, errors.New("goal is not set")
	}
	if allow != nil && !allow(*cloneGoalState(s.meta.Goal)) {
		s.mu.Unlock()
		return GoalState{}, false, nil
	}
	now := storeTimestamp(s.options)
	previousGoalState := *cloneGoalState(s.meta.Goal)
	goal := *cloneGoalState(s.meta.Goal)
	previousStatus := goal.Status
	goal.Status = normalizedStatus
	goal.UpdatedAt = now
	var extraEvents []EventInput
	if buildExtraEvents != nil {
		extraEvents, err = buildExtraEvents(goal)
		if err != nil {
			s.mu.Unlock()
			return GoalState{}, false, err
		}
	}
	events, err := s.buildGoalEventsLocked("goal_status_updated", GoalStatusUpdatedEvent{Goal: goal, Actor: normalizedActor, PreviousStatus: previousStatus}, extraEvents, now)
	if err != nil {
		s.mu.Unlock()
		return GoalState{}, false, err
	}
	s.meta.Goal = cloneGoalState(&goal)
	if err := s.appendGoalEventsLocked(events, func() {
		s.meta.Goal = cloneGoalState(&previousGoalState)
	}); err != nil {
		return GoalState{}, false, err
	}
	return goal, true, nil
}

func (s *Store) ClearGoal(actor GoalActor) (GoalState, error) {
	return s.ClearGoalWithEvents(actor, nil)
}

func (s *Store) ClearGoalWithEvents(actor GoalActor, extraEvents []EventInput) (GoalState, error) {
	normalizedActor, err := normalizeGoalActor(actor)
	if err != nil {
		return GoalState{}, err
	}
	s.mu.Lock()
	if s.meta.Goal == nil {
		s.mu.Unlock()
		return GoalState{}, errors.New("goal is not set")
	}
	now := storeTimestamp(s.options)
	goal := *cloneGoalState(s.meta.Goal)
	events, err := s.buildGoalEventsLocked("goal_cleared", GoalClearedEvent{Goal: goal, Actor: normalizedActor}, extraEvents, now)
	if err != nil {
		s.mu.Unlock()
		return GoalState{}, err
	}
	s.meta.Goal = nil
	if err := s.appendGoalEventsLocked(events, func() {
		s.meta.Goal = cloneGoalState(&goal)
	}); err != nil {
		return GoalState{}, err
	}
	return goal, nil
}

func (s *Store) appendGoalEventsLocked(events []Event, rollback func()) error {
	observation, _, err := s.appendEventsAtomicLockedWithCommitStatus(events)
	if err != nil && rollback != nil {
		rollback()
	}
	return s.unlockAndObservePersistence(observation, err)
}

func storeTimestamp(options storeOptions) time.Time {
	now := time.Now().UTC()
	if options.now != nil {
		now = options.now()
	}
	return now.UTC().Round(0)
}

func (s *Store) buildGoalEventsLocked(kind string, payload any, extraEvents []EventInput, now time.Time) ([]Event, error) {
	events := make([]Event, 0, 1+len(extraEvents))
	seq := s.meta.LastSequence
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}
	seq++
	events = append(events, Event{Seq: seq, Timestamp: now, Kind: kind, Payload: body})
	for _, in := range extraEvents {
		body, err := json.Marshal(in.Payload)
		if err != nil {
			return nil, fmt.Errorf("marshal event payload: %w", err)
		}
		seq++
		events = append(events, Event{Seq: seq, Timestamp: now, Kind: in.Kind, Payload: body})
	}
	return events, nil
}

func (s *Store) SetUsageState(state *UsageState) error {
	s.mu.Lock()

	normalized := normalizeUsageState(state)
	if usageStatesEqual(s.meta.UsageState, normalized) && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	s.meta.UsageState = normalized
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetContinuationContext(ctx ContinuationContext) error {
	s.mu.Lock()

	s.meta.Continuation = normalizeContinuationContext(ctx)
	s.meta.UpdatedAt = time.Now().UTC()
	if !s.persisted {
		s.mu.Unlock()
		return nil
	}
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) MarkGeneratedRecoveredWarningIssued() error {
	return s.mutateAndPersist(func() error {
		s.meta.GeneratedRecoveredWarningIssued = true
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) SetWorkflowSessionState(state *WorkflowSessionState) error {
	return s.mutateAndPersist(func() error {
		if state == nil {
			s.meta.WorkflowSession = nil
		} else {
			normalized := *state
			normalized.RunID = strings.TrimSpace(normalized.RunID)
			normalized.TaskID = strings.TrimSpace(normalized.TaskID)
			normalized.WorkflowID = strings.TrimSpace(normalized.WorkflowID)
			if normalized.RunID == "" && normalized.TaskID == "" && normalized.WorkflowID == "" {
				s.meta.WorkflowSession = nil
			} else {
				s.meta.WorkflowSession = &normalized
			}
		}
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) MarkModelDispatchLocked(contract LockedContract) error {
	return s.mutateAndPersist(func() error {
		s.meta.ModelRequestCount++
		if s.meta.Locked == nil {
			contract.EnabledTools = append([]string(nil), contract.EnabledTools...)
			contract.HasEnabledTools = true
			contract.LockedAt = time.Now().UTC()
			s.meta.Locked = &contract
		}
		s.meta.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (s *Store) BackfillLockedContextBudget(contextWindow, contextPercent int) error {
	if contextWindow <= 0 || contextPercent <= 0 {
		return nil
	}
	s.mu.Lock()
	if s.meta.Locked == nil {
		s.mu.Unlock()
		return nil
	}
	changed := false
	if s.meta.Locked.ContextWindow <= 0 {
		s.meta.Locked.ContextWindow = contextWindow
		changed = true
	}
	if s.meta.Locked.ContextPercent <= 0 {
		s.meta.Locked.ContextPercent = contextPercent
		changed = true
	}
	if !changed {
		s.mu.Unlock()
		return nil
	}
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) BackfillLockedProviderContract(contract LockedProviderCapabilities) error {
	if strings.TrimSpace(contract.ProviderID) == "" {
		return nil
	}
	s.mu.Lock()
	if s.meta.Locked == nil || strings.TrimSpace(s.meta.Locked.ProviderContract.ProviderID) != "" {
		s.mu.Unlock()
		return nil
	}
	s.meta.Locked.ProviderContract = contract
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) BackfillLockedSystemPrompt(systemPrompt string) error {
	trimmed := strings.TrimSpace(systemPrompt)
	s.mu.Lock()
	if s.meta.Locked == nil || s.meta.Locked.HasSystemPrompt {
		s.mu.Unlock()
		return nil
	}
	s.meta.Locked.SystemPrompt = trimmed
	s.meta.Locked.HasSystemPrompt = true
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) BackfillLockedReviewerPrompt(reviewerPrompt string) error {
	trimmed := strings.TrimSpace(reviewerPrompt)
	s.mu.Lock()
	if s.meta.Locked == nil || s.meta.Locked.HasReviewerPrompt {
		s.mu.Unlock()
		return nil
	}
	s.meta.Locked.ReviewerPrompt = trimmed
	s.meta.Locked.HasReviewerPrompt = true
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) MarkLockedPromptFacingSnapshotsStale() (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		locked.SystemPrompt = ""
		locked.HasSystemPrompt = false
		locked.ReviewerPrompt = ""
		locked.HasReviewerPrompt = false
	})
}

func (s *Store) RefreshLockedMainPromptSnapshot(snapshot LockedMainPromptSnapshot) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		locked.SystemPrompt = strings.TrimSpace(snapshot.SystemPrompt)
		locked.HasSystemPrompt = snapshot.HasSystemPrompt
		locked.ToolPreambles = cloneBoolPtr(snapshot.ToolPreambles)
		if snapshot.ContextWindow > 0 {
			locked.ContextWindow = snapshot.ContextWindow
		}
		if snapshot.ContextPercent > 0 {
			locked.ContextPercent = snapshot.ContextPercent
		}
	})
}

func (s *Store) RefreshLockedReviewerPromptSnapshot(snapshot LockedReviewerPromptSnapshot) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		locked.ReviewerPrompt = strings.TrimSpace(snapshot.ReviewerPrompt)
		locked.HasReviewerPrompt = snapshot.HasReviewerPrompt
	})
}

func (s *Store) BackfillLockedRequestShape(fields LockedRequestShapeBackfill) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		locked.EnabledTools = append([]string(nil), fields.EnabledTools...)
		locked.HasEnabledTools = fields.HasEnabledTools
		locked.WebSearchMode = strings.TrimSpace(fields.WebSearchMode)
	})
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (s *Store) AppendEvent(stepID, kind string, payload any) (Event, bool, error) {
	s.mu.Lock()

	evt, err := s.buildEventLocked(stepID, kind, payload, time.Now().UTC())
	if err != nil {
		s.mu.Unlock()
		return Event{}, false, err
	}
	committed, err := s.appendObservedEventsLockedWithCommitStatus([]Event{evt})
	if err != nil {
		return evt, committed, err
	}
	return evt, committed, nil
}

func (s *Store) buildEventLocked(stepID, kind string, payload any, now time.Time) (Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload: %w", err)
	}
	return Event{
		Seq:       s.meta.LastSequence + 1,
		Timestamp: now,
		Kind:      kind,
		StepID:    stepID,
		Payload:   body,
	}, nil
}

func (s *Store) AppendTurnAtomic(stepID string, events []EventInput) ([]Event, error) {
	s.mu.Lock()

	if len(events) == 0 {
		s.mu.Unlock()
		return nil, nil
	}
	built := make([]Event, 0, len(events))
	seq := s.meta.LastSequence
	now := time.Now().UTC()
	for _, in := range events {
		body, err := json.Marshal(in.Payload)
		if err != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("marshal event payload: %w", err)
		}
		seq++
		built = append(built, Event{
			Seq:       seq,
			Timestamp: now,
			Kind:      in.Kind,
			StepID:    stepID,
			Payload:   body,
		})
	}
	if _, err := s.appendObservedEventsLockedWithCommitStatus(built); err != nil {
		return nil, err
	}
	return built, nil
}

type ReplayEvent struct {
	StepID  string
	Kind    string
	Payload json.RawMessage
}

func (s *Store) AppendReplayEvents(events []ReplayEvent) ([]Event, error) {
	s.mu.Lock()

	if len(events) == 0 {
		s.mu.Unlock()
		return nil, nil
	}
	built := make([]Event, 0, len(events))
	seq := s.meta.LastSequence
	now := time.Now().UTC()
	for _, in := range events {
		seq++
		payload := append(json.RawMessage(nil), in.Payload...)
		built = append(built, Event{
			Seq:       seq,
			Timestamp: now,
			Kind:      in.Kind,
			StepID:    strings.TrimSpace(in.StepID),
			Payload:   payload,
		})
	}
	if _, err := s.appendObservedEventsLockedWithCommitStatus(built); err != nil {
		return nil, err
	}
	return built, nil
}

func (s *Store) appendObservedEventsLockedWithCommitStatus(events []Event) (bool, error) {
	previousMeta := s.meta
	previousFreshness := s.conversationFreshness
	s.captureFirstPromptPreviewLocked(events)
	s.advanceConversationFreshnessLocked(events)
	s.updateLatestRunLocked(events)
	observation, committed, err := s.appendEventsAtomicLockedWithCommitStatus(events)
	if err != nil && !committed {
		s.meta = previousMeta
		s.conversationFreshness = previousFreshness
	}
	s.mu.Unlock()
	if err != nil {
		return committed, err
	}
	return committed, s.observePersistence(observation)
}

type EventInput struct {
	Kind    string
	Payload any
}

func (s *Store) ReadEventsBackwardUntil(match func(Event) bool) ([]Event, error) {
	window, err := s.ReadSegmentBackward(0, match)
	if err != nil {
		return nil, err
	}
	return window.Events, nil
}

func (s *Store) ReadSegmentBackward(endOffset int64, match func(Event) bool) (SegmentWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persisted {
		return SegmentWindow{ReachedStart: true, ReachedEnd: true}, nil
	}
	return readSegmentBackwardFile(s.eventsFP, endOffset, activeTailReverseChunkBytes, match)
}

func (s *Store) ReadSegmentForward(startOffset int64, match func(Event) bool) (SegmentWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persisted {
		return SegmentWindow{ReachedStart: true, ReachedEnd: true}, nil
	}
	return readSegmentForwardFile(s.eventsFP, startOffset, activeTailReverseChunkBytes, match)
}

func (s *Store) ReadRecentEvents(maxEvents int) (SegmentWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persisted {
		return SegmentWindow{ReachedStart: true}, nil
	}
	return readRecentEventsBackwardFile(s.eventsFP, 0, maxEvents, activeTailReverseChunkBytes)
}

func (s *Store) WalkEvents(visit func(Event) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persisted {
		return nil
	}
	parsed, err := walkEventsFile(s.eventsFP, visit)
	if err != nil {
		return err
	}
	s.eventsFileSizeBytes = parsed.totalBytes
	return nil
}

func readMetaFile(path string) (Meta, error) {
	data, err := readRegularSessionFile(path, "session meta")
	if err != nil {
		return Meta{}, fmt.Errorf("%w: %w", ErrReadSessionMeta, err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, fmt.Errorf("parse session meta: %w", err)
	}
	return meta, nil
}

func (s *Store) loadMetaLocked() error {
	m, err := readMetaFile(s.sessionFP)
	if err == nil {
		s.meta = m
		return nil
	}
	if s.options.resolver == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	record, resolveErr := s.options.resolver.ResolvePersistedSession(context.Background(), filepath.Base(s.sessionDir))
	if resolveErr != nil {
		return fmt.Errorf("%w (resolver fallback failed: %v)", err, resolveErr)
	}
	if record.Meta == nil {
		return fmt.Errorf("%w (resolver fallback returned nil metadata)", err)
	}
	s.meta = *record.Meta
	return nil
}

func (s *Store) persistMetaLocked() (*persistenceObservation, error) {
	if err := s.ensurePersistedLocked(); err != nil {
		return nil, err
	}
	s.metadataVersion++
	if !s.options.filelessMeta {
		data, err := json.MarshalIndent(s.meta, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal session meta: %w", err)
		}
		tmp := s.sessionFP + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return nil, fmt.Errorf("write session meta tmp: %w", err)
		}
		if err := os.Rename(tmp, s.sessionFP); err != nil {
			return nil, fmt.Errorf("replace session meta: %w", err)
		}
		s.persistedMetaVersion = s.metadataVersion
	}
	return &persistenceObservation{snapshot: s.persistenceSnapshotLocked(), version: s.metadataVersion}, nil
}

func (s *Store) hasDurableMetadataLocked() bool {
	if s == nil || !s.persisted {
		return false
	}
	if hasSessionMeta(s.sessionDir) {
		return true
	}
	if !s.options.filelessMeta {
		return false
	}
	return s.metadataVersion != 0 && s.persistedMetaVersion == s.metadataVersion
}

func (s *Store) appendEventsAtomicLockedWithCommitStatus(events []Event) (*persistenceObservation, bool, error) {
	if err := s.ensurePersistedLocked(); err != nil {
		return nil, false, err
	}

	if _, err := s.appendEventsLogLocked(events); err != nil {
		return nil, false, err
	}
	for _, e := range events {
		s.meta.LastSequence = e.Seq
	}
	s.meta.UpdatedAt = time.Now().UTC()
	snapshot, err := s.persistMetaLocked()
	if err != nil {
		return nil, true, err
	}
	return snapshot, true, nil
}

func (s *Store) ensurePersistedLocked() error {
	if s.persisted {
		return nil
	}
	if err := os.MkdirAll(s.sessionDir, 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	if err := os.WriteFile(s.eventsFP, nil, 0o644); err != nil {
		return fmt.Errorf("initialize events file: %w", err)
	}
	s.eventsFileSizeBytes = 0
	s.pendingFsyncWrites = 0
	s.persisted = true
	return nil
}

func (s *Store) persistenceSnapshotLocked() *PersistedStoreSnapshot {
	if s == nil || !s.persisted || s.options.observer == nil {
		return nil
	}
	snapshot := PersistedStoreSnapshot{
		SessionDir: s.sessionDir,
		Meta:       s.meta,
	}
	return &snapshot
}

func (s *Store) observePersistence(observation *persistenceObservation) error {
	if s == nil || observation == nil || observation.snapshot == nil || s.options.observer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.options.observerTimeout)
	defer cancel()
	if err := s.options.observer.ObservePersistedStore(ctx, *observation.snapshot); err != nil {
		return err
	}
	if s.options.filelessMeta {
		s.mu.Lock()
		if observation.version > s.persistedMetaVersion {
			s.persistedMetaVersion = observation.version
		}
		s.mu.Unlock()
	}
	return nil
}

func normalizeContinuationContext(ctx ContinuationContext) *ContinuationContext {
	openAIBaseURL := strings.TrimSpace(ctx.OpenAIBaseURL)
	agentRole := strings.TrimSpace(ctx.AgentRole)
	if openAIBaseURL == "" && agentRole == "" {
		return nil
	}
	return &ContinuationContext{OpenAIBaseURL: openAIBaseURL, AgentRole: agentRole}
}

func normalizeUsageState(state *UsageState) *UsageState {
	if state == nil {
		return nil
	}
	normalized := *state
	if normalized.InputTokens < 0 {
		normalized.InputTokens = 0
	}
	if normalized.OutputTokens < 0 {
		normalized.OutputTokens = 0
	}
	if normalized.WindowTokens < 0 {
		normalized.WindowTokens = 0
	}
	if normalized.CachedInputTokens < 0 {
		normalized.CachedInputTokens = 0
	}
	if normalized.CachedInputTokens > normalized.InputTokens {
		normalized.CachedInputTokens = normalized.InputTokens
	}
	if normalized.EstimatedProviderTokens < 0 {
		normalized.EstimatedProviderTokens = 0
	}
	if normalized.TotalInputTokens < 0 {
		normalized.TotalInputTokens = 0
	}
	if normalized.TotalCachedInputTokens < 0 {
		normalized.TotalCachedInputTokens = 0
	}
	if normalized.TotalCachedInputTokens > normalized.TotalInputTokens {
		normalized.TotalCachedInputTokens = normalized.TotalInputTokens
	}
	if normalized.InputTokens == 0 && normalized.OutputTokens == 0 && normalized.WindowTokens == 0 && normalized.CachedInputTokens == 0 && !normalized.HasCachedInputTokens && normalized.EstimatedProviderTokens == 0 && normalized.TotalInputTokens == 0 && normalized.TotalCachedInputTokens == 0 {
		return nil
	}
	return &normalized
}

func usageStatesEqual(left, right *UsageState) bool {
	left = normalizeUsageState(left)
	right = normalizeUsageState(right)
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Store) captureFirstPromptPreviewLocked(events []Event) {
	if strings.TrimSpace(s.meta.FirstPromptPreview) != "" {
		return
	}
	for _, evt := range events {
		if preview, ok := firstPromptPreviewFromEvent(evt.Kind, evt.Payload); ok {
			s.meta.FirstPromptPreview = preview
			return
		}
	}
}

func (s *Store) advanceConversationFreshnessLocked(events []Event) {
	if s.conversationFreshness == ConversationFreshnessEstablished {
		return
	}
	for _, evt := range events {
		s.conversationFreshness = advanceConversationFreshness(s.conversationFreshness, evt)
		if s.conversationFreshness == ConversationFreshnessEstablished {
			s.meta.ConversationEstablished = true
			return
		}
	}
}
