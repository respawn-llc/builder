//go:build windows

package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"

	"golang.org/x/sys/windows"
)

func assertScriptProcessGone(t *testing.T, _ int) {
	t.Helper()
	t.Fatal("POSIX process assertion used by a Windows workflow script test")
}

func assertScriptProcessAlive(t *testing.T, _ int) {
	t.Helper()
	t.Fatal("POSIX process assertion used by a Windows workflow script test")
}

const (
	windowsWorkflowScriptHelperEnvironment = "KENT_WORKFLOW_SCRIPT_PROCESS_HELPER"
	windowsWorkflowScriptIdentityPath      = "KENT_WORKFLOW_SCRIPT_PROCESS_IDENTITY_PATH"
	windowsWorkflowScriptDescendantMode    = "descendant"
)

func TestWindowsWorkflowScriptExecutionCancellationUsesOwnedTree(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close session runtime authority: %v", err)
		}
	})
	handle, err := authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Command: sessionruntime.ScriptCommand{
			Path: os.Args[0],
			Args: []string{"-test.run=TestWindowsWorkflowScriptProcessHelper"},
			Env: append(os.Environ(),
				windowsWorkflowScriptHelperEnvironment+"=hold",
				windowsWorkflowScriptIdentityPath+"="+identityPath,
			),
		},
	})
	if err != nil {
		t.Fatalf("start Windows workflow script execution: %v", err)
	}

	identity := waitForWindowsWorkflowScriptIdentity(t, identityPath)
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handle.Stop(stopCtx); err != nil {
		t.Fatalf("stop Windows workflow script execution: %v", err)
	}
	result, err := handle.Wait(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Windows workflow script execution error = %v, want cancellation", err)
	}
	if result.Script == nil || !result.Script.Canceled {
		t.Fatalf("Windows workflow script execution result = %#v, want canceled script", result.Script)
	}
	assertWindowsWorkflowScriptProcessGone(t, identity.RootPID)
	assertWindowsWorkflowScriptProcessGone(t, identity.DescendantPID)
}

func TestWindowsWorkflowScriptProcessHelper(t *testing.T) {
	mode := os.Getenv(windowsWorkflowScriptHelperEnvironment)
	if mode == "" {
		return
	}
	if mode == windowsWorkflowScriptDescendantMode {
		select {}
	}
	child, err := os.StartProcess(os.Args[0], []string{os.Args[0], "-test.run=TestWindowsWorkflowScriptProcessHelper"}, &os.ProcAttr{
		Env: []string{windowsWorkflowScriptHelperEnvironment + "=" + windowsWorkflowScriptDescendantMode},
	})
	if err != nil {
		t.Fatalf("start Windows workflow script descendant: %v", err)
	}
	identity, err := json.Marshal(windowsWorkflowScriptProcessIdentity{RootPID: os.Getpid(), DescendantPID: child.Pid})
	if err != nil {
		t.Fatalf("marshal Windows workflow script identity: %v", err)
	}
	identityPath := os.Getenv(windowsWorkflowScriptIdentityPath)
	temporaryPath := identityPath + ".tmp"
	if err := os.WriteFile(temporaryPath, identity, 0o600); err != nil {
		t.Fatalf("write Windows workflow script identity temp: %v", err)
	}
	if err := os.Rename(temporaryPath, identityPath); err != nil {
		t.Fatalf("publish Windows workflow script identity: %v", err)
	}
	select {}
}

type windowsWorkflowScriptProcessIdentity struct {
	RootPID       int `json:"root_pid"`
	DescendantPID int `json:"descendant_pid"`
}

func waitForWindowsWorkflowScriptIdentity(t *testing.T, path string) windowsWorkflowScriptProcessIdentity {
	t.Helper()
	var identity windowsWorkflowScriptProcessIdentity
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		body, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(body, &identity); err != nil {
				t.Fatalf("decode Windows workflow script identity: %v", err)
			}
			return identity.RootPID > 0 && identity.DescendantPID > 0
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read Windows workflow script identity: %v", err)
		}
		return false
	}, "timed out waiting for Windows workflow script identity")
	return identity
}

func assertWindowsWorkflowScriptProcessGone(t *testing.T, pid int) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return true
		}
		if err != nil {
			t.Fatalf("open Windows workflow script process %d: %v", pid, err)
		}
		if err := windows.CloseHandle(handle); err != nil {
			t.Fatalf("close Windows workflow script process handle: %v", err)
		}
		return false
	}, "Windows workflow script process %d remained observable", pid)
}
