package blackbox

import (
	"errors"
	"fmt"
	"os"
	"time"

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
	Owners []string
}

func (e *IncompleteCleanup) Error() string {
	return fmt.Sprintf("incomplete cleanup: owners=%v", e.Owners)
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
	environment, err := NewIsolatedEnvironment(request.ServerBinary, request.Scenario.ModelOperations)
	if err != nil {
		return RunResult{Err: err}
	}
	result.RunRoot = environment.Root
	var session *driver.Session
	defer func() {
		cleanupDeadline := time.Now().Add(fixedWait)
		result.Cleanup = cleanup(session, environment, result.Err == nil, cleanupDeadline)
		if result.Err == nil && result.Cleanup != nil {
			result.Err = result.Cleanup
		}
		if session != nil {
			capture, captureErr := session.Capture()
			result.Capture = capture
			if result.Err == nil && captureErr != nil {
				result.Err = captureErr
			}
		}
		if result.Err != nil && environment != nil && session != nil && result.Capture.ReadLoopDone {
			artifactDir, artifactErr := publishFailureArtifacts(cleanupDeadline, environment.Root, result.Capture, result.Observation.Analysis, result.Err, result.Cleanup)
			if artifactErr != nil {
				result.Cleanup = appendCleanupOwner(result.Cleanup, "artifact_publication")
			} else {
				result.ArtifactDir = artifactDir
			}
		}
	}()
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

func appendCleanupOwner(cleanup *IncompleteCleanup, owner string) *IncompleteCleanup {
	if cleanup == nil {
		return &IncompleteCleanup{Owners: []string{owner}}
	}
	cleanup.Owners = append(cleanup.Owners, owner)
	return cleanup
}

func runActions(session *driver.Session, environment *IsolatedEnvironment, actions []Action) (RunObservation, error) {
	observation := RunObservation{ServerReady: true, Model: environment.Stub.Snapshot()}
	events := session.Events()
	modelEvents := environment.Stub.Events()
	for index, action := range actions {
		deadline := time.NewTimer(fixedWait)
		commandID, commandPending, err := dispatchAction(session, action)
		if err != nil {
			deadline.Stop()
			return observation, fmt.Errorf("action %d (%s): %w", index, action.Kind, err)
		}
		for {
			observation.Model = environment.Stub.Snapshot()
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
			case <-modelEvents:
				observation.Model = environment.Stub.Snapshot()
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

func cleanup(session *driver.Session, environment *IsolatedEnvironment, success bool, deadline time.Time) *IncompleteCleanup {
	if session != nil {
		_ = session.Close()
		_ = session.Terminate()
	}
	if environment != nil && environment.Stub != nil {
		environment.Stub.Close()
	}
	if environment != nil && environment.Server != nil {
		environment.Server.Terminate()
	}
	graceDeadline := deadline.Add(-fixedWait / 2)
	waitForOwners(graceDeadline, session, environment)
	if session != nil {
		select {
		case <-session.Done():
		default:
			_ = session.ForceKill()
		}
	}
	if environment != nil && environment.Server != nil {
		select {
		case <-environment.Server.Done():
		default:
			environment.Server.ForceKill()
		}
	}
	waitForOwners(deadline, session, environment)
	owners := liveOwners(session, environment)
	if success && environment != nil && environment.Root != "" && len(owners) == 0 {
		_ = os.RemoveAll(environment.Root)
	}
	if len(owners) == 0 {
		return nil
	}
	return &IncompleteCleanup{Owners: owners}
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
