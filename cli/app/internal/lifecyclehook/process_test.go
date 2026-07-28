package lifecyclehook

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"core/shared/lifecyclecontract"
)

const (
	invokeHookProcessModeEnv    = "KENT_TEST_LIFECYCLE_HOOK_PROCESS_MODE"
	invokeHookProcessStartedEnv = "KENT_TEST_LIFECYCLE_HOOK_PROCESS_STARTED"
	invokeHookProcessReleaseEnv = "KENT_TEST_LIFECYCLE_HOOK_PROCESS_RELEASE"
	invokeHookProcessModeBlock  = "block"
	invokeHookTestDeadline      = time.Second
)

func TestInvokeHookReportsTypedTimeoutAfterTerminatingLiveProcess(t *testing.T) {
	startedPath := t.TempDir() + "/started"
	releasePath := t.TempDir() + "/release"
	t.Setenv(invokeHookProcessModeEnv, invokeHookProcessModeBlock)
	t.Setenv(invokeHookProcessStartedEnv, startedPath)
	t.Setenv(invokeHookProcessReleaseEnv, releasePath)
	t.Cleanup(func() {
		if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
			t.Errorf("release blocking lifecycle hook: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), invokeHookTestDeadline)
	defer cancel()

	issues := make(chan *Issue, 1)
	go func() {
		issues <- invokeHook(ctx, []string{
			os.Args[0],
			"-test.run=^TestInvokeHookBlockingProcess$",
		}, lifecyclecontract.Event{
			Category: lifecyclecontract.CategorySessionStart,
		})
	}()

	var issue *Issue
	select {
	case issue = <-issues:
	case <-time.After(2 * invokeHookTestDeadline):
		t.Fatal("live lifecycle hook did not stop at its deadline")
	}
	if issue == nil {
		t.Fatal("timed-out lifecycle hook produced no issue")
	}
	if _, err := os.Stat(startedPath); err != nil {
		t.Fatalf("blocking lifecycle hook did not start: %v", err)
	}
	process, ok := issue.Detail.(ProcessIssue)
	if !ok {
		t.Fatalf("issue detail = %T, want ProcessIssue", issue.Detail)
	}
	var timeoutError *timeoutError
	if !errors.As(process.Cause, &timeoutError) {
		t.Fatalf("process cause = %T, want timeoutError", process.Cause)
	}
	if timeoutError.Limit != timeout {
		t.Fatalf("timeout limit = %s, want %s", timeoutError.Limit, timeout)
	}
}

func TestInvokeHookBlockingProcess(t *testing.T) {
	if os.Getenv(invokeHookProcessModeEnv) != invokeHookProcessModeBlock {
		return
	}
	startedPath := os.Getenv(invokeHookProcessStartedEnv)
	releasePath := os.Getenv(invokeHookProcessReleaseEnv)
	if err := os.WriteFile(startedPath, []byte("started"), 0o600); err != nil {
		t.Fatalf("record blocking lifecycle hook start: %v", err)
	}
	for {
		if _, err := os.Stat(releasePath); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("check blocking lifecycle hook release: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}
