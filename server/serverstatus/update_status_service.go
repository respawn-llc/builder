package serverstatus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/shared/invariant"
	"core/shared/serverapi"
)

const (
	updateStatusFreshness = time.Hour
	updateStatusTimeout   = 2 * time.Second
)

var ErrUpdateStatusServiceClosed = errors.New("update status service is closed")

type Dependencies struct {
	ReleaseSource ReleaseMetadataSource
}

type UpdateStatusService struct {
	currentVersion string
	releaseSource  ReleaseMetadataSource
	policy         invariant.Policy
	lifecycle      context.Context
	cancel         context.CancelFunc

	mu        sync.Mutex
	workers   sync.WaitGroup
	completed *completedUpdateStatus
	inflight  *updateStatusOperation
	closed    bool
}

type completedUpdateStatus struct {
	result      serverapi.UpdateStatusResult
	completedAt time.Time
}

type updateStatusOperation struct {
	done          chan struct{}
	terminal      updateStatusTerminal
	waiters       int
	latestVersion string
}

type updateStatusTerminal struct {
	result serverapi.UpdateStatusResult
	err    error
	set    bool
}

func NewUpdateStatusService(currentVersion string, dependencies Dependencies) *UpdateStatusService {
	releaseSource := dependencies.ReleaseSource
	if releaseSource == nil {
		releaseSource = NewGitHubReleaseMetadataSource(nil, "")
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &UpdateStatusService{
		currentVersion: currentVersion,
		releaseSource:  releaseSource,
		policy:         invariant.NewPolicy(),
		lifecycle:      lifecycle,
		cancel:         cancel,
	}
}

func (s *UpdateStatusService) Status(ctx context.Context) (serverapi.UpdateStatusResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	observedAt := time.Now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return serverapi.UpdateStatusResult{}, ErrUpdateStatusServiceClosed
	}
	if s.completed != nil && isFreshUpdateStatusCache(observedAt, s.completed.completedAt) {
		result := s.completed.result
		s.mu.Unlock()
		return result, nil
	}
	if s.completed != nil {
		s.completed = nil
	}
	if s.inflight == nil {
		operation := &updateStatusOperation{done: make(chan struct{})}
		s.inflight = operation
		s.workers.Add(1)
		go s.runUpdateStatusCheck(operation)
	}
	operation := s.inflight
	operation.waiters++
	s.mu.Unlock()

	select {
	case <-operation.done:
		return s.completedOperationResult(operation)
	case <-ctx.Done():
		s.mu.Lock()
		operation.waiters--
		s.mu.Unlock()
		return serverapi.UpdateStatusResult{}, ctx.Err()
	}
}

func (s *UpdateStatusService) completedOperationResult(operation *updateStatusOperation) (serverapi.UpdateStatusResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation.waiters--
	if !operation.terminal.set {
		return s.handleIncompleteTerminalLocked(operation)
	}
	if operation.terminal.err != nil {
		return serverapi.UpdateStatusResult{}, operation.terminal.err
	}
	if err := operation.terminal.result.Validate(); err != nil {
		return s.handleInvalidTerminalResultLocked(operation, err)
	}
	return operation.terminal.result, nil
}

func (s *UpdateStatusService) runUpdateStatusCheck(operation *updateStatusOperation) {
	defer s.workers.Done()
	ctx, cancel := context.WithTimeout(s.lifecycle, updateStatusTimeout)
	defer cancel()

	result := s.checkUpdateStatus(ctx, operation)
	s.publishUpdateStatusOperation(operation, result)
}

func (s *UpdateStatusService) checkUpdateStatus(
	ctx context.Context,
	operation *updateStatusOperation,
) serverapi.UpdateStatusResult {
	currentVersion, err := parseConfiguredUpdateVersion(s.currentVersion)
	if err != nil {
		return serverapi.FailedUpdateStatusResult(fmt.Sprintf("current release version is invalid: %v", err))
	}

	metadata, err := s.releaseSource.LatestRelease(ctx)
	if err != nil {
		return classifyReleaseSourceFailure(err)
	}
	operation.latestVersion = metadata.Version
	latestVersion, err := parseUpdateVersion(metadata.Version)
	if err != nil {
		return serverapi.FailedUpdateStatusResult(fmt.Sprintf("latest release version is invalid: %v", err))
	}

	current := currentVersion.String()
	latest := latestVersion.String()
	if latestVersion.Compare(currentVersion) > 0 {
		return serverapi.AvailableUpdateStatusResult(current, latest)
	}
	return serverapi.CurrentUpdateStatusResult(current, latest)
}

func classifyReleaseSourceFailure(err error) serverapi.UpdateStatusResult {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return serverapi.CheckUnavailableUpdateStatusResult()
	}
	var httpStatusError *ReleaseHTTPStatusError
	var metadataError *ReleaseMetadataError
	if errors.As(err, &httpStatusError) || errors.As(err, &metadataError) {
		return serverapi.FailedUpdateStatusResult(err.Error())
	}
	var transportError *ReleaseTransportError
	if errors.As(err, &transportError) {
		return serverapi.CheckUnavailableUpdateStatusResult()
	}
	return serverapi.FailedUpdateStatusResult(err.Error())
}

