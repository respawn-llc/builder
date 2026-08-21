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

	serverpb "core/shared/protoapi/gen/kent/api/server"

	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	updateStatusFreshness = time.Hour
	updateStatusTimeout   = 2 * time.Second
)

var ErrUpdateStatusServiceClosed = errors.New("update status service is closed")

type UpdateStatusService struct {
	currentVersion string
	debug          bool
	releaseSource  releaseMetadataSource
	now            func() time.Time
	lifecycle      context.Context
	cancel         context.CancelFunc

	mu        sync.Mutex
	workers   sync.WaitGroup
	completed *completedUpdateStatus
	inflight  *updateStatusOperation
	closed    bool
}

type completedUpdateStatus struct {
	result      updateStatusResult
	completedAt time.Time
}

type updateStatusOperation struct {
	done   chan struct{}
	result updateStatusResult
	err    error
}

type updateStatusKind uint8

const (
	updateStatusCurrent updateStatusKind = iota + 1
	updateStatusAvailable
	updateStatusCheckUnavailable
	updateStatusCheckFailed
)

type updateStatusResult struct {
	kind    updateStatusKind
	current string
	latest  string
	cause   string
}

func currentUpdateStatusResult(current, latest string) updateStatusResult {
	return updateStatusResult{kind: updateStatusCurrent, current: current, latest: latest}
}

func availableUpdateStatusResult(current, latest string) updateStatusResult {
	return updateStatusResult{kind: updateStatusAvailable, current: current, latest: latest}
}

func checkUnavailableUpdateStatusResult() updateStatusResult {
	return updateStatusResult{kind: updateStatusCheckUnavailable}
}

func failedUpdateStatusResult(cause string) updateStatusResult {
	return updateStatusResult{kind: updateStatusCheckFailed, cause: strings.TrimSpace(cause)}
}

func (r updateStatusResult) validate() error {
	switch r.kind {
	case updateStatusCurrent, updateStatusAvailable:
		if strings.TrimSpace(r.current) == "" || strings.TrimSpace(r.latest) == "" {
			return errors.New("update versions are required")
		}
	case updateStatusCheckUnavailable:
		if r.current != "" || r.latest != "" || r.cause != "" {
			return errors.New("unavailable update status cannot carry details")
		}
	case updateStatusCheckFailed:
		if strings.TrimSpace(r.cause) == "" {
			return errors.New("failed update status requires a cause")
		}
	default:
		return errors.New("update status kind is invalid")
	}
	return nil
}

func (r updateStatusResult) proto() *serverpb.UpdateStatus {
	status := &serverpb.UpdateStatus{}
	switch r.kind {
	case updateStatusCurrent:
		status.Status = &serverpb.UpdateStatus_Current{Current: &serverpb.UpdateVersions{CurrentVersion: r.current, LatestVersion: r.latest}}
	case updateStatusAvailable:
		status.Status = &serverpb.UpdateStatus_Available{Available: &serverpb.UpdateVersions{CurrentVersion: r.current, LatestVersion: r.latest}}
	case updateStatusCheckUnavailable:
		status.Status = &serverpb.UpdateStatus_CheckUnavailable{CheckUnavailable: &emptypb.Empty{}}
	case updateStatusCheckFailed:
		status.Status = &serverpb.UpdateStatus_CheckFailed{CheckFailed: &serverpb.UpdateCheckFailed{Cause: r.cause}}
	}
	return status
}

func NewUpdateStatusService(currentVersion string, debug bool) *UpdateStatusService {
	return newUpdateStatusService(currentVersion, debug, nil, time.Now)
}

func newUpdateStatusService(currentVersion string, debug bool, releaseSource releaseMetadataSource, now func() time.Time) *UpdateStatusService {
	if releaseSource == nil {
		releaseSource = newDefaultGitHubReleaseMetadataSource()
	}
	if now == nil {
		now = time.Now
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &UpdateStatusService{
		currentVersion: currentVersion,
		debug:          debug,
		releaseSource:  releaseSource,
		now:            now,
		lifecycle:      lifecycle,
		cancel:         cancel,
	}
}

func (s *UpdateStatusService) status(ctx context.Context) (updateStatusResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return updateStatusResult{}, ErrUpdateStatusServiceClosed
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
		return updateStatusResult{}, ctx.Err()
	}
}

func (s *UpdateStatusService) runUpdateStatusCheck(operation *updateStatusOperation) {
	defer s.workers.Done()

	ctx, cancel := context.WithTimeout(s.lifecycle, updateStatusTimeout)
	defer cancel()

	result := s.checkUpdateStatus(ctx)
	if err := result.validate(); err != nil {
		result = s.invalidCalculatedResult(result, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.inflight != operation {
		return
	}
	s.completeOperationLocked(operation, result, nil, true)
}

func (s *UpdateStatusService) invalidCalculatedResult(result updateStatusResult, cause error) updateStatusResult {
	diagnostic := fmt.Sprintf(
		"update status invariant violated: operation=publish calculated_kind=%q configured_version=%q cause=%v",
		result.kind,
		strings.TrimSpace(s.currentVersion),
		cause,
	)
	if s.debug {
		panic(diagnostic)
	}
	return failedUpdateStatusResult("internal update checker failure: " + cause.Error())
}

func (s *UpdateStatusService) checkUpdateStatus(ctx context.Context) updateStatusResult {
	currentVersion, err := parseConfiguredUpdateVersion(s.currentVersion)

	if err != nil {
		return failedUpdateStatusResult(fmt.Sprintf("current release version is invalid: %v", err))
	}

	metadata, err := s.releaseSource.LatestRelease(ctx)
	if err != nil {
		return classifyReleaseSourceFailure(err)
	}
	latestVersion, err := parseUpdateVersion(metadata.Version)
	if err != nil {
		return failedUpdateStatusResult(fmt.Sprintf("latest release version is invalid: %v", err))
	}

	current := currentVersion.String()
	latest := latestVersion.String()
	if latestVersion.Compare(currentVersion) > 0 {
		return availableUpdateStatusResult(current, latest)
	}
	return currentUpdateStatusResult(current, latest)
}

func classifyReleaseSourceFailure(err error) updateStatusResult {
	var httpStatusError *releaseHTTPStatusError
	var metadataError *releaseMetadataError
	if errors.As(err, &httpStatusError) || errors.As(err, &metadataError) {
		return failedUpdateStatusResult(err.Error())
	}
	var transportError *releaseTransportError
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.As(err, &transportError) ||
		errors.As(err, &networkError) {
		return checkUnavailableUpdateStatusResult()
	}
	return failedUpdateStatusResult(err.Error())
}

func (s *UpdateStatusService) completeOperationLocked(operation *updateStatusOperation, result updateStatusResult, err error, cache bool) {
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
			s.completeOperationLocked(s.inflight, updateStatusResult{}, ErrUpdateStatusServiceClosed, false)
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
