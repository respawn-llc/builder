package lifecyclehook

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/shared/lifecyclecontract"
)

func TestInvokeHookReportsTypedTimeoutForExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()

	issue := invokeHook(ctx, []string{filepath.Join(t.TempDir(), "hook")}, lifecyclecontract.Event{
		Category: lifecyclecontract.CategorySessionStart,
	})
	if issue == nil {
		t.Fatal("expired hook deadline produced no issue")
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
