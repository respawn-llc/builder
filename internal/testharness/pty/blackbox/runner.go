package blackbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/driver"

	"github.com/google/uuid"
)

type ClientProfile string

const GoProfile ClientProfile = "go"

type RunRequest struct {
	Scenario     Scenario
	Profile      ClientProfile
	ClientBinary string
	ServerBinary string
}

type RunResult struct {
	Observation RunObservation
	Capture     analyzer.Capture
	RunRoot     string
	ArtifactDir string
	Err         error
	Cleanup     *IncompleteCleanup
}

type IncompleteCleanup struct {
	Owners      []string            `json:"owners"`
	Diagnostics []CleanupDiagnostic `json:"diagnostics,omitempty"`
}

type CleanupDiagnostic struct {
	Owner string `json:"owner"`
	Cause string `json:"cause"`
}

func (e *IncompleteCleanup) Error() string {
	return fmt.Sprintf("incomplete cleanup: owners=%v diagnostics=%v", e.Owners, e.Diagnostics)
}

type Runner struct{}

func (Runner) Run(request RunRequest) (result RunResult) {
	if err := request.Scenario.Validate(); err != nil {
		return RunResult{Err: err}
	}
	if request.Profile != GoProfile {
		return RunResult{Err: fmt.Errorf("requested client profile is unavailable: %s", request.Profile)}
	}
	if request.ClientBinary == "" || request.ServerBinary == "" {
		return RunResult{Err: errors.New("client and server binaries are required")}
	}
	if _, err := os.Stat(request.ClientBinary); err != nil {
		return RunResult{Err: fmt.Errorf("client binary: %w", err)}
	}
	artifacts, err := beginArtifactRun()
	if err != nil {
		return RunResult{Err: fmt.Errorf("begin run artifacts: %w", err)}
	}
	var session *driver.Session
	var environment *IsolatedEnvironment
	supervisor := cleanupSupervisor{artifacts: artifacts, session: &session, environment: &environment}
	defer func() {
		supervisor.finish(&result, request.Scenario.Dimensions)
	}()
	environment, err = NewIsolatedEnvironment(request.ServerBinary, request.Scenario.ModelOperations)
	if environment != nil {
		result.RunRoot = environment.Root
	}
	if err != nil {
		result.Err = err
		return result
	}
	if err := environment.WaitReady(); err != nil {
		result.Err = err
		return result
	}
	if err := environment.BindProject(); err != nil {
		result.Err = err
		return result
	}
	clientEnvironment, err := environment.ClientEnvironment()
	if err != nil {
		result.Err = fmt.Errorf("build client environment: %w", err)
		return result
	}
	session, err = driver.StartSession(driver.SessionSpec{
		Path:       request.ClientBinary,
		Args:       []string{"--force-interactive", "--persistence-root", environment.Root},
		Env:        clientEnvironment,
		Dir:        environment.Workspace,
		Dimensions: analyzer.MustDimensions(request.Scenario.Dimensions.Rows, request.Scenario.Dimensions.Cols),
	})
	if err != nil {
		result.Err = fmt.Errorf("start client PTY session: %w", err)
		return result
	}
	observation, err := runActions(session, environment, request.Scenario.Actions)
	result.Observation = observation
	if err != nil {
		result.Err = err
		return result
	}
	if err := environment.Stub.Verify(); err != nil {
		result.Err = err
		return result
	}
	return result
}

func failureArtifactEvidence(capture analyzer.Capture, analysis *analyzer.Analysis, dimensions Dimensions, environment *IsolatedEnvironment) (artifactEvidence, error) {
	attachments, err := artifactAttachments(environment)
	if err != nil {
		return artifactEvidence{}, err
	}
	if capture.Dimensions.Rows != 0 && capture.Dimensions.Cols != 0 {
		return artifactEvidence{capture: capture, analysis: analysis, attachments: attachments}, nil
	}
	empty, err := analyzer.NewCapture(analyzer.MustDimensions(dimensions.Rows, dimensions.Cols), nil)
	if err != nil {
		return artifactEvidence{}, fmt.Errorf("create empty failure capture: %w", err)
	}
	return artifactEvidence{capture: empty, analysis: analysis, attachments: attachments}, nil
}

