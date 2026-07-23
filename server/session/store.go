package session

import (
	"context"
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
	"core/shared/textutil"
	"github.com/google/uuid"
)

const eventsFile = "events.jsonl"

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

type InvalidSessionCategoryError struct {
	SessionID string
	Category  sessioncontract.SessionCategory
	Err       error
}

func (e InvalidSessionCategoryError) Error() string {
	return fmt.Sprintf("session %q has invalid category %q: %v", e.SessionID, e.Category, e.Err)
}

func (e InvalidSessionCategoryError) Unwrap() error {
	return e.Err
}

type Store struct {
	mu                      sync.Mutex
	mutationMu              sync.Mutex
	sessionDir              string
	eventsFP                string
	meta                    Meta
	conversationFreshness   ConversationFreshness
	persisted               bool
	metadataVersion         uint64
	persistedMetaVersion    uint64
	options                 storeOptions
	materializedEventLog    *currentEventLog
	eventLogMaterialization *eventLogMaterializationSnapshot
	recoveryErr             error
}

type persistenceObservation struct {
	snapshot *PersistedStoreSnapshot
	version  uint64
}

type metadataMutationCheckpoint struct {
	meta                 Meta
	metadataVersion      uint64
	persistedMetaVersion uint64
}

type sessionDescriptorData struct {
	sessionID runtimeids.SessionID
}

type sessionDescriptorVariant interface {
	descriptorData() sessionDescriptorData
}

type openSessionDescriptor struct {
	sessionDescriptorData
	containerDir *string
}

func (d openSessionDescriptor) descriptorData() sessionDescriptorData {
	return d.sessionDescriptorData
}

type createSessionDescriptor struct {
	sessionDescriptorData
	containerDir  string
	containerName string
	workspaceRoot string
	category      sessioncontract.SessionCategory
}

func (d createSessionDescriptor) descriptorData() sessionDescriptorData {
	return d.sessionDescriptorData
}

type SessionDescriptor struct {
	value sessionDescriptorVariant
}

func (d SessionDescriptor) Validate() error {
	if d.value == nil || d.value.descriptorData().sessionID.IsZero() {
		return errors.New("session descriptor is required")
	}
	return nil
}

func NewOpenSessionDescriptor(sessionID runtimeids.SessionID) (SessionDescriptor, error) {
	if sessionID.IsZero() {
		return SessionDescriptor{}, errors.New("session id is required")
	}
	return SessionDescriptor{value: openSessionDescriptor{
		sessionDescriptorData: sessionDescriptorData{sessionID: sessionID},
	}}, nil
}

func NewScopedOpenSessionDescriptor(sessionID runtimeids.SessionID, containerDir string) (SessionDescriptor, error) {
	if sessionID.IsZero() {
		return SessionDescriptor{}, errors.New("session id is required")
	}
	if strings.TrimSpace(containerDir) == "" {
		return SessionDescriptor{}, errors.New("session container directory is required")
	}
	scopedContainer := filepath.Clean(containerDir)
	return SessionDescriptor{value: openSessionDescriptor{
		sessionDescriptorData: sessionDescriptorData{sessionID: sessionID},
		containerDir:          &scopedContainer,
	}}, nil
}

func NewCreateSessionDescriptor(
	sessionID runtimeids.SessionID,
	containerDir string,
	containerName string,
	workspaceRoot string,
	category sessioncontract.SessionCategory,
) (SessionDescriptor, error) {
	if sessionID.IsZero() || !sessionID.IsCanonicalUUIDv4() {
		return SessionDescriptor{}, errors.New("new session id must be a canonical UUIDv4")
	}
	validatedCategory, err := sessioncontract.ParseSessionCategory(string(category))
	if err != nil {
		return SessionDescriptor{}, err
	}
	return SessionDescriptor{value: createSessionDescriptor{
		sessionDescriptorData: sessionDescriptorData{sessionID: sessionID},
		containerDir:          containerDir,
		containerName:         containerName,
		workspaceRoot:         workspaceRoot,
		category:              validatedCategory,
	}}, nil
}

func (d SessionDescriptor) SessionID() runtimeids.SessionID {
	if err := d.Validate(); err != nil {
		panic(err.Error())
	}
	return d.value.descriptorData().sessionID
}

