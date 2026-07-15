package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/valuecopy"
	"github.com/google/uuid"
)

const (
	eventsFile = "events.jsonl"

	eventModelRecoveryPending   = "model_recovery_pending"
	eventModelRecoveryConsumed  = "model_recovery_consumed"
	eventModelRecoveryDiscarded = "model_recovery_discarded"
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
	mutationMu            sync.Mutex
	sessionDir            string
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

func Create(workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, options ...StoreOption) (*Store, error) {
	s, err := NewLazy(workspaceContainerDir, workspaceContainerName, workspaceRoot, category, options...)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.options.filelessEvents {
		s.mu.Unlock()
		return nil, errEphemeralStoreCannotBeDurable
	}
	observation, err := s.persistMetaLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := s.observePersistence(observation); err != nil {
		return nil, errors.Join(err, s.RemoveDurable())
	}
	return s, nil
}

func NewLazy(workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, options ...StoreOption) (*Store, error) {
	storeOpts := normalizeStoreOptions(options...)
	return newLazyWithStoreOptions(workspaceContainerDir, workspaceContainerName, workspaceRoot, category, storeOpts)
}

func newLazyWithStoreOptions(workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, storeOpts storeOptions) (*Store, error) {
	validatedCategory, err := sessioncontract.ParseSessionCategory(string(category))
	if err != nil {
		return nil, err
	}
	sid := runtimeids.NewSessionID().String()
	sessionDir := filepath.Join(workspaceContainerDir, sid)
	now := storeTimestamp(storeOpts)
	return &Store{
		sessionDir: sessionDir,
		eventsFP:   filepath.Join(sessionDir, eventsFile),
		options:    storeOpts,
		meta: Meta{
			SessionID:          sid,
			Category:           sessionCategoryPointer(validatedCategory),
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
	resolvedMeta, err := resolvePersistedSessionMetaForDir(sessionDir, storeOpts)
	if err != nil {
		return nil, err
	}
	return openPersistedSession(sessionDir, resolvedMeta, storeOpts)
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
		eventsFP:   filepath.Join(sessionDir, eventsFile),
		persisted:  true,
		options:    storeOpts,
	}
	if resolvedMeta == nil {
		return nil, errPersistedSessionResolverRequired
	}
	s.meta = cloneMeta(*resolvedMeta)
	if err := normalizeMetaContinuation(&s.meta); err != nil {
		return nil, fmt.Errorf("validate session continuation: %w", err)
	}
	if err := normalizeMetaWorktreeReminder(&s.meta); err != nil {
		return nil, fmt.Errorf("validate session worktree context: %w", err)
	}
	if err := validateMetaCategory(&s.meta); err != nil {
		return nil, err
	}
	s.metadataVersion = 1
	s.persistedMetaVersion = 1
	observation, err := s.bootstrapEventLogStateLocked()
	if err != nil {
		return nil, err
	}
	if err := s.observeEventLogReconciliation(observation); err != nil {
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
	if err := validatePersistedSessionRecord(id, record); err != nil {
		return PersistedSessionRecord{}, err
	}
	return record, nil
}

func resolvePersistedSessionMetaForDir(sessionDir string, storeOpts storeOptions) (*Meta, error) {
	if storeOpts.resolver == nil {
		return nil, errPersistedSessionResolverRequired
	}
	cleanDir := filepath.Clean(sessionDir)
	sessionID := filepath.Base(cleanDir)
	record, err := storeOpts.resolver.ResolvePersistedSession(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}
	if err := validatePersistedSessionRecord(sessionID, record); err != nil {
		return nil, err
	}
	scopedIdentity, err := config.CanonicalPathIdentity(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("resolve scoped session dir identity %q: %w", cleanDir, err)
	}
	authoritativeIdentity, err := config.CanonicalPathIdentity(record.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("resolve authoritative session dir identity %q: %w", record.SessionDir, err)
	}
	if scopedIdentity != authoritativeIdentity {
		return nil, fmt.Errorf(
			"session %q scoped dir %q does not match authoritative dir %q: %w",
			sessionID,
			cleanDir,
			record.SessionDir,
			errResolverRecordSessionDirMismatch,
		)
	}
	return record.Meta, nil
}

func validatePersistedSessionRecord(sessionID string, record PersistedSessionRecord) error {
	id := strings.TrimSpace(sessionID)
	if strings.TrimSpace(record.SessionDir) == "" {
		return fmt.Errorf("session %q: %w", id, errResolverRecordMissingSessionDir)
	}
	if !filepath.IsAbs(record.SessionDir) || filepath.Clean(record.SessionDir) != record.SessionDir {
		return fmt.Errorf("session %q: %w", id, errResolverRecordRelativeSessionDir)
	}
	if record.Meta == nil {
		return fmt.Errorf("session %q: %w", id, errResolverRecordMissingMetadata)
	}
	if strings.TrimSpace(record.Meta.SessionID) == "" {
		return fmt.Errorf("session %q: %w", id, errResolverRecordMissingSessionID)
	}
	if record.Meta.SessionID != id {
		return fmt.Errorf(
			"requested session %q resolved metadata for session %q: %w",
			id,
			record.Meta.SessionID,
			errResolverRecordSessionIDMismatch,
		)
	}
	return nil
}

func (s *Store) Dir() string {
	if s == nil {
		panic("session store dir: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionDir
}

type ArtifactRelocationTarget struct {
	SessionDir         string
	WorkspaceRoot      string
	WorkspaceContainer string
	UpdatedAt          time.Time
}

func (s *Store) RunArtifactRelocation(target ArtifactRelocationTarget, relocate func() error) error {
	if s == nil {
		return errors.New("session store is required")
	}
	if relocate == nil {
		return errors.New("session artifact relocation callback is required")
	}
	target.SessionDir = filepath.Clean(strings.TrimSpace(target.SessionDir))
	target.WorkspaceRoot = strings.TrimSpace(target.WorkspaceRoot)
	target.WorkspaceContainer = strings.TrimSpace(target.WorkspaceContainer)
	if target.SessionDir == "." || !filepath.IsAbs(target.SessionDir) {
		return errors.New("session relocation target dir must be absolute")
	}
	if target.WorkspaceRoot == "" {
		return errors.New("session relocation workspace root is required")
	}
	if target.WorkspaceContainer == "" {
		return errors.New("session relocation workspace container is required")
	}
	if target.UpdatedAt.IsZero() {
		return errors.New("session relocation updated time is required")
	}
	target.UpdatedAt = target.UpdatedAt.UTC()
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := strings.TrimSpace(s.meta.SessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if filepath.Base(target.SessionDir) != sessionID {
		return fmt.Errorf("session relocation target %q does not end with session id %q", target.SessionDir, sessionID)
	}
	if err := relocate(); err != nil {
		return err
	}
	s.sessionDir = target.SessionDir
	s.eventsFP = filepath.Join(target.SessionDir, eventsFile)
	s.meta.WorkspaceRoot = target.WorkspaceRoot
	s.meta.WorkspaceContainer = target.WorkspaceContainer
	s.meta.WorktreeReminder = nil
	s.meta.UpdatedAt = target.UpdatedAt
	return nil
}

// RemoveDurable deletes this session's on-disk artifacts after a failed
// creation flow and returns the store to a non-durable state.
func (s *Store) RemoveDurable() error {
	if s == nil {
		return errors.New("session store is required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
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
	return cloneMeta(s.meta)
}

func (s *Store) ConversationFreshness() ConversationFreshness {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversationFreshness
}

func (s *Store) mutateAndPersist(mutator func() error) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
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
	return s.mutateMetaAndReplaceLockedContractWithCommitStatus(nil, func(locked *LockedContract) *LockedContract {
		mutator(locked)
		return locked
	}, true)
}

func (s *Store) mutateMetaAndLockedContractWithCommitStatus(metaMutator func(*Meta), lockedMutator func(*LockedContract), requireLocked bool) (LockedContractMutationResult, error) {
	if lockedMutator == nil {
		return s.mutateMetaAndReplaceLockedContractWithCommitStatus(metaMutator, nil, requireLocked)
	}
	return s.mutateMetaAndReplaceLockedContractWithCommitStatus(metaMutator, func(locked *LockedContract) *LockedContract {
		lockedMutator(locked)
		return locked
	}, requireLocked)
}

func (s *Store) mutateMetaAndReplaceLockedContractWithCommitStatus(metaMutator func(*Meta), lockedMutator func(*LockedContract) *LockedContract, requireLocked bool) (LockedContractMutationResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if requireLocked && s.meta.Locked == nil {
		s.mu.Unlock()
		return LockedContractMutationResult{}, nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return LockedContractMutationResult{}, err
	}
	previousMeta := cloneMeta(s.meta)
	previousMetadataVersion := s.metadataVersion
	previousPersistedMetaVersion := s.persistedMetaVersion
	if metaMutator != nil {
		metaMutator(&s.meta)
	}
	if lockedMutator != nil && s.meta.Locked != nil {
		s.meta.Locked = lockedMutator(cloneLockedContract(s.meta.Locked))
	}
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
	s.mu.Unlock()
	observeErr := s.observePersistence(observation)
	if observeErr != nil {
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
	if s == nil {
		return errors.New("session store is required")
	}
	s.mu.Lock()
	ephemeral := s.options.filelessEvents
	s.mu.Unlock()
	if ephemeral {
		return errEphemeralStoreCannotBeDurable
	}
	return s.mutateAndPersist(func() error { return nil })
}

func (s *Store) SetPendingModelRecovery(recovery PendingModelRecovery) error {
	next := normalizePendingModelRecovery(recovery)
	return s.persistPendingModelRecoveryEvent(eventModelRecoveryPending, next.StepID, next, func() {
		s.meta.PendingModelRecovery = &next
	})
}

func (s *Store) ClearPendingModelRecovery() error {
	current := s.Meta().PendingModelRecovery
	if current == nil {
		return nil
	}
	consumed := clonePendingModelRecovery(current)
	return s.persistPendingModelRecoveryEvent(eventModelRecoveryConsumed, consumed.StepID, consumed, func() {
		s.meta.PendingModelRecovery = nil
	})
}

func (s *Store) ClearPendingModelRecoveryForStep(stepID string) error {
	current := s.Meta().PendingModelRecovery
	if current == nil || strings.TrimSpace(current.StepID) != strings.TrimSpace(stepID) {
		return nil
	}
	consumed := clonePendingModelRecovery(current)
	return s.persistPendingModelRecoveryEvent(eventModelRecoveryConsumed, consumed.StepID, consumed, func() {
		s.meta.PendingModelRecovery = nil
	})
}

func (s *Store) DiscardPendingModelRecoveryCandidate() error {
	current := s.Meta().PendingModelRecovery
	if current == nil {
		return nil
	}
	discarded := clonePendingModelRecovery(current)
	return s.persistPendingModelRecoveryEvent(eventModelRecoveryDiscarded, discarded.StepID, discarded, func() {
		s.meta.PendingModelRecovery = nil
	})
}

func normalizePendingModelRecovery(recovery PendingModelRecovery) PendingModelRecovery {
	next := recovery
	next.RecoveryID = strings.TrimSpace(next.RecoveryID)
	next.StepID = strings.TrimSpace(next.StepID)
	next.Reason = strings.TrimSpace(next.Reason)
	if next.CreatedAt.IsZero() {
		next.CreatedAt = time.Now().UTC()
	}
	next.OutstandingToolCallIDs = append([]string(nil), next.OutstandingToolCallIDs...)
	return next
}

func clonePendingModelRecovery(recovery *PendingModelRecovery) PendingModelRecovery {
	if recovery == nil {
		return PendingModelRecovery{}
	}
	return normalizePendingModelRecovery(*recovery)
}

func (s *Store) persistPendingModelRecoveryEvent(kind string, stepID string, payload PendingModelRecovery, apply func()) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	previousMeta := cloneMeta(s.meta)
	previousFreshness := s.conversationFreshness
	apply()
	evt, err := s.buildEventLocked(stepID, kind, payload, time.Now().UTC())
	if err != nil {
		s.meta = previousMeta
		s.conversationFreshness = previousFreshness
		s.mu.Unlock()
		return err
	}
	observation, committed, err := s.appendEventsAtomicLockedWithCommitStatus([]Event{evt})
	if err != nil && !committed {
		s.meta = previousMeta
		s.conversationFreshness = previousFreshness
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.observePersistence(observation)
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()

	if s.meta.InputDraft == inputDraft && len(s.meta.InputDraftRecoveryBuffers) == 0 && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.InputDraft = inputDraft
	s.meta.InputDraftRecoveryBuffers = nil
	s.meta.UpdatedAt = time.Now().UTC()
	if !s.persisted && inputDraft == "" {
		s.mu.Unlock()
		return nil
	}
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetInputDraftRecovery(inputDraft string, buffers []InputDraftRecoveryBuffer) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	nextBuffers := append([]InputDraftRecoveryBuffer(nil), buffers...)
	if s.meta.InputDraft == inputDraft && inputDraftRecoveryBuffersEqual(s.meta.InputDraftRecoveryBuffers, nextBuffers) && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.InputDraft = inputDraft
	s.meta.InputDraftRecoveryBuffers = nextBuffers
	s.meta.UpdatedAt = time.Now().UTC()
	if !s.persisted && inputDraft == "" && len(nextBuffers) == 0 {
		s.mu.Unlock()
		return nil
	}
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func inputDraftRecoveryBuffersEqual(a, b []InputDraftRecoveryBuffer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) SetHeadlessActive(active bool) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.HeadlessActive == active && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.HeadlessActive = active
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) PromoteSubagentToMain() (bool, error) {
	s.mu.Lock()
	if s.meta.Category == nil || *s.meta.Category == sessioncontract.SessionCategoryMain {
		s.mu.Unlock()
		return false, nil
	}
	if *s.meta.Category != sessioncontract.SessionCategorySubagent {
		raw := string(*s.meta.Category)
		sessionID := s.meta.SessionID
		s.mu.Unlock()
		return false, fmt.Errorf("session %q has invalid category %q", sessionID, raw)
	}
	mainCategory := sessioncontract.SessionCategoryMain
	s.meta.Category = &mainCategory
	s.meta.UpdatedAt = storeTimestamp(s.options)
	return true, s.unlockAndObservePersistence(s.persistMetaLocked())
}

func sessionCategoryPointer(category sessioncontract.SessionCategory) *sessioncontract.SessionCategory {
	return &category
}

func validateMetaCategory(meta *Meta) error {
	if meta == nil || meta.Category == nil {
		return nil
	}
	raw := string(*meta.Category)
	category, err := sessioncontract.ParseSessionCategory(raw)
	if err != nil {
		return fmt.Errorf("session %q has invalid category %q: %w", meta.SessionID, raw, err)
	}
	meta.Category = sessionCategoryPointer(category)
	return nil
}

func (s *Store) SetCompactionSoonReminderIssued(issued bool) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.CompactionSoonReminderIssued == issued && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.CompactionSoonReminderIssued = issued
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetWorktreeReminderState(state *WorktreeReminderState) error {
	var nextState *WorktreeReminderState
	if state != nil {
		normalized, err := NormalizeWorktreeReminderState(*state)
		if err != nil {
			return err
		}
		nextState = &normalized
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.WorktreeReminder != nil && nextState != nil && WorktreeReminderTargetEqual(*s.meta.WorktreeReminder, *nextState) {
		nextState.ContextID = cloneUUID(s.meta.WorktreeReminder.ContextID)
	} else if nextState != nil && nextState.ContextID == nil {
		contextID := uuid.New()
		nextState.ContextID = &contextID
	}
	statesEqual := s.meta.WorktreeReminder == nil && nextState == nil
	if s.meta.WorktreeReminder != nil && nextState != nil {
		statesEqual = WorktreeReminderStateEqual(*s.meta.WorktreeReminder, *nextState)
	}
	if statesEqual && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.WorktreeReminder = CloneWorktreeReminderState(nextState)
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func normalizeMetaWorktreeReminder(meta *Meta) error {
	if meta == nil || meta.WorktreeReminder == nil {
		return nil
	}
	normalized, err := NormalizeWorktreeReminderState(*meta.WorktreeReminder)
	if err != nil {
		return err
	}
	meta.WorktreeReminder = &normalized
	return nil
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()

	normalized := normalizeUsageState(state)
	if usageStatesEqual(s.meta.UsageState, normalized) && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.UsageState = normalized
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetContinuationContext(ctx ContinuationContext) error {
	normalized, err := NormalizeContinuationContext(ctx)
	if err != nil {
		return err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.persisted {
		if err := s.requireMetadataPersistenceLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.meta.Continuation = normalized
	s.meta.UpdatedAt = time.Now().UTC()
	if !s.persisted {
		s.mu.Unlock()
		return nil
	}
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) SetContinuationContextAndMarkLockedPromptFacingContractStale(ctx ContinuationContext) (LockedContractMutationResult, error) {
	normalized, err := NormalizeContinuationContext(ctx)
	if err != nil {
		return LockedContractMutationResult{}, err
	}
	return s.mutateMetaAndLockedContractWithCommitStatus(func(meta *Meta) {
		meta.Continuation = normalized
	}, markLockedPromptFacingContractStale, false)
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

func (s *Store) ResetLockedContractForCompactionBoundary() error {
	_, err := s.mutateMetaAndReplaceLockedContractWithCommitStatus(func(meta *Meta) {
		meta.PromptCacheLineageGeneration++
	}, func(*LockedContract) *LockedContract {
		return nil
	}, false)
	return err
}

func (s *Store) BackfillLockedContextBudget(contextWindow, contextPercent int) error {
	if contextWindow <= 0 || contextPercent <= 0 {
		return nil
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.Locked == nil {
		s.mu.Unlock()
		return nil
	}
	setContextWindow := s.meta.Locked.ContextWindow <= 0
	setContextPercent := s.meta.Locked.ContextPercent <= 0
	if !setContextWindow && !setContextPercent {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if setContextWindow {
		s.meta.Locked.ContextWindow = contextWindow
	}
	if setContextPercent {
		s.meta.Locked.ContextPercent = contextPercent
	}
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) BackfillLockedProviderContract(contract LockedProviderCapabilities) error {
	if strings.TrimSpace(contract.ProviderID) == "" {
		return nil
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.Locked == nil || strings.TrimSpace(s.meta.Locked.ProviderContract.ProviderID) != "" {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.Locked.ProviderContract = contract
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) BackfillLockedSystemPrompt(systemPrompt string) error {
	trimmed := strings.TrimSpace(systemPrompt)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.Locked == nil || s.meta.Locked.HasSystemPrompt {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.meta.Locked.SystemPrompt = trimmed
	s.meta.Locked.HasSystemPrompt = true
	s.meta.UpdatedAt = time.Now().UTC()
	return s.unlockAndObservePersistence(s.persistMetaLocked())
}

func (s *Store) BackfillLockedReviewerPrompt(reviewerPrompt string) error {
	trimmed := strings.TrimSpace(reviewerPrompt)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.meta.Locked == nil || s.meta.Locked.HasReviewerPrompt {
		s.mu.Unlock()
		return nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return err
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

func (s *Store) MarkLockedPromptFacingContractStale() (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(markLockedPromptFacingContractStale)
}

func markLockedPromptFacingContractStale(locked *LockedContract) {
	locked.SystemPrompt = ""
	locked.HasSystemPrompt = false
	locked.ReviewerPrompt = ""
	locked.HasReviewerPrompt = false
	locked.EnabledTools = nil
	locked.HasEnabledTools = false
	locked.WebSearchMode = ""
	locked.ToolPreambles = nil
}

func (s *Store) RefreshLockedMainPromptSnapshot(snapshot LockedMainPromptSnapshot) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		locked.SystemPrompt = strings.TrimSpace(snapshot.SystemPrompt)
		locked.HasSystemPrompt = snapshot.HasSystemPrompt
		locked.ToolPreambles = valuecopy.Pointer(snapshot.ToolPreambles)
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

type EventAppendResult struct {
	Event         Event
	Committed     bool
	EndByteCursor *int64
}

type eventAppendOutcome struct {
	event         Event
	committed     bool
	endByteCursor *int64
}

func (s *Store) AppendEvent(stepID, kind string, payload any) (Event, bool, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	outcome, err := s.appendEventLocked(stepID, kind, payload)
	return outcome.event, outcome.committed, err
}

func (s *Store) AppendEventWithEndByteCursor(stepID, kind string, payload any) (EventAppendResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.options.filelessEvents {
		s.mu.Unlock()
		return EventAppendResult{}, errors.New("event-log byte cursor is unavailable with fileless event persistence")
	}
	outcome, err := s.appendEventLocked(stepID, kind, payload)
	result := EventAppendResult{
		Event:         outcome.event,
		Committed:     outcome.committed,
		EndByteCursor: valuecopy.Pointer(outcome.endByteCursor),
	}
	if err != nil {
		return result, err
	}
	if result.EndByteCursor == nil || *result.EndByteCursor <= 0 {
		return result, errors.New("committed event append did not produce a positive event-log byte cursor")
	}
	return result, nil
}

func (s *Store) appendEventLocked(stepID, kind string, payload any) (eventAppendOutcome, error) {
	evt, err := s.buildEventLocked(stepID, kind, payload, time.Now().UTC())
	if err != nil {
		s.mu.Unlock()
		return eventAppendOutcome{}, err
	}
	committed, endByteCursor, err := s.appendObservedEventsLockedWithCommitStatus([]Event{evt})
	return eventAppendOutcome{
		event:         evt,
		committed:     committed,
		endByteCursor: endByteCursor,
	}, err
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
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
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
	if _, _, err := s.appendObservedEventsLockedWithCommitStatus(built); err != nil {
		return nil, err
	}
	return built, nil
}

type ReplayEvent struct {
	StepID  string
	Kind    string
	Payload json.RawMessage
}

type replayEventsAppendOutcome struct {
	events        []Event
	endByteCursor *int64
}

func (s *Store) AppendReplayEvents(events []ReplayEvent) ([]Event, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	outcome, err := s.appendReplayEventsLocked(events)
	if err != nil {
		return nil, err
	}
	return outcome.events, nil
}

func (s *Store) appendReplayEventsWithEndByteCursor(events []ReplayEvent) (replayEventsAppendOutcome, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.options.filelessEvents {
		s.mu.Unlock()
		return replayEventsAppendOutcome{}, errors.New("event-log byte cursor is unavailable with fileless event persistence")
	}
	outcome, err := s.appendReplayEventsLocked(events)
	if err != nil {
		return outcome, err
	}
	if outcome.endByteCursor == nil || *outcome.endByteCursor <= 0 {
		return outcome, errors.New("replayed events did not produce a positive event-log byte cursor")
	}
	return outcome, nil
}

func (s *Store) appendReplayEventsLocked(events []ReplayEvent) (replayEventsAppendOutcome, error) {
	if len(events) == 0 {
		s.mu.Unlock()
		return replayEventsAppendOutcome{}, nil
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
	_, endByteCursor, err := s.appendObservedEventsLockedWithCommitStatus(built)
	if err != nil {
		return replayEventsAppendOutcome{events: built, endByteCursor: endByteCursor}, err
	}
	return replayEventsAppendOutcome{events: built, endByteCursor: endByteCursor}, nil
}

func (s *Store) appendObservedEventsLockedWithCommitStatus(events []Event) (bool, *int64, error) {
	previousMeta := cloneMeta(s.meta)
	previousFreshness := s.conversationFreshness
	s.captureFirstPromptPreviewLocked(events)
	s.advanceConversationFreshnessLocked(events)
	observation, committed, err := s.appendEventsAtomicLockedWithCommitStatus(events)
	var endByteCursor *int64
	if committed && !s.options.filelessEvents {
		cursor := s.eventsFileSizeBytes
		endByteCursor = &cursor
	}
	if err != nil && !committed {
		s.meta = previousMeta
		s.conversationFreshness = previousFreshness
	}
	s.mu.Unlock()
	if err != nil {
		return committed, endByteCursor, err
	}
	return committed, endByteCursor, s.observePersistence(observation)
}

type EventInput struct {
	Kind    string
	Payload any
}

func (s *Store) ReadEventsBackwardUntil(match func(Event) bool) ([]Event, error) {
	window, err := s.ReadNewestSegmentBackward(match)
	if err != nil {
		return nil, err
	}
	return window.Events, nil
}

func (s *Store) ReadNewestSegmentBackward(match func(Event) bool) (SegmentWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persisted {
		return SegmentWindow{ReachedStart: true, ReachedEnd: true}, nil
	}
	return readNewestSegmentBackwardFile(s.eventsFP, activeTailReverseChunkBytes, match)
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

func (s *Store) persistMetaLocked() (*persistenceObservation, error) {
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		return nil, err
	}
	if s.options.filelessEvents {
		s.metadataVersion++
		return nil, nil
	}
	if err := s.ensurePersistedLocked(); err != nil {
		return nil, err
	}
	s.metadataVersion++
	observation := &persistenceObservation{snapshot: s.persistenceSnapshotLocked(), version: s.metadataVersion}
	return observation, nil
}

func (s *Store) hasDurableMetadataLocked() bool {
	if s == nil || !s.persisted || s.options.filelessEvents {
		return false
	}
	return s.metadataVersion != 0 && s.persistedMetaVersion == s.metadataVersion
}

func (s *Store) appendEventsAtomicLockedWithCommitStatus(events []Event) (*persistenceObservation, bool, error) {
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		return nil, false, err
	}

	if s.options.filelessEvents {
		for _, e := range events {
			s.meta.LastSequence = e.Seq
		}
		s.meta.UpdatedAt = time.Now().UTC()
		snapshot, err := s.persistMetaLocked()
		if err != nil {
			return nil, false, err
		}
		return snapshot, true, nil
	}

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
	if s == nil || !s.persisted || s.options.observer == nil || s.options.filelessEvents {
		return nil
	}
	snapshot := PersistedStoreSnapshot{
		SessionDir: s.sessionDir,
		Meta:       cloneMeta(s.meta),
	}
	return &snapshot
}

func (s *Store) requireMetadataPersistenceLocked() error {
	if s.options.filelessEvents {
		return nil
	}
	if s.options.observer == nil {
		return errPersistenceObserverRequired
	}
	return nil
}

func (s *Store) observePersistence(observation *persistenceObservation) error {
	if observation == nil {
		return nil
	}
	if s == nil || observation.snapshot == nil || s.options.observer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.options.observerTimeout)
	defer cancel()
	if err := s.options.observer.ObservePersistedStore(ctx, *observation.snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	if observation.version > s.persistedMetaVersion {
		s.persistedMetaVersion = observation.version
	}
	s.mu.Unlock()
	return nil
}

func (s *Store) observeEventLogReconciliation(observation *eventLogReconciliationObservation) error {
	if observation == nil {
		return nil
	}
	if s == nil || s.options.reconciler == nil {
		return errEventLogReconcilerRequired
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.options.observerTimeout)
	defer cancel()
	if err := s.options.reconciler.ObserveEventLogReconciliation(ctx, observation.reconciliation); err != nil {
		return err
	}
	s.mu.Lock()
	if observation.version > s.persistedMetaVersion {
		s.persistedMetaVersion = observation.version
	}
	s.mu.Unlock()
	return nil
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