func artifactAttachments(environment *IsolatedEnvironment) ([]pty.ArtifactAttachment, error) {
	if environment == nil {
		return nil, nil
	}
	attachments := make([]pty.ArtifactAttachment, 0, 3)
	if environment.Server != nil {
		if environment.Server.stdout != nil {
			attachments = append(attachments, pty.ArtifactAttachment{Name: "server.stdout.log", Data: environment.Server.stdout.Bytes()})
		}
		if environment.Server.stderr != nil {
			attachments = append(attachments, pty.ArtifactAttachment{Name: "server.stderr.log", Data: environment.Server.stderr.Bytes()})
		}
	}
	if environment.Stub != nil {
		snapshot := environment.Stub.Snapshot()
		var failure *string
		if snapshot.Failure != nil {
			message := snapshot.Failure.Error()
			failure = &message
		}
		model, err := json.Marshal(struct {
			RequiredIndex  int            `json:"required_index"`
			RequiredTotal  int            `json:"required_total"`
			ActiveRequests int            `json:"active_requests"`
			Failure        *string        `json:"failure,omitempty"`
			Observed       []ObservedCall `json:"observed"`
		}{
			RequiredIndex: snapshot.RequiredIndex, RequiredTotal: snapshot.RequiredTotal,
			ActiveRequests: snapshot.ActiveRequests, Failure: failure, Observed: snapshot.Observed,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal model diagnostics: %w", err)
		}
		attachments = append(attachments, pty.ArtifactAttachment{Name: "model.json", Data: model})
	}
	return attachments, nil
}

func appendCleanupOwner(cleanup *IncompleteCleanup, owner string) *IncompleteCleanup {
	if cleanup == nil {
		return &IncompleteCleanup{Owners: []string{owner}}
	}
	cleanup.Owners = append(cleanup.Owners, owner)
	return cleanup
}

func appendCleanupFailure(cleanup *IncompleteCleanup, owner string, cause error) *IncompleteCleanup {
	cleanup = appendCleanupOwner(cleanup, owner)
	if cause != nil {
		cleanup.Diagnostics = append(cleanup.Diagnostics, CleanupDiagnostic{Owner: owner, Cause: cause.Error()})
	}
	return cleanup
}

func runActions(session *driver.Session, environment *IsolatedEnvironment, actions []Action) (RunObservation, error) {
	observation := RunObservation{ServerReady: true, Model: environment.Stub.Snapshot()}
	events := session.Events()
	sessionFailures := session.Failure()
	modelEvents := environment.Stub.Events()
	serverFailures := environment.Server.Failure()
	for index, action := range actions {
		deadline := time.NewTimer(fixedWait)
		commandID, commandPending, err := dispatchAction(session, action)
		if err != nil {
			deadline.Stop()
			return observation, fmt.Errorf("action %d (%s): %w", index, action.Kind, err)
		}
		for {
			observation.Model = environment.Stub.Snapshot()
			if err := environment.Server.Error(); err != nil {
				return observation, fmt.Errorf("standalone server failure: %w", err)
			}
			if !commandPending && actionSatisfied(action, observation) {
				deadline.Stop()
				break
			}
			select {
			case event, ok := <-events:
				if !ok {
					observation.ClientExited = true
					if !commandPending && actionSatisfied(action, observation) {
						deadline.Stop()
						break
					}
					return observation, fmt.Errorf("action %d (%s) observed closed session before completion", index, action.Kind)
				}
				if event.Analysis != nil {
					analysis := cloneAnalysis(*event.Analysis)
					observation.Analysis = &analysis
				}
				if event.Kind == driver.SessionEventProcessExit {
					observation.ClientExited = true
				}
				if event.Kind == driver.SessionEventFailure {
					return observation, fmt.Errorf("client PTY failure: %w", event.Err)
				}
				if event.CommandID == commandID {
					switch event.Kind {
					case driver.SessionEventCommandCompleted:
						commandPending = false
					case driver.SessionEventCommandFailed:
						return observation, fmt.Errorf("command %s failed: %w", commandID, event.Err)
					}
				}
			case <-sessionFailures:
				if err := session.Error(); err != nil {
					return observation, fmt.Errorf("client PTY failure: %w", err)
				}
				return observation, errors.New("client PTY reported an unknown failure")
			case <-modelEvents:
				observation.Model = environment.Stub.Snapshot()
			case <-serverFailures:
				if err := environment.Server.Error(); err != nil {
					return observation, fmt.Errorf("standalone server failure: %w", err)
				}
				return observation, errors.New("standalone server reported an unknown evidence failure")
			case <-environment.Server.Done():
				if err := environment.Server.Error(); err != nil {
					return observation, fmt.Errorf("standalone server failure: %w", err)
				}
				return observation, errors.New("standalone server exited during scenario")
			case <-deadline.C:
				return observation, fmt.Errorf("action %d (%s) timed out after %s: private_modes=%v", index, action.Kind, fixedWait, privateModeState(observation.Analysis))
			}
			if !commandPending && actionSatisfied(action, observation) {
				deadline.Stop()
				break
			}
		}
	}
	return observation, nil
}

func privateModeState(analysis *analyzer.Analysis) []analyzer.PrivateModeChange {
	if analysis == nil {
		return nil
	}
	return append([]analyzer.PrivateModeChange(nil), analysis.PrivateModeChanges...)
}

func dispatchAction(session *driver.Session, action Action) (uuid.UUID, bool, error) {
	switch action.Kind {
	case ActionWait, ActionAssert, ActionWaitExit:
		return uuid.Nil, false, nil
	case ActionEnterInput:
		return enqueue(session, driver.SessionCommandWrite, []byte(*action.Input), nil)
	case ActionSubmitPrompt:
		return enqueue(session, driver.SessionCommandWrite, []byte("\r"), nil)
	case ActionCancel:
		return enqueue(session, driver.SessionCommandRuntimeControlByte, []byte{3}, nil)
	case ActionTerminate:
		return enqueue(session, driver.SessionCommandTerminateProcess, nil, nil)
	case ActionResize:
		dimensions := analyzer.MustDimensions(action.Dimensions.Rows, action.Dimensions.Cols)
		return enqueue(session, driver.SessionCommandResize, nil, &dimensions)
	default:
		return uuid.Nil, false, fmt.Errorf("unsupported action kind %s", action.Kind)
	}
}

func enqueue(session *driver.Session, kind driver.SessionCommandKind, bytes []byte, dimensions *analyzer.Dimensions) (uuid.UUID, bool, error) {
	id := uuid.New()
	if err := session.Enqueue(driver.SessionCommand{ID: id, Kind: kind, Bytes: bytes, Dimensions: dimensions}); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

func actionSatisfied(action Action, observation RunObservation) bool {
	switch action.Kind {
	case ActionWait, ActionAssert:
		return action.Predicate.Matches(observation)
	case ActionWaitExit, ActionTerminate:
		return observation.ClientExited
	default:
		return true
	}
}

func cloneAnalysis(analysis analyzer.Analysis) analyzer.Analysis {
	return analysis
}

type cleanupSupervisor struct {
	artifacts   *artifactRun
	session     **driver.Session
	environment **IsolatedEnvironment
}

func (s cleanupSupervisor) finish(result *RunResult, dimensions Dimensions) {
	s.finishUntil(result, dimensions, time.Now().Add(cleanupWait))
}

func (s cleanupSupervisor) finishUntil(result *RunResult, dimensions Dimensions, deadline time.Time) {
	session := *s.session
	environment := *s.environment
	incomplete := s.stopOwners(session, environment, deadline)
	if session != nil {
		capture, err := session.Capture()
		if err != nil {
			incomplete = appendCleanupFailure(incomplete, "client_capture", err)
		} else {
			result.Capture = capture
		}
	}
	if result.Err == nil && incomplete != nil {
		result.Err = incomplete
	}
	if result.Err != nil && s.artifacts != nil {
		evidence, evidenceErr := failureArtifactEvidence(result.Capture, result.Observation.Analysis, dimensions, environment)
		if evidenceErr != nil {
			incomplete = appendCleanupFailure(incomplete, "artifact_evidence", evidenceErr)
		} else {
			publisher := s.artifacts.startFailurePublication(deadline, evidence, result.Err, incomplete)
			select {
			case <-publisher.Done():
				outcome := publisher.Outcome()
				if outcome.dir != "" {
					result.ArtifactDir = outcome.dir
				}
				if outcome.err != nil {
					incomplete = appendCleanupFailure(incomplete, "artifact_publication", outcome.err)
				}
			case <-time.After(time.Until(deadline)):
				incomplete = appendCleanupOwner(incomplete, "artifact_publisher")
			}
		}
	} else if result.Err == nil && s.artifacts != nil {
		if err := s.artifacts.discard(deadline); err != nil {
			incomplete = appendCleanupFailure(incomplete, "artifact_staging", err)
			result.Err = incomplete
		}
	}
	if incomplete == nil && environment != nil && environment.Root != "" {
		if err := removeOwnedRootUntil(environment.Root, deadline); err != nil {
			incomplete = appendCleanupFailure(incomplete, "temporary_root", err)
			if result.Err == nil {
				result.Err = incomplete
			}
		}
	}
	result.Cleanup = incomplete
}

func (cleanupSupervisor) stopOwners(session *driver.Session, environment *IsolatedEnvironment, deadline time.Time) *IncompleteCleanup {
	var incomplete *IncompleteCleanup
	if session != nil {
		if err := session.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			incomplete = appendCleanupFailure(incomplete, "client_pty", err)
		}
		if err := session.Terminate(); err != nil {
			incomplete = appendCleanupFailure(incomplete, "client_terminate", err)
		}
	}
	if environment != nil && environment.Server != nil {
		if err := environment.Server.Terminate(); err != nil {
			incomplete = appendCleanupFailure(incomplete, "server_terminate", err)
		}
	}
	if environment != nil && environment.Stub != nil {
		if err := environment.Stub.Stop(); err != nil {
			incomplete = appendCleanupFailure(incomplete, "model_stub_stop", err)
		}
	}
	graceDeadline := time.Now().Add(time.Until(deadline) / 2)
	waitForOwners(graceDeadline, session, environment)
	if session != nil {
		select {
		case <-session.Done():
		default:
			if err := session.ForceKill(); err != nil {
				incomplete = appendCleanupFailure(incomplete, "client_force_kill", err)
			}
		}
	}
	if environment != nil && environment.Server != nil {
		select {
		case <-environment.Server.Done():
		default:
			if err := environment.Server.ForceKill(); err != nil {
				incomplete = appendCleanupFailure(incomplete, "server_force_kill", err)
			}
		}
	}
	waitForOwners(deadline, session, environment)
	owners := liveOwners(session, environment)
	for _, owner := range owners {
		incomplete = appendCleanupOwner(incomplete, owner)
	}
	return incomplete
}

func removeOwnedRootUntil(root string, deadline time.Time) error {
	return removeTreeUntil(root, deadline)
}

func removeTreeUntil(path string, deadline time.Time) error {
	if time.Now().After(deadline) {
		return errors.New("temporary root cleanup deadline elapsed")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	for {
		entries, readErr := directory.ReadDir(16)
		for _, entry := range entries {
			if time.Now().After(deadline) {
				return closeTreeDirectory(directory, errors.New("temporary root cleanup deadline elapsed"))
			}
			if err := removeTreeUntil(filepath.Join(path, entry.Name()), deadline); err != nil {
				return closeTreeDirectory(directory, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return closeTreeDirectory(directory, readErr)
		}
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}

func closeTreeDirectory(directory *os.File, cause error) error {
	if closeErr := directory.Close(); closeErr != nil {
		return fmt.Errorf("%w; close directory: %v", cause, closeErr)
	}
	return cause
}

func waitForOwners(deadline time.Time, session *driver.Session, environment *IsolatedEnvironment) {
	for {
		if len(liveOwners(session, environment)) == 0 {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		timer := time.NewTimer(remaining)
		var clientDone <-chan struct{}
		var serverDone <-chan struct{}
		var stubDone <-chan struct{}
		if session != nil {
			clientDone = session.Done()
		}
		if environment != nil && environment.Server != nil {
			serverDone = environment.Server.Done()
		}
		if environment != nil && environment.Stub != nil {
			stubDone = environment.Stub.Done()
		}
		select {
		case <-clientDone:
		case <-serverDone:
		case <-stubDone:
		case <-timer.C:
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func liveOwners(session *driver.Session, environment *IsolatedEnvironment) []string {
	owners := make([]string, 0, 3)
	if session != nil {
		select {
		case <-session.Done():
		default:
			owners = append(owners, "client")
		}
	}
	if environment != nil && environment.Server != nil {
		select {
		case <-environment.Server.Done():
		default:
			owners = append(owners, "server")
		}
	}
	if environment != nil && environment.Stub != nil {
		select {
		case <-environment.Stub.Done():
		default:
			owners = append(owners, "model_stub")
		}
	}
	return owners
}