func (s *UpdateStatusService) publishUpdateStatusOperation(
	operation *updateStatusOperation,
	result serverapi.UpdateStatusResult,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if operation.terminal.set {
		if s.closed {
			s.completeOperationLocked(operation, updateStatusTerminal{
				err: ErrUpdateStatusServiceClosed,
				set: true,
			}, false)
			return
		}
		result := s.invariantFailureResultLocked(
			operation,
			"publish_update_status",
			"operation terminal was already published",
		)
		s.completeOperationLocked(operation, updateStatusTerminal{result: result, set: true}, true)
		return
	}
	if s.closed {
		s.completeOperationLocked(operation, updateStatusTerminal{
			err: ErrUpdateStatusServiceClosed,
			set: true,
		}, false)
	} else if err := result.Validate(); err != nil {
		result = s.handleInvalidResultBeforePublishLocked(operation, err)
		s.completeOperationLocked(operation, updateStatusTerminal{result: result, set: true}, true)
	} else {
		s.completeOperationLocked(operation, updateStatusTerminal{result: result, set: true}, true)
	}
}

func (s *UpdateStatusService) handleIncompleteTerminalLocked(
	operation *updateStatusOperation,
) (serverapi.UpdateStatusResult, error) {
	result := s.invariantFailureResultLocked(
		operation,
		"await_update_status",
		"operation completed without a terminal result or error",
	)
	if s.closed {
		return serverapi.UpdateStatusResult{}, ErrUpdateStatusServiceClosed
	}
	s.completeOperationLocked(operation, updateStatusTerminal{result: result, set: true}, true)
	return result, nil
}

func (s *UpdateStatusService) handleInvalidTerminalResultLocked(
	operation *updateStatusOperation,
	cause error,
) (serverapi.UpdateStatusResult, error) {
	result := s.invariantFailureResultLocked(
		operation,
		"read_update_status_terminal",
		fmt.Sprintf("operation terminal result is invalid: %v", cause),
	)
	if s.closed {
		return serverapi.UpdateStatusResult{}, ErrUpdateStatusServiceClosed
	}
	s.completeOperationLocked(operation, updateStatusTerminal{result: result, set: true}, true)
	return result, nil
}

func (s *UpdateStatusService) handleInvalidResultBeforePublishLocked(
	operation *updateStatusOperation,
	cause error,
) serverapi.UpdateStatusResult {
	return s.invariantFailureResultLocked(
		operation,
		"publish_update_status",
		fmt.Sprintf("calculated result is invalid: %v", cause),
	)
}

func (s *UpdateStatusService) completeOperationLocked(
	operation *updateStatusOperation,
	terminal updateStatusTerminal,
	cacheResult bool,
) {
	operation.terminal = terminal
	if cacheResult {
		s.completed = &completedUpdateStatus{result: terminal.result, completedAt: time.Now()}
	} else {
		s.completed = nil
	}
	if s.inflight == operation {
		s.inflight = nil
	}
	select {
	case <-operation.done:
	default:
		close(operation.done)
	}
}

func (s *UpdateStatusService) invariantFailureResultLocked(
	operation *updateStatusOperation,
	operationName string,
	cause string,
) serverapi.UpdateStatusResult {
	s.policy.Check(false, invariant.UpdateStatusDiagnostic(invariant.UpdateStatusDiagnosticInput{
		Operation:      operationName,
		CurrentVersion: strings.TrimSpace(s.currentVersion),
		LatestVersion:  operation.latestVersion,
		CacheState:     updateStatusCacheState(s.completed),
		InflightState:  updateStatusInflightState(s.inflight, operation),
		Cause:          cause,
	}))
	return serverapi.FailedUpdateStatusResult("internal update checker failure: " + cause)
}

func updateStatusCacheState(completed *completedUpdateStatus) string {
	if completed == nil {
		return "absent"
	}
	return "completed"
}

func updateStatusInflightState(current *updateStatusOperation, observed *updateStatusOperation) string {
	switch {
	case current == nil:
		return "absent"
	case current != observed:
		return "different_operation"
	case observed.terminal.set:
		return "terminal_published"
	default:
		return "terminal_missing"
	}
}

func (s *UpdateStatusService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.completed = nil
		s.cancel()
	}
	s.mu.Unlock()
	s.workers.Wait()

	s.mu.Lock()
	if s.inflight != nil && s.inflight.terminal.set {
		s.inflight = nil
	}
	s.mu.Unlock()
	return nil
}

func isFreshUpdateStatusCache(observedAt time.Time, completedAt time.Time) bool {
	return observedAt.Before(completedAt.Add(updateStatusFreshness))
}

type updateVersion struct {
	components [3]uint64
}

func parseConfiguredUpdateVersion(raw string) (updateVersion, error) {
	if strings.TrimSpace(raw) == "dev" {
		return updateVersion{}, nil
	}
	return parseUpdateVersion(raw)
}

func parseUpdateVersion(raw string) (updateVersion, error) {
	normalized := strings.TrimSpace(raw)
	normalized = strings.TrimPrefix(normalized, "v")
	parts := strings.Split(normalized, ".")
	if len(parts) != 3 {
		return updateVersion{}, errors.New("version must contain exactly three numeric components")
	}
	var version updateVersion
	for index, part := range parts {
		if part == "" {
			return updateVersion{}, fmt.Errorf("component %d is empty", index)
		}
		var value uint64
		for _, character := range part {
			if character < '0' || character > '9' {
				return updateVersion{}, fmt.Errorf("component %d contains a non-numeric character", index)
			}
			digit := uint64(character - '0')
			if value > (math.MaxUint64-digit)/10 {
				return updateVersion{}, fmt.Errorf("component %d exceeds the supported unsigned range", index)
			}
			value = value*10 + digit
		}
		version.components[index] = value
	}
	return version, nil
}

func (v updateVersion) Compare(other updateVersion) int {
	for index := range v.components {
		switch {
		case v.components[index] > other.components[index]:
			return 1
		case v.components[index] < other.components[index]:
			return -1
		}
	}
	return 0
}

func (v updateVersion) String() string {
	return strconv.FormatUint(v.components[0], 10) + "." +
		strconv.FormatUint(v.components[1], 10) + "." +
		strconv.FormatUint(v.components[2], 10)
}