func MaterializeSessionDescriptor(persistenceRoot string, descriptor SessionDescriptor, options ...StoreOption) (*Store, error) {
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	switch value := descriptor.value.(type) {
	case openSessionDescriptor:
		if value.containerDir != nil {
			sessionDir, err := ResolveScopedSessionDir(*value.containerDir, value.sessionID.String())
			if err != nil {
				return nil, err
			}
			return Open(sessionDir, options...)
		}
		return OpenByID(persistenceRoot, value.sessionID.String(), options...)
	case createSessionDescriptor:
		store, err := NewLazyWithID(
			value.sessionID,
			value.containerDir,
			value.containerName,
			value.workspaceRoot,
			value.category,
			options...,
		)
		if err != nil {
			return nil, err
		}
		if err := InitializeCreationContext(
			store,
			nil,
			SessionCreationSourceIndependent,
			ChildContextOptions{},
		); err != nil {
			return nil, err
		}
		if err := store.EnsureDurable(); err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, fmt.Errorf("unsupported session descriptor %T", descriptor.value)
	}
}

func Create(workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, options ...StoreOption) (*Store, error) {
	s, err := NewLazy(workspaceContainerDir, workspaceContainerName, workspaceRoot, category, options...)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
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
	return NewLazyWithID(runtimeids.NewSessionID(), workspaceContainerDir, workspaceContainerName, workspaceRoot, category, options...)
}

func NewLazyWithID(sessionID runtimeids.SessionID, workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, options ...StoreOption) (*Store, error) {
	if sessionID.IsZero() || !sessionID.IsCanonicalUUIDv4() {
		return nil, errors.New("new session id must be a canonical UUIDv4")
	}
	storeOpts := normalizeStoreOptions(options...)
	return newLazyWithIDAndStoreOptions(sessionID, workspaceContainerDir, workspaceContainerName, workspaceRoot, category, storeOpts)
}

func newLazyWithStoreOptions(workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, storeOpts storeOptions) (*Store, error) {
	return newLazyWithIDAndStoreOptions(runtimeids.NewSessionID(), workspaceContainerDir, workspaceContainerName, workspaceRoot, category, storeOpts)
}

func newLazyWithIDAndStoreOptions(sessionID runtimeids.SessionID, workspaceContainerDir, workspaceContainerName, workspaceRoot string, category sessioncontract.SessionCategory, storeOpts storeOptions) (*Store, error) {
	validatedCategory, err := sessioncontract.ParseSessionCategory(string(category))
	if err != nil {
		return nil, err
	}
	sid := sessionID.String()
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
	return resolveAndOpenPersistedSession(storeOpts, func() (PersistedSessionRecord, error) {
		resolvedMeta, err := resolvePersistedSessionMetaForDir(sessionDir, storeOpts)
		if err != nil {
			return PersistedSessionRecord{}, err
		}
		return PersistedSessionRecord{SessionDir: sessionDir, Meta: resolvedMeta}, nil
	})
}

func OpenByID(persistenceRoot, sessionID string, options ...StoreOption) (*Store, error) {
	storeOpts := normalizeStoreOptions(options...)
	return resolveAndOpenPersistedSession(storeOpts, func() (PersistedSessionRecord, error) {
		return resolvePersistedSessionRecord(persistenceRoot, sessionID, storeOpts)
	})
}

func resolveAndOpenPersistedSession(storeOpts storeOptions, resolve func() (PersistedSessionRecord, error)) (*Store, error) {
	record, err := resolve()
	if err != nil {
		return nil, err
	}
	return openPersistedSession(record.SessionDir, record.Meta, storeOpts)
}

// OpenResolved opens an authoritative persisted-session record without
// resolving the session identity a second time.
func OpenResolved(record PersistedSessionRecord, options ...StoreOption) (*Store, error) {
	if record.Meta == nil {
		return nil, errResolverRecordMissingMetadata
	}
	if err := validatePersistedSessionRecord(record.Meta.SessionID, record); err != nil {
		return nil, err
	}
	return openPersistedSession(record.SessionDir, record.Meta, normalizeStoreOptions(options...))
}

