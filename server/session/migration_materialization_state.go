package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

const (
	eventLogMigrationLockFile                = "events.jsonl.lock"
	eventLogMigrationWorkspaceDir            = "events.jsonl.migration"
	eventLogMigrationWorkspaceMarkerFile     = "kent-session-events-migration-v1"
	eventLogMigrationStagedLogFile           = "staged-events.jsonl"
	eventLogMigrationWorkspaceMarkerContents = "kent.session.events.migration.workspace.v1\n"
)

type eventLogMaterializationState uint8

const (
	eventLogUnmaterialized eventLogMaterializationState = iota
	eventLogCurrentReconciliationPending
	eventLogCurrent
)

type eventLogSourceClassification uint8

const (
	eventLogSourceMissing eventLogSourceClassification = iota + 1
	eventLogSourceEmpty
	eventLogSourceLegacy
	eventLogSourceCurrent
	eventLogSourceNewer
	eventLogSourceMalformed
)

type eventLogPreparationResult struct {
	State            eventLogMaterializationState
	Source           eventLogSourceClassification
	FoundVersion     *int
	SupportedVersion int
}

type eventLogMaterializationSnapshot struct {
	state        eventLogMaterializationState
	source       eventLogSourceClassification
	foundVersion *int
}

type eventLogSourceClassificationResult struct {
	source       eventLogSourceClassification
	foundVersion *int
}

// UnsupportedEventLogVersionError preserves the source unchanged. Slice 12
// will map it to the client-visible upgrade requirement.
type UnsupportedEventLogVersionError struct {
	FoundVersion     int
	SupportedVersion int
}

func (e UnsupportedEventLogVersionError) Error() string {
	return fmt.Sprintf(
		"session event log version %d is newer than supported version %d",
		e.FoundVersion,
		e.SupportedVersion,
	)
}

type malformedEventLogHeaderReason uint8

const (
	malformedEventLogHeaderMissingField malformedEventLogHeaderReason = iota + 1
	malformedEventLogHeaderInvalidField
	malformedEventLogHeaderUnexpectedContract
	malformedEventLogHeaderUnsupportedVersion
	malformedEventLogHeaderUnterminated
)

// MalformedEventLogHeaderError identifies a structurally current-looking
// header that cannot be treated as headerless legacy input.
type MalformedEventLogHeaderError struct {
	Reason malformedEventLogHeaderReason
}

func (e MalformedEventLogHeaderError) Error() string {
	return fmt.Sprintf("malformed session event-log header: reason=%d", e.Reason)
}

type unknownEventLogMigrationWorkspaceContentError struct {
	Name string
}

func (e unknownEventLogMigrationWorkspaceContentError) Error() string {
	return fmt.Sprintf("unknown event-log migration workspace content %q", e.Name)
}

type invalidEventLogMigrationWorkspaceError struct {
	Reason string
}

func (e invalidEventLogMigrationWorkspaceError) Error() string {
	return fmt.Sprintf("invalid event-log migration workspace: %s", e.Reason)
}

// prepareEventLogMaterialization establishes the durable source state needed
// by the authoritative legacy transformer. It deliberately does not transform
// a nonempty legacy source or reconcile metadata; those operations belong to
// later ordered slices.
func (s *Store) prepareEventLogMaterialization() (eventLogPreparationResult, error) {
	if s == nil {
		return eventLogPreparationResult{}, errors.New("session store is required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.prepareEventLogMaterializationWithMutationHeld()
}

func (s *Store) prepareEventLogMaterializationWithMutationHeld() (
	result eventLogPreparationResult,
	resultErr error,
) {
	s.mu.Lock()
	if !s.persisted {
		s.mu.Unlock()
		return eventLogPreparationResult{}, errors.New(
			"event-log materialization preparation requires durable session metadata",
		)
	}
	sessionDir := s.sessionDir
	s.mu.Unlock()

	lock, lockPath, err := acquireEventLogMigrationLock(sessionDir)
	if err != nil {
		return eventLogPreparationResult{}, wrapEventLogPreparationError(false, err)
	}
	committed := false
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("release event-log migration lock %s: %w", lockPath, closeErr),
			)
		}
		resultErr = wrapEventLogPreparationError(committed, resultErr)
	}()

	result, committed, resultErr = s.prepareEventLogMaterializationWithStableLockHeld()
	return result, resultErr
}

