package app

import (
	"testing"
	"time"
)

func TestLifecycleHookIssueMailboxCoalescesOverloadWithoutBlocking(t *testing.T) {
	mailbox := newLifecycleHookIssueMailbox()
	for range 3 {
		mailbox.ReportOverload()
	}

	issue := waitForLifecycleHookIssue(t, mailbox)
	if issue.Kind != lifecycleHookIssueQueueOverload {
		t.Fatalf("issue kind = %v, want queue overload", issue.Kind)
	}
	if issue.DroppedCount == nil || *issue.DroppedCount != 3 {
		t.Fatalf("overload dropped count = %+v, want 3", issue.DroppedCount)
	}
}

func TestLifecycleHookIssueMailboxDrainsBeforeClosedSourceStops(t *testing.T) {
	mailbox := newLifecycleHookIssueMailbox()
	exitCode := 7
	mailbox.Report(lifecycleHookIssue{
		Kind:     lifecycleHookIssueNonzeroExit,
		ExitCode: &exitCode,
	})
	mailbox.Close()

	issue := waitForLifecycleHookIssue(t, mailbox)
	if issue.ExitCode == nil || *issue.ExitCode != exitCode {
		t.Fatalf("drained close issue exit code = %+v, want %d", issue.ExitCode, exitCode)
	}
	select {
	case <-mailbox.Signal():
	case <-time.After(time.Second):
		t.Fatal("closed lifecycle issue mailbox did not signal source completion")
	}
	if _, ok := mailbox.Take(); ok || !mailbox.ClosedAndEmpty() {
		t.Fatal("closed lifecycle issue mailbox did not become empty")
	}
}

func waitForLifecycleHookIssue(
	t *testing.T,
	mailbox *lifecycleHookIssueMailbox,
) lifecycleHookIssue {
	t.Helper()
	select {
	case <-mailbox.Signal():
	case <-time.After(7 * time.Second):
		t.Fatal("timed out waiting for lifecycle hook issue signal")
	}
	issue, ok := mailbox.Take()
	if !ok {
		t.Fatal("lifecycle hook issue signal had no issue")
	}
	return issue
}