func openPersistedSession(
	sessionDir string,
	resolvedMeta *Meta,
	storeOpts storeOptions,
) (_ *Store, resultErr error) {
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
	lock, lockPath, err := acquireEventLogPersistenceLock(sessionDir)
	if err != nil {
		return nil, err
	}
	defer joinEventLogPersistenceLockRelease(&resultErr, lock, lockPath)
	if err := s.recoverAppendTransactionWithEventLogLockHeld(); err != nil {
		return nil, err
	}
	s.metadataVersion = 1
	s.persistedMetaVersion = 1
	if s.meta.ConversationEstablished {
		s.conversationFreshness = ConversationFreshnessEstablished
	} else {
		s.conversationFreshness = ConversationFreshnessFresh
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
	if s.materializedEventLog != nil {
		s.materializedEventLog.path = s.eventsFP
	}
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
	s.persistedMetaVersion = 0
	s.materializedEventLog = nil
	s.eventLogMaterialization = nil
	return nil
}

type metaSnapshot struct {
	meta                  Meta
	conversationFreshness ConversationFreshness
}

func (s *Store) Meta() Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMeta(s.meta)
}

func (s *Store) metaSnapshot() metaSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return metaSnapshot{
		meta:                  cloneMeta(s.meta),
		conversationFreshness: s.conversationFreshness,
	}
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
}

func (s *Store) unlockAndObservePersistence(observation *persistenceObservation, err error) error {
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.observePersistence(observation)
}

func (s *Store) metadataMutationCheckpointLocked() metadataMutationCheckpoint {
	return metadataMutationCheckpoint{
		meta:                 cloneMeta(s.meta),
		metadataVersion:      s.metadataVersion,
		persistedMetaVersion: s.persistedMetaVersion,
	}
}

func (s *Store) restoreMetadataMutationLocked(checkpoint metadataMutationCheckpoint) {
	s.meta = checkpoint.meta
	s.metadataVersion = checkpoint.metadataVersion
	s.persistedMetaVersion = checkpoint.persistedMetaVersion
}

func (s *Store) closeMutationAuthorityLocked(operation string, err error) error {
	recoveryErr := s.recoveryError(operation, err)
	s.recoveryErr = recoveryErr
	return recoveryErr
}

func (s *Store) recoveryError(operation string, err error) error {
	return storeRecoveryError(s.meta.SessionID, operation, err)
}

func (s *Store) persistMetadataMutationWithCommitReceiptLocked(checkpoint metadataMutationCheckpoint) (CommitReceipt, error) {
	observation, err := s.persistMetaAfterRecoveryVerifiedLocked()
	if err != nil {
		s.restoreMetadataMutationLocked(checkpoint)
		s.mu.Unlock()
		return CommitReceipt{}, err
	}
	record, recordErr := s.newAppendRecoveryRecord(checkpoint.meta, s.meta, appendRecoveryCommitted, nil)
	if recordErr == nil {
		recordErr = s.writeAppendRecoveryRecord(record)
	}
	if recordErr != nil {
		s.restoreMetadataMutationLocked(checkpoint)
		if cleanupErr := s.clearAppendRecoveryRecord(); cleanupErr != nil {
			recordErr = s.closeMutationAuthorityLocked("rollback metadata recovery", errors.Join(recordErr, cleanupErr))
		}
		s.mu.Unlock()
		return CommitReceipt{}, recordErr
	}
	s.mu.Unlock()
	return CommitReceipt{Committed: true},
		s.observePersistenceAndClearAppendRecovery(observation)
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
	checkpoint := s.metadataMutationCheckpointLocked()
	if metaMutator != nil {
		metaMutator(&s.meta)
	}
	if lockedMutator != nil && s.meta.Locked != nil {
		s.meta.Locked = lockedMutator(cloneLockedContract(s.meta.Locked))
	}
	s.meta.UpdatedAt = time.Now().UTC()
	committed := cloneLockedContract(s.meta.Locked)
	receipt, err := s.persistMetadataMutationWithCommitReceiptLocked(checkpoint)
	if !receipt.Committed {
		return LockedContractMutationResult{
			CommitReceipt: receipt,
			Locked:        cloneLockedContract(checkpoint.meta.Locked),
		}, err
	}
	return LockedContractMutationResult{
		CommitReceipt: receipt,
		Locked:        committed,
	}, err
}