func (s *Store) prepareEventLogMaterializationWithStableLockHeld() (
	result eventLogPreparationResult,
	committed bool,
	resultErr error,
) {
	s.mu.Lock()
	sessionDir := s.sessionDir
	eventsPath := s.eventsFP
	s.mu.Unlock()

	workspace := eventLogMigrationWorkspacePath(sessionDir)
	if err := cleanupEventLogMigrationWorkspace(workspace); err != nil {
		s.clearEventLogMaterialization()
		return eventLogPreparationResult{}, false, err
	}
	s.clearEventLogMaterialization()

	classificationResult, err := classifyEventLogSource(eventsPath)
	if err != nil {
		if classificationResult != nil {
			s.setEventLogMaterializationState(
				eventLogUnmaterialized,
				classificationResult.source,
				classificationResult.foundVersion,
			)
		}
		return eventLogPreparationResult{}, false, err
	}
	if classificationResult == nil {
		panic("event-log materialization invariant violated: successful classification is missing")
	}
	classification := classificationResult.source
	foundVersion := classificationResult.foundVersion

	switch classification {
	case eventLogSourceMissing, eventLogSourceEmpty:
		if err := installHeaderOnlyCurrentEventLog(
			eventsPath,
			workspace,
			func() {
				// Rename is the migration commit point. This transition must
				// precede workspace cleanup and directory sync.
				s.setEventLogMaterializationState(
					eventLogCurrentReconciliationPending,
					classification,
					foundVersion,
				)
				committed = true
			},
		); err != nil {
			var materializationErr *EventLogMaterializationError
			if errors.As(err, &materializationErr) {
				committed = materializationErr.Committed
			}
			return eventLogPreparationResult{}, committed, err
		}
	case eventLogSourceLegacy:
		s.setEventLogMaterializationState(eventLogUnmaterialized, classification, foundVersion)
		return eventLogPreparationResult{}, false, errors.New(
			"legacy events.jsonl is unsupported; open it with a pre-v1 Kent build before upgrading",
		)
	case eventLogSourceCurrent:
		s.setEventLogMaterializationState(
			eventLogCurrentReconciliationPending,
			classification,
			foundVersion,
		)
	default:
		return eventLogPreparationResult{}, false, fmt.Errorf(
			"unsupported event-log source classification %d",
			classification,
		)
	}

	s.mu.Lock()
	result = s.eventLogPreparationResultLocked()
	s.mu.Unlock()
	return result, committed, nil
}

func acquireEventLogMigrationLock(sessionDir string) (*flock.Flock, string, error) {
	lockPath := filepath.Join(sessionDir, eventLogMigrationLockFile)
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return nil, lockPath, fmt.Errorf(
			"acquire event-log migration lock %s: %w",
			lockPath,
			err,
		)
	}
	return lock, lockPath, nil
}

func (s *Store) setEventLogMaterializationState(
	state eventLogMaterializationState,
	source eventLogSourceClassification,
	foundVersion *int,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventLogMaterialization = &eventLogMaterializationSnapshot{
		state:        state,
		source:       source,
		foundVersion: cloneEventLogSourceVersion(foundVersion),
	}
}

func (s *Store) clearEventLogMaterialization() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventLogMaterialization = nil
}

func cloneEventLogSourceVersion(version *int) *int {
	if version == nil {
		return nil
	}
	cloned := *version
	return &cloned
}

