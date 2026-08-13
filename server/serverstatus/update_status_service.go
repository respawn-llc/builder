package serverstatus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/shared/serverapi"
)

const (
	updateStatusFreshness = time.Hour
	updateStatusTimeout   = 2 * time.Second
)

var ErrUpdateStatusServiceClosed = errors.New("update status service is closed")

type UpdateStatusService struct {
	currentVersion    updateVersion
	currentVersionErr error
	releaseSource     releaseMetadataSource
	now               func() time.Time
	lifecycle         context.Context
	cancel            context.CancelFunc

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
	done   chan struct{}
	result serverapi.UpdateStatusResult
	err    error
}

func NewUpdateStatusService(currentVersion string, debug bool) *UpdateStatusService {
	return newUpdateStatusService(currentVersion, debug, nil, time.Now)
}

func newUpdateStatusService(currentVersion string, _ bool, releaseSource releaseMetadataSource, now func() time.Time) *UpdateStatusService {
	if releaseSource == nil {
		releaseSource = newDefaultGitHubReleaseMetadataSource()
	}
	if now == nil {
		now = time.Now
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	parsedVersion, versionErr := parseConfiguredUpdateVersion(currentVersion)
	return &UpdateStatusService{
		currentVersion:    parsedVersion,
		currentVersionErr: versionErr,
		releaseSource:     releaseSource,
		now:               now,
		lifecycle:         lifecycle,
		cancel:            cancel,
	}
}

func (s *UpdateStatusService) Status(ctx context.Context) (serverapi.UpdateStatusResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return serverapi.UpdateStatusResult{}, ErrUpdateStatusServiceClosed
	}
	if s.completed != nil && isFreshUpdateStatusCache(s.now(), s.completed.completedAt) {
		result := s.completed.result
		s.mu.Unlock()
		return result, nil
	}
	s.completed = nil
	if s.inflight == nil {
		s.inflight = &updateStatusOperation{done: make(chan struct{})}
		s.workers.Add(1)
		go s.runUpdateStatusCheck(s.inflight)
	}
	operation := s.inflight
	s.mu.Unlock()

	select {
	case <-operation.done:
		return operation.result, operation.err
	case <-ctx.Done():
		return serverapi.UpdateStatusResult{}, ctx.Err()
	}
}

func (s *UpdateStatusService) runUpdateStatusCheck(operation *updateStatusOperation) {
	defer s.workers.Done()

	ctx, cancel := context.WithTimeout(s.lifecycle, updateStatusTimeout)
	defer cancel()

	result := s.checkUpdateStatus(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.inflight != operation {
		return
	}
	s.completeOperationLocked(operation, result, nil, true)
}

func (s *UpdateStatusService) checkUpdateStatus(ctx context.Context) serverapi.UpdateStatusResult {
	if s.currentVersionErr != nil {
		return serverapi.FailedUpdateStatusResult(fmt.Sprintf("current release version is invalid: %v", s.currentVersionErr))
	}

	metadata, err := s.releaseSource.LatestRelease(ctx)
	if err != nil {
		return classifyReleaseSourceFailure(err)
	}
	current := s.currentVersion.String()
	latest := metadata.Version.String()
	if metadata.Version.Compare(s.currentVersion) > 0 {
		return serverapi.AvailableUpdateStatusResult(current, latest)
	}
	return serverapi.CurrentUpdateStatusResult(current, latest)
}

func classifyReleaseSourceFailure(err error) serverapi.UpdateStatusResult {
	var httpStatusError *releaseHTTPStatusError
	var metadataError *releaseMetadataError
	if errors.As(err, &httpStatusError) || errors.As(err, &metadataError) {
		return serverapi.FailedUpdateStatusResult(err.Error())
	}
	var transportError *releaseTransportError
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.As(err, &transportError) ||
		errors.As(err, &networkError) {
		return serverapi.CheckUnavailableUpdateStatusResult()
	}
	return serverapi.FailedUpdateStatusResult(err.Error())
}

func (s *UpdateStatusService) completeOperationLocked(operation *updateStatusOperation, result serverapi.UpdateStatusResult, err error, cache bool) {
	operation.result = result
	operation.err = err
	if cache {
		s.completed = &completedUpdateStatus{result: result, completedAt: s.now()}
	} else {
		s.completed = nil
	}
	s.inflight = nil
	close(operation.done)
}

func (s *UpdateStatusService) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		if s.inflight != nil {
			s.completeOperationLocked(s.inflight, serverapi.UpdateStatusResult{}, ErrUpdateStatusServiceClosed, false)
		} else {
			s.completed = nil
		}
	}
	s.mu.Unlock()
	s.workers.Wait()
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
	normalized := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts := strings.Split(normalized, ".")
	if len(parts) != 3 {
		return updateVersion{}, errors.New("version must contain exactly three numeric components")
	}
	var version updateVersion
	for index, part := range parts {
		if part == "" {
			return updateVersion{}, fmt.Errorf("component %d is empty", index)
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return updateVersion{}, fmt.Errorf("component %d is invalid: %w", index, err)
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
