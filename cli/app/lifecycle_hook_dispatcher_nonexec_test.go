//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"testing"

	"core/shared/invariant"
	"core/shared/lifecyclecontract"
)

func TestLifecycleHookDispatcherNonExecutableDisablesAfterOneIssue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "non-executable-hook")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatalf("write non-executable lifecycle hook: %v", err)
	}
	dispatcher, err := newLifecycleHookDispatcher(
		[]string{path},
		lifecyclecontract.NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))),
	)
	if err != nil {
		t.Fatalf("construct non-executable lifecycle hook dispatcher: %v", err)
	}
	t.Cleanup(func() {
		if err := dispatcher.Close(); err != nil {
			t.Errorf("close non-executable lifecycle hook dispatcher: %v", err)
		}
	})

	if accepted := dispatcher.EnqueueLifecycleEnvelope(dispatcherTestEnvelope(t, 1)); !accepted {
		t.Fatal("first non-executable lifecycle event was not accepted for launch")
	}
	issue := waitForLifecycleHookIssue(t, dispatcher.Issues())
	if issue.Kind != lifecycleHookIssueLaunchDisabled {
		t.Fatalf("non-executable issue kind = %v, want launch disabled", issue.Kind)
	}
	if issue.LaunchFailure == nil || *issue.LaunchFailure != lifecycleHookLaunchNonExecutable {
		t.Fatalf("non-executable launch failure = %+v, want non-executable", issue.LaunchFailure)
	}
	if accepted := dispatcher.EnqueueLifecycleEnvelope(dispatcherTestEnvelope(t, 2)); accepted {
		t.Fatal("non-executable lifecycle dispatcher accepted a later event")
	}
}