func (s *Store) EnsureDurable() error {
	if s == nil {
		return errors.New("session store is required")
	}
	return s.mutateAndPersist(func() error { return nil })
}

func (s *Store) SetPendingModelRecovery(recovery PendingModelRecovery) error {
	next := normalizePendingModelRecovery(recovery)
	return s.mutateAndPersist(func() error {
		s.meta.PendingModelRecovery = &next
		s.meta.UpdatedAt = storeTimestamp(s.options)
		return nil
	})
}

func (s *Store) ClearPendingModelRecovery() error {
	current := s.Meta().PendingModelRecovery
	if current == nil {
		return nil
	}
	return s.mutateAndPersist(func() error {
		s.meta.PendingModelRecovery = nil
		s.meta.UpdatedAt = storeTimestamp(s.options)
		return nil
	})
}

func (s *Store) ClearPendingModelRecoveryForStep(stepID string) error {
	current := s.Meta().PendingModelRecovery
	if current == nil || strings.TrimSpace(current.StepID) != strings.TrimSpace(stepID) {
		return nil
	}
	return s.mutateAndPersist(func() error {
		s.meta.PendingModelRecovery = nil
		s.meta.UpdatedAt = storeTimestamp(s.options)
		return nil
	})
}

func (s *Store) DiscardPendingModelRecoveryCandidate() error {
	current := s.Meta().PendingModelRecovery
	if current == nil {
		return nil
	}
	return s.mutateAndPersist(func() error {
		s.meta.PendingModelRecovery = nil
		s.meta.UpdatedAt = storeTimestamp(s.options)
		return nil
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
}

func (s *Store) PromoteSubagentToMain() (bool, error) {
	s.mu.Lock()
	if s.meta.Category == nil || *s.meta.Category == sessioncontract.SessionCategoryMain {
		s.mu.Unlock()
		return false, nil
	}
	if *s.meta.Category != sessioncontract.SessionCategorySubagent {
		category := *s.meta.Category
		sessionID := s.meta.SessionID
		s.mu.Unlock()
		_, err := sessioncontract.ParseSessionCategory(string(category))
		if err == nil {
			panic(fmt.Sprintf("unsupported session category %q passed category validation", category))
		}
		return false, InvalidSessionCategoryError{SessionID: sessionID, Category: category, Err: err}
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
		return InvalidSessionCategoryError{SessionID: meta.SessionID, Category: *meta.Category, Err: err}
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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

func (s *Store) SetGoal(objective string, actor GoalActor) (GoalState, CommitReceipt, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	goal, err := prepareActiveGoalState(
		GoalState{Objective: objective},
		actor,
		s.meta.Goal,
		storeTimestamp(s.options),
	)
	if err != nil {
		s.mu.Unlock()
		return GoalState{}, CommitReceipt{}, err
	}
	checkpoint := s.metadataMutationCheckpointLocked()
	s.meta.Goal = cloneGoalState(&goal)
	receipt, err := s.persistGoalMetadataLocked(checkpoint)
	if !receipt.Committed {
		return GoalState{}, receipt, err
	}
	return goal, receipt, err
}

func (s *Store) SetGoalStatus(status GoalStatus, actor GoalActor) (GoalState, bool, CommitReceipt, error) {
	return s.transitionGoalStatus(status, actor, nil)
}

func (s *Store) CompleteGoalIfActive(expectedID string, actor GoalActor) (GoalState, bool, CommitReceipt, error) {
	return s.transitionGoalStatus(GoalStatusComplete, actor, func(current GoalState) bool {
		return current.ID == expectedID && current.Status == GoalStatusActive
	})
}

func (s *Store) transitionGoalStatus(
	status GoalStatus,
	actor GoalActor,
	allow func(GoalState) bool,
) (GoalState, bool, CommitReceipt, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	goal, transitioned, err := prepareGoalStatusState(
		s.meta.Goal,
		status,
		actor,
		allow,
		storeTimestamp(s.options),
	)
	if err != nil {
		s.mu.Unlock()
		return GoalState{}, false, CommitReceipt{}, err
	}
	if !transitioned {
		s.mu.Unlock()
		return goal, false, CommitReceipt{}, nil
	}
	checkpoint := s.metadataMutationCheckpointLocked()
	s.meta.Goal = cloneGoalState(&goal)
	receipt, err := s.persistGoalMetadataLocked(checkpoint)
	if !receipt.Committed {
		return GoalState{}, false, receipt, err
	}
	return goal, true, receipt, err
}

func (s *Store) ClearGoal(actor GoalActor) (GoalState, CommitReceipt, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	goal, err := prepareGoalClearState(s.meta.Goal, actor)
	if err != nil {
		s.mu.Unlock()
		return GoalState{}, CommitReceipt{}, err
	}
	checkpoint := s.metadataMutationCheckpointLocked()
	s.meta.Goal = nil
	receipt, err := s.persistGoalMetadataLocked(checkpoint)
	if !receipt.Committed {
		return GoalState{}, receipt, err
	}
	return goal, receipt, err
}

func (s *Store) persistGoalMetadataLocked(checkpoint metadataMutationCheckpoint) (CommitReceipt, error) {
	s.meta.UpdatedAt = storeTimestamp(s.options)
	return s.persistMetadataMutationWithCommitReceiptLocked(checkpoint)
}

func prepareActiveGoalState(
	goal GoalState,
	actor GoalActor,
	current *GoalState,
	now time.Time,
) (GoalState, error) {
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
	current = cloneGoalState(current)
	if normalizedActor == GoalActorAgent && current != nil && current.Status != GoalStatusComplete {
		return GoalState{}, GoalAgentOverwriteBlockedError{Goal: *current}
	}
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = now
	}
	if goal.UpdatedAt.IsZero() {
		goal.UpdatedAt = goal.CreatedAt
	}
	goal.CreatedAt = goal.CreatedAt.UTC().Round(0)
	goal.UpdatedAt = goal.UpdatedAt.UTC().Round(0)
	return goal, nil
}

func prepareGoalStatusState(
	current *GoalState,
	status GoalStatus,
	actor GoalActor,
	allow func(GoalState) bool,
	now time.Time,
) (GoalState, bool, error) {
	normalizedStatus, err := normalizeGoalStatus(status)
	if err != nil {
		return GoalState{}, false, err
	}
	if _, err := normalizeGoalActor(actor); err != nil {
		return GoalState{}, false, err
	}
	current = cloneGoalState(current)
	if current == nil {
		if allow != nil {
			return GoalState{}, false, nil
		}
		return GoalState{}, false, errors.New("goal is not set")
	}
	if allow != nil && !allow(*current) {
		return GoalState{}, false, nil
	}
	current.Status = normalizedStatus
	current.UpdatedAt = now.UTC().Round(0)
	return *current, true, nil
}

func prepareGoalClearState(current *GoalState, actor GoalActor) (GoalState, error) {
	if _, err := normalizeGoalActor(actor); err != nil {
		return GoalState{}, err
	}
	current = cloneGoalState(current)
	if current == nil {
		return GoalState{}, errors.New("goal is not set")
	}
	return *current, nil
}

func storeTimestamp(options storeOptions) time.Time {
	now := time.Now().UTC()
	if options.now != nil {
		now = options.now()
	}
	return now.UTC().Round(0)
}

func (s *Store) SetUsageState(state *UsageState) (CommitReceipt, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()

	normalized := normalizeUsageState(state)
	if usageStatesEqual(s.meta.UsageState, normalized) && (!s.persisted || s.hasDurableMetadataLocked()) {
		s.mu.Unlock()
		return CommitReceipt{Committed: true}, nil
	}
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		s.mu.Unlock()
		return CommitReceipt{}, err
	}
	checkpoint := s.metadataMutationCheckpointLocked()
	s.meta.UsageState = normalized
	s.meta.UpdatedAt = time.Now().UTC()
	return s.persistMetadataMutationWithCommitReceiptLocked(checkpoint)
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
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
	return s.unlockAndObservePersistence(s.persistMetaAfterRecoveryVerifiedLocked())
}

func (s *Store) MarkLockedPromptFacingSnapshotsStale() (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		*locked = locked.WithPromptFacingSnapshotsStale()
	})
}