func (s *Store) eventLogPreparationResultLocked() eventLogPreparationResult {
	if s.eventLogMaterialization == nil {
		panic("event-log materialization invariant violated: preparation result has no source classification")
	}
	snapshot := s.eventLogMaterialization
	result := eventLogPreparationResult{
		State:            snapshot.state,
		Source:           snapshot.source,
		SupportedVersion: EventLogVersionV1,
	}
	if snapshot.foundVersion != nil {
		version := *snapshot.foundVersion
		result.FoundVersion = &version
	}
	return result
}

func eventLogMigrationWorkspacePath(sessionDir string) string {
	return filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)
}

// cleanupEventLogMigrationWorkspace only deletes names in the closed owned
// artifact set. An unexpected name is preserved and prevents classification.
func cleanupEventLogMigrationWorkspace(workspace string) error {
	entries, err := readOwnedEventLogMigrationWorkspace(workspace)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case eventLogMigrationStagedLogFile:
			if err := os.Remove(filepath.Join(workspace, entry.Name())); err != nil {
				return fmt.Errorf("remove staged event log: %w", err)
			}
		}
	}
	return nil
}

func ensureOwnedEventLogMigrationWorkspace(workspace string) (resultErr error) {
	if _, err := readOwnedEventLogMigrationWorkspace(workspace); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return fmt.Errorf("create event-log migration workspace %s: %w", workspace, err)
	}
	markerPath := filepath.Join(workspace, eventLogMigrationWorkspaceMarkerFile)
	marker, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create event-log migration workspace marker: %w", err)
	}
	defer func() {
		if marker != nil {
			if closeErr := marker.Close(); closeErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("close event-log migration workspace marker: %w", closeErr),
				)
			}
		}
	}()
	if _, err := writeAll(marker, []byte(eventLogMigrationWorkspaceMarkerContents)); err != nil {
		return fmt.Errorf("write event-log migration workspace marker: %w", err)
	}
	if err := marker.Sync(); err != nil {
		return fmt.Errorf("sync event-log migration workspace marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("close event-log migration workspace marker: %w", err)
	}
	marker = nil
	if err := syncSessionDirectory(workspace); err != nil {
		return err
	}
	return nil
}

func removeOwnedEventLogMigrationWorkspace(workspace string) error {
	if err := cleanupEventLogMigrationWorkspace(workspace); err != nil {
		return err
	}
	markerPath := filepath.Join(workspace, eventLogMigrationWorkspaceMarkerFile)
	if err := os.Remove(markerPath); err != nil {
		return fmt.Errorf("remove event-log migration workspace marker: %w", err)
	}
	if err := os.Remove(workspace); err != nil {
		return fmt.Errorf("remove empty event-log migration workspace: %w", err)
	}
	return nil
}

func readOwnedEventLogMigrationWorkspace(workspace string) ([]os.DirEntry, error) {
	info, err := os.Lstat(workspace)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalidEventLogMigrationWorkspaceError{
			Reason: "workspace is not a directory",
		}
	}
	if err := validateEventLogMigrationWorkspaceMarker(workspace); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return nil, fmt.Errorf("read event-log migration workspace: %w", err)
	}
	for _, entry := range entries {
		if err := validateOwnedEventLogMigrationWorkspaceEntry(entry); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func validateEventLogMigrationWorkspaceMarker(workspace string) error {
	markerPath := filepath.Join(workspace, eventLogMigrationWorkspaceMarkerFile)
	info, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return invalidEventLogMigrationWorkspaceError{
			Reason: "workspace marker is missing",
		}
	}
	if err != nil {
		return fmt.Errorf("stat event-log migration workspace marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return invalidEventLogMigrationWorkspaceError{
			Reason: "workspace marker is not a regular file",
		}
	}
	if info.Size() != int64(len(eventLogMigrationWorkspaceMarkerContents)) {
		return invalidEventLogMigrationWorkspaceError{
			Reason: "workspace marker has unexpected size",
		}
	}
	content, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("read event-log migration workspace marker: %w", err)
	}
	if !bytes.Equal(content, []byte(eventLogMigrationWorkspaceMarkerContents)) {
		return invalidEventLogMigrationWorkspaceError{
			Reason: "workspace marker has unexpected contents",
		}
	}
	return nil
}

