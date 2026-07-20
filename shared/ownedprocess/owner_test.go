//go:build aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package ownedprocess

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
)

func TestTerminateRemovesRootAndDescendantAfterRootExit(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	owner, expected := launchProcessTreeHelper(t, identityPath, helperModeExitRoot)
	identity := waitForProcessTreeIdentity(t, identityPath)
	assertProcessTreeLaunchInputs(t, identity, expected)

	if err := owner.Wait(); err != nil {
		t.Fatalf("wait for root: %v", err)
	}
	if err := owner.Terminate(); err != nil {
		t.Fatalf("terminate process tree: %v", err)
	}
	assertProcessGoneEventually(t, identity.RootPID)
	assertProcessGoneEventually(t, identity.DescendantPID)
	if err := owner.Close(); err != nil {
		t.Fatalf("close process tree: %v", err)
	}
}

func TestCloseRemovesRootAndDescendantAfterRootExit(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	owner, expected := launchProcessTreeHelper(t, identityPath, helperModeExitRootIgnoringTermination)
	identity := waitForProcessTreeIdentity(t, identityPath)
	assertProcessTreeLaunchInputs(t, identity, expected)

	if err := owner.Wait(); err != nil {
		t.Fatalf("wait for root: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close process tree: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("second close process tree: %v", err)
	}

	assertProcessGoneWhenCloseReturns(t, identity.RootPID)
	assertProcessGoneWhenCloseReturns(t, identity.DescendantPID)
}

const (
	helperEnvironmentName                   = "KENT_OWNED_PROCESS_HELPER_MODE"
	helperEnvironmentMarkerName             = "KENT_OWNED_PROCESS_ENV_MARKER"
	helperModeExitRoot                      = "exit_root"
	helperModeExitRootIgnoringTermination   = "exit_root_ignoring_termination"
	helperModeDescendant                    = "descendant"
	helperModeDescendantIgnoringTermination = "descendant_ignoring_termination"
)

type processTreeIdentity struct {
	RootPID          int    `json:"root_pid"`
	DescendantPID    int    `json:"descendant_pid"`
	WorkingDirectory string `json:"working_directory"`
	Environment      string `json:"environment"`
	Stdin            string `json:"stdin"`
}

type processTreeLaunchInputs struct {
	WorkingDirectory string
	Environment      string
	Stdin            string
}

func launchProcessTreeHelper(t *testing.T, identityPath, mode string) (*Owner, processTreeLaunchInputs) {
	t.Helper()
	workingDirectory := t.TempDir()
	expectedWorkingDirectory, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	expected := processTreeLaunchInputs{
		WorkingDirectory: expectedWorkingDirectory,
		Environment:      "marker",
		Stdin:            "caller-supplied stdin",
	}
	owner, err := Launch(LaunchRequest{
		Argv: []string{os.Args[0], "-test.run=TestOwnedProcessHelper"},
		Cwd:  &workingDirectory,
		Env: append(os.Environ(),
			helperEnvironmentName+"="+mode,
			helperEnvironmentMarkerName+"="+expected.Environment,
			"KENT_OWNED_PROCESS_IDENTITY_PATH="+identityPath,
		),
		Stdin:  strings.NewReader(expected.Stdin),
		Stdout: io.Discard,
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("launch helper: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close helper process tree: %v", err)
		}
	})
	return owner, expected
}

func TestOwnedProcessHelper(t *testing.T) {
	mode := os.Getenv(helperEnvironmentName)
	if mode == "" {
		return
	}
	identityPath := os.Getenv("KENT_OWNED_PROCESS_IDENTITY_PATH")
	switch mode {
	case helperModeExitRoot, helperModeExitRootIgnoringTermination:
		descendantMode := helperModeDescendant
		if mode == helperModeExitRootIgnoringTermination {
			descendantMode = helperModeDescendantIgnoringTermination
		}
		child, err := os.StartProcess(os.Args[0], []string{os.Args[0], "-test.run=TestOwnedProcessHelper"}, &os.ProcAttr{
			Env: []string{helperEnvironmentName + "=" + descendantMode},
		})
		if err != nil {
			t.Fatalf("start descendant: %v", err)
		}
		workingDirectory, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory: %v", err)
		}
		stdin, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatalf("read stdin: %v", err)
		}
		writeProcessTreeIdentity(t, identityPath, processTreeIdentity{
			RootPID:          os.Getpid(),
			DescendantPID:    child.Pid,
			WorkingDirectory: workingDirectory,
			Environment:      os.Getenv(helperEnvironmentMarkerName),
			Stdin:            string(stdin),
		})
		return
	case helperModeDescendant:
		select {}
	case helperModeDescendantIgnoringTermination:
		signal.Ignore(syscall.SIGTERM)
		select {}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func assertProcessTreeLaunchInputs(t *testing.T, identity processTreeIdentity, expected processTreeLaunchInputs) {
	t.Helper()
	if identity.WorkingDirectory != expected.WorkingDirectory {
		t.Fatalf("working directory = %q, want %q", identity.WorkingDirectory, expected.WorkingDirectory)
	}
	if identity.Environment != expected.Environment {
		t.Fatalf("environment = %q, want %q", identity.Environment, expected.Environment)
	}
	if identity.Stdin != expected.Stdin {
		t.Fatalf("stdin = %q, want %q", identity.Stdin, expected.Stdin)
	}
}

func writeProcessTreeIdentity(t *testing.T, path string, identity processTreeIdentity) {
	t.Helper()
	body, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, body, 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		t.Fatalf("publish identity: %v", err)
	}
}

func waitForProcessTreeIdentity(t *testing.T, path string) processTreeIdentity {
	t.Helper()
	var identity processTreeIdentity
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		body, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(body, &identity); err != nil {
				t.Fatalf("decode identity: %v", err)
			}
			if identity.RootPID <= 0 || identity.DescendantPID <= 0 {
				t.Fatalf("identity = %+v, want positive process IDs", identity)
			}
			return true
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read identity: %v", err)
		}
		return false
	}, "timed out waiting for process tree identity")
	return identity
}

func assertProcessGoneEventually(t *testing.T, pid int) {
	t.Helper()
	testsetup.RequireProcessGone(t, time.Now().Add(time.Second), pid)
}

func assertProcessGoneWhenCloseReturns(t *testing.T, pid int) {
	t.Helper()
	testsetup.RequireProcessGoneNow(t, pid)
}