func (s *Store) MarkLockedPromptFacingContractStale() (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(markLockedPromptFacingContractStale)
}

func markLockedPromptFacingContractStale(locked *LockedContract) {
	*locked = locked.WithPromptFacingSnapshotsStale()
	locked.EnabledTools = nil
	locked.HasEnabledTools = false
	locked.WebSearchMode = ""
	locked.ToolPreambles = nil
}

func (s *Store) RefreshLockedMainPromptSnapshot(snapshot LockedMainPromptSnapshot) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		snapshot.SystemPrompt = strings.TrimSpace(snapshot.SystemPrompt)
		snapshot.ToolPreambles = textutil.Pointer(snapshot.ToolPreambles)
		*locked = locked.WithMainPromptSnapshot(snapshot)
	})
}

func (s *Store) RefreshLockedReviewerPromptSnapshot(snapshot LockedReviewerPromptSnapshot) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		snapshot.ReviewerPrompt = strings.TrimSpace(snapshot.ReviewerPrompt)
		*locked = locked.WithReviewerPromptSnapshot(snapshot)
	})
}

func (s *Store) BackfillLockedRequestShape(fields LockedRequestShapeBackfill) (LockedContractMutationResult, error) {
	return s.mutateLockedContractWithCommitStatus(func(locked *LockedContract) {
		fields.WebSearchMode = strings.TrimSpace(fields.WebSearchMode)
		*locked = locked.WithRequestShape(fields)
	})
}