func validateOwnedEventLogMigrationWorkspaceEntry(entry os.DirEntry) error {
	switch entry.Name() {
	case eventLogMigrationWorkspaceMarkerFile:
		if entry.Type().IsRegular() {
			return nil
		}
	case eventLogMigrationStagedLogFile:
		if entry.Type().IsRegular() {
			return nil
		}
	default:
		return unknownEventLogMigrationWorkspaceContentError{Name: entry.Name()}
	}
	return invalidEventLogMigrationWorkspaceError{
		Reason: fmt.Sprintf("owned artifact %q has unexpected type %s", entry.Name(), entry.Type()),
	}
}

func installHeaderOnlyCurrentEventLog(
	eventsPath string,
	workspace string,
	onCommitted func(),
) (resultErr error) {
	committed := false
	defer func() {
		if committed {
			resultErr = wrapEventLogMaterializationError(
				EventLogMaterializationStagePreparation,
				true,
				true,
				resultErr,
			)
		}
	}()
	if onCommitted == nil {
		return errors.New("event-log commit transition is required")
	}
	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		return err
	}
	stagePath := filepath.Join(workspace, eventLogMigrationStagedLogFile)
	header, err := encodeEventLogHeaderV1()
	if err != nil {
		return err
	}
	stage, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create staged event log %s: %w", stagePath, err)
	}
	defer func() {
		if stage != nil {
			if closeErr := stage.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close staged event log: %w", closeErr))
			}
		}
	}()
	if _, err := writeAll(stage, append(header, '\n')); err != nil {
		return fmt.Errorf("write staged event log header: %w", err)
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync staged event log header: %w", err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("close staged event log header: %w", err)
	}
	stage = nil
	if err := atomicallyReplaceEventLog(stagePath, eventsPath); err != nil {
		return err
	}
	onCommitted()
	committed = true
	if err := syncSessionDirectory(filepath.Dir(eventsPath)); err != nil {
		return err
	}
	if err := removeOwnedEventLogMigrationWorkspace(workspace); err != nil {
		return err
	}
	return nil
}

func classifyEventLogSource(
	path string,
) (classification *eventLogSourceClassificationResult, resultErr error) {
	fp, err := openRegularSessionFile(path, "session event log")
	if errors.Is(err, os.ErrNotExist) {
		return &eventLogSourceClassificationResult{source: eventLogSourceMissing}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open session event log for classification: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close session event log after classification: %w", closeErr),
			)
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat session event log for classification: %w", err)
	}
	if info.Size() == 0 {
		return &eventLogSourceClassificationResult{source: eventLogSourceEmpty}, nil
	}
	firstLine, complete, err := readEventLogFirstLine(fp)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(firstLine)) == 0 {
		return &eventLogSourceClassificationResult{source: eventLogSourceEmpty}, nil
	}
	classification, err = classifyEventLogHeader(firstLine)
	if !complete {
		switch classification.source {
		case eventLogSourceLegacy:
			if info.Size() > currentEventLogHeaderMaxBytes {
				return classification, nil
			}
			return classification, nil
		case eventLogSourceCurrent:
			return &eventLogSourceClassificationResult{
					source:       eventLogSourceMalformed,
					foundVersion: cloneEventLogSourceVersion(classification.foundVersion),
				}, MalformedEventLogHeaderError{
					Reason: malformedEventLogHeaderUnterminated,
				}
		default:
			return classification, err
		}
	}
	return classification, err
}

func classifyEventLogHeader(
	line []byte,
) (*eventLogSourceClassificationResult, error) {
	fields, structuralErr := decodeEventLogHeaderFields(line)
	if structuralErr != nil {
		if fields.hasContract || fields.hasVersion {
			return &eventLogSourceClassificationResult{source: eventLogSourceMalformed}, MalformedEventLogHeaderError{
				Reason: malformedEventLogHeaderInvalidField,
			}
		}
		return &eventLogSourceClassificationResult{source: eventLogSourceLegacy}, nil
	}
	if !fields.hasContract && !fields.hasVersion {
		return &eventLogSourceClassificationResult{source: eventLogSourceLegacy}, nil
	}
	if !fields.hasContract || !fields.hasVersion {
		return &eventLogSourceClassificationResult{source: eventLogSourceMalformed}, MalformedEventLogHeaderError{
			Reason: malformedEventLogHeaderMissingField,
		}
	}
	var contract string
	if err := json.Unmarshal(fields.contract, &contract); err != nil {
		return &eventLogSourceClassificationResult{source: eventLogSourceMalformed}, MalformedEventLogHeaderError{
			Reason: malformedEventLogHeaderInvalidField,
		}
	}
	var version int
	if err := json.Unmarshal(fields.version, &version); err != nil {
		return &eventLogSourceClassificationResult{source: eventLogSourceMalformed}, MalformedEventLogHeaderError{
			Reason: malformedEventLogHeaderInvalidField,
		}
	}
	if contract != EventLogContract {
		return &eventLogSourceClassificationResult{
				source:       eventLogSourceMalformed,
				foundVersion: &version,
			}, MalformedEventLogHeaderError{
				Reason: malformedEventLogHeaderUnexpectedContract,
			}
	}
	if version > EventLogVersionV1 {
		return &eventLogSourceClassificationResult{
				source:       eventLogSourceNewer,
				foundVersion: &version,
			}, UnsupportedEventLogVersionError{
				FoundVersion:     version,
				SupportedVersion: EventLogVersionV1,
			}
	}
	if version != EventLogVersionV1 {
		return &eventLogSourceClassificationResult{
				source:       eventLogSourceMalformed,
				foundVersion: &version,
			}, MalformedEventLogHeaderError{
				Reason: malformedEventLogHeaderUnsupportedVersion,
			}
	}
	if _, err := decodeEventLogHeader(line); err != nil {
		return &eventLogSourceClassificationResult{
				source:       eventLogSourceMalformed,
				foundVersion: &version,
			}, MalformedEventLogHeaderError{
				Reason: malformedEventLogHeaderInvalidField,
			}
	}
	return &eventLogSourceClassificationResult{
		source:       eventLogSourceCurrent,
		foundVersion: &version,
	}, nil
}

type eventLogHeaderFields struct {
	contract    json.RawMessage
	version     json.RawMessage
	hasContract bool
	hasVersion  bool
}

func decodeEventLogHeaderFields(line []byte) (eventLogHeaderFields, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	start, err := decoder.Token()
	if err != nil {
		return eventLogHeaderFields{}, err
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return eventLogHeaderFields{}, errors.New("event-log header is not an object")
	}
	fields := eventLogHeaderFields{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fields, err
		}
		name, ok := token.(string)
		if !ok {
			return fields, errors.New("event-log header field name is not a string")
		}
		switch name {
		case "contract":
			fields.hasContract = true
		case "version":
			fields.hasVersion = true
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fields, err
		}
		switch name {
		case "contract":
			fields.contract = append(json.RawMessage(nil), value...)
		case "version":
			fields.version = append(json.RawMessage(nil), value...)
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return fields, err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return fields, errors.New("event-log header object is not closed")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fields, errors.New("event-log header has trailing JSON")
		}
		return fields, err
	}
	return fields, nil
}

func readEventLogFirstLine(fp *os.File) ([]byte, bool, error) {
	buffer := make([]byte, currentEventLogHeaderMaxBytes+1)
	read, err := fp.ReadAt(buffer, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("read session event-log header for classification: %w", err)
	}
	buffer = buffer[:read]
	if newline := bytes.IndexByte(buffer, '\n'); newline >= 0 {
		return buffer[:newline], true, nil
	}
	if len(buffer) > currentEventLogHeaderMaxBytes {
		return buffer[:currentEventLogHeaderMaxBytes], false, nil
	}
	return buffer, false, nil
}