func (s *Store) persistMetaLocked() (*persistenceObservation, error) {
	if err := s.requireMetadataPersistenceLocked(); err != nil {
		return nil, err
	}
	return s.persistMetaAfterRecoveryVerifiedLocked()
}

// persistMetaAfterRecoveryVerifiedLocked advances metadata only while the
// caller retains s.mu after requireMetadataPersistenceLocked succeeds.
func (s *Store) persistMetaAfterRecoveryVerifiedLocked() (*persistenceObservation, error) {
	if err := s.ensurePersistedLocked(); err != nil {
		return nil, err
	}
	s.metadataVersion++
	observation := &persistenceObservation{snapshot: s.persistenceSnapshotLocked(), version: s.metadataVersion}
	return observation, nil
}

func (s *Store) hasDurableMetadataLocked() bool {
	if s == nil || !s.persisted {
		return false
	}
	return s.metadataVersion != 0 && s.persistedMetaVersion == s.metadataVersion
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
	if err := initializeEventLogPersistenceLock(s.sessionDir); err != nil {
		return err
	}
	s.persisted = true
	return nil
}

func (s *Store) persistenceSnapshotLocked() *PersistedStoreSnapshot {
	if s == nil || !s.persisted || s.options.observer == nil {
		return nil
	}
	snapshot := PersistedStoreSnapshot{
		SessionDir: s.sessionDir,
		Meta:       cloneMeta(s.meta),
	}
	return &snapshot
}

func (s *Store) requireMetadataPersistenceLocked() error {
	if s.recoveryErr != nil {
		return s.recoveryErr
	}
	if s.options.observer == nil {
		return errPersistenceObserverRequired
	}
	record, err := s.readAppendRecoveryRecord()
	if err != nil || record == nil {
		return err
	}
	digest, err := digestMeta(s.meta)
	if err != nil {
		return err
	}
	if record.Phase != appendRecoveryCommitted || digest != record.Post.SHA256 {
		return s.closeMutationAuthorityLocked("supersede unresolved recovery", errors.New("pending recovery does not describe current metadata"))
	}
	observation := &persistenceObservation{snapshot: s.persistenceSnapshotLocked(), version: s.metadataVersion}
	s.mu.Unlock()
	err = s.observePersistenceAndClearAppendRecovery(observation)
	s.mu.Lock()
	return err
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

func (s *Store) observePersistenceAndClearAppendRecovery(
	observation *persistenceObservation,
) error {
	if err := s.observePersistence(observation); err != nil {
		return err
	}
	if observation == nil || observation.snapshot == nil {
		return nil
	}
	if err := s.clearAppendRecoveryRecord(); err != nil {
		return storeRecoveryError(observation.snapshot.Meta.SessionID, "clear committed mutation", err)
	}
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
