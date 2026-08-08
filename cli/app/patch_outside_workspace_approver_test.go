package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/runtimewire"
	askquestion "core/server/tools"
	patchtool "core/server/tools/patch"
)

const (
	outsideWorkspaceAllowOnceSuggestion    = runtimewire.OutsideWorkspaceAllowOnceSuggestion
	outsideWorkspaceAllowSessionSuggestion = runtimewire.OutsideWorkspaceAllowSessionSuggestion
	outsideWorkspaceDenySuggestion         = runtimewire.OutsideWorkspaceDenySuggestion
)

func testOutsideWorkspaceApprovalResolution(
	decision askquestion.AskQuestionApprovalDecision,
	commentary string,
) askquestion.AskQuestionApproval {
	resolution := askquestion.AskQuestionApproval{Decision: decision}
	if commentary != "" {
		resolution.Commentary = &commentary
	}
	return resolution
}

func TestOutsideWorkspaceApprovalFromResolution(t *testing.T) {
	tests := []struct {
		name       string
		resolution askquestion.AskQuestionResolution
		want       patchtool.OutsideWorkspaceApproval
	}{
		{name: "allow once", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, ""), want: patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowOnce}},
		{name: "allow session", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowSession, ""), want: patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowSession}},
		{name: "deny", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionDeny, ""), want: patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}},
		{name: "allow once with commentary", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, "approved, but keep it small"), want: patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowOnce, Commentary: "approved, but keep it small"}},
		{name: "deny with commentary", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionDeny, "no because this is protected"), want: patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny, Commentary: "no because this is protected"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runtimewire.OutsideWorkspaceApprovalFromResolution(tc.resolution)
			if err != nil {
				t.Fatalf("parse approval response: %v", err)
			}
			if got != tc.want {
				t.Fatalf("decision mismatch: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestOutsideWorkspaceApprovalFromResolutionRejectsMissingOrInvalidPayload(t *testing.T) {
	tests := []struct {
		name       string
		resolution askquestion.AskQuestionResolution
	}{
		{name: "missing payload"},
		{name: "invalid decision", resolution: askquestion.AskQuestionApproval{Decision: "maybe"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := runtimewire.OutsideWorkspaceApprovalFromResolution(tc.resolution); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPatchOutsideWorkspaceApproverCachesSessionDecision(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	askCalls := 0
	broker.SetAskHandler(func(_ context.Context, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		askCalls++
		if !req.Approval {
			t.Fatalf("expected approval=true for outside-workspace ask")
		}
		if len(req.Suggestions) != 0 {
			t.Fatalf("expected structured approval options instead of suggestions, got %+v", req.Suggestions)
		}
		if len(req.ApprovalOptions) != 3 {
			t.Fatalf("expected 3 approval options, got %+v", req.ApprovalOptions)
		}
		if req.ApprovalOptions[0].Label != "Allow once" || req.ApprovalOptions[1].Label != "Allow for this session" || req.ApprovalOptions[2].Label != "Deny" {
			t.Fatalf("expected fixed built-in approval labels, got %+v", req.ApprovalOptions)
		}
		if strings.Contains(req.Question, "workspace:") || strings.Contains(req.Question, "requested path:") || strings.Contains(req.Question, "Patch requested an edit outside the workspace.") {
			t.Fatalf("approval prompt contains removed fields: %q", req.Question)
		}
		if !strings.Contains(req.Question, "Allow editing /tmp/x.txt (outside workspace dir)?") {
			t.Fatalf("unexpected approval question text: %q", req.Question)
		}
		return testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowSession, ""), nil
	})

	approver := runtimewire.NewOutsideWorkspaceApprover(broker, "editing")
	req := patchtool.OutsideWorkspaceRequest{RequestedPath: "../x.txt", ResolvedPath: "/tmp/x.txt", WorkingDirectory: "/tmp/w"}

	first, err := approver.Approve(context.Background(), req)
	if err != nil {
		t.Fatalf("approve first call: %v", err)
	}
	if first.Decision != patchtool.OutsideWorkspaceDecisionAllowSession {
		t.Fatalf("unexpected first decision: %v", first)
	}
	second, err := approver.Approve(context.Background(), req)
	if err != nil {
		t.Fatalf("approve second call: %v", err)
	}
	if second.Decision != patchtool.OutsideWorkspaceDecisionAllowSession {
		t.Fatalf("unexpected second decision: %v", second)
	}
	if askCalls != 1 {
		t.Fatalf("expected one ask call, got %d", askCalls)
	}
}

func TestPatchOutsideWorkspaceApproverPropagatesAskError(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		return nil, errors.New("ask failed")
	})

	approver := runtimewire.NewOutsideWorkspaceApprover(broker, "editing")
	_, err := approver.Approve(context.Background(), patchtool.OutsideWorkspaceRequest{RequestedPath: "../x.txt", ResolvedPath: "/tmp/x.txt", WorkingDirectory: "/tmp/w"})
	if err == nil {
		t.Fatal("expected ask error")
	}
}

func TestOutsideWorkspaceApproverUsesReadPromptText(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	askCalls := 0
	broker.SetAskHandler(func(_ context.Context, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		askCalls++
		if !strings.Contains(req.Question, "Allow reading /tmp/x.pdf (outside workspace dir)?") {
			t.Fatalf("unexpected read approval question text: %q", req.Question)
		}
		return testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, ""), nil
	})

	approver := runtimewire.NewOutsideWorkspaceApprover(broker, "reading")
	approval, err := approver.Approve(context.Background(), patchtool.OutsideWorkspaceRequest{RequestedPath: "../x.pdf", ResolvedPath: "/tmp/x.pdf", WorkingDirectory: "/tmp/w"})
	if err != nil {
		t.Fatalf("approve read call: %v", err)
	}
	if approval.Decision != patchtool.OutsideWorkspaceDecisionAllowOnce {
		t.Fatalf("unexpected approval decision: %v", approval)
	}
	if askCalls != 1 {
		t.Fatalf("expected one ask call, got %d", askCalls)
	}
}

func TestOutsideWorkspaceApproverQueuedApprovalBlocksUntilSubmitted(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	approver := runtimewire.NewOutsideWorkspaceApprover(broker, "editing")
	req := patchtool.OutsideWorkspaceRequest{RequestedPath: "../x.txt", ResolvedPath: "/tmp/x.txt", WorkingDirectory: "/tmp/w"}
	type out struct {
		approval patchtool.OutsideWorkspaceApproval
		err      error
	}
	done := make(chan out, 1)

	go func() {
		approval, err := approver.Approve(context.Background(), req)
		done <- out{approval: approval, err: err}
	}()

	pending := waitForPendingApprovals(t, broker, 1)
	if len(pending) != 1 {
		t.Fatalf("expected one pending approval, got %+v", pending)
	}
	if !pending[0].Approval {
		t.Fatalf("expected queued request to be approval-backed, got %+v", pending[0])
	}
	if len(pending[0].Suggestions) != 0 {
		t.Fatalf("expected no suggestion list for approval request, got %+v", pending[0].Suggestions)
	}
	if len(pending[0].ApprovalOptions) != 3 {
		t.Fatalf("expected three approval options, got %+v", pending[0].ApprovalOptions)
	}
	if pending[0].ApprovalOptions[0].Decision != askquestion.AskQuestionApprovalDecisionAllowOnce || pending[0].ApprovalOptions[1].Decision != askquestion.AskQuestionApprovalDecisionAllowSession || pending[0].ApprovalOptions[2].Decision != askquestion.AskQuestionApprovalDecisionDeny {
		t.Fatalf("unexpected approval options: %+v", pending[0].ApprovalOptions)
	}
	if !strings.Contains(pending[0].Question, "Allow editing /tmp/x.txt (outside workspace dir)?") {
		t.Fatalf("unexpected queued approval question: %q", pending[0].Question)
	}

	select {
	case result := <-done:
		t.Fatalf("approval returned before submission: %+v", result)
	default:
	}

	if err := broker.Submit(pending[0].ID, testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionDeny, "no")); err != nil {
		t.Fatalf("submit denial: %v", err)
	}
	if err := broker.Submit(pending[0].ID, testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, "")); err == nil {
		t.Fatal("expected duplicate approval resolution to fail")
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("approve: %v", result.err)
		}
		if result.approval.Decision != patchtool.OutsideWorkspaceDecisionDeny {
			t.Fatalf("unexpected approval decision: %+v", result.approval)
		}
		if result.approval.Commentary != "no" {
			t.Fatalf("unexpected approval commentary: %+v", result.approval)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued approval result")
	}

	if pending := broker.Pending(); len(pending) != 0 {
		t.Fatalf("expected pending approvals cleared after completion, got %+v", pending)
	}
}

func TestOutsideWorkspaceApproverQueuedAllowSessionCachesWithoutSecondPrompt(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	approver := runtimewire.NewOutsideWorkspaceApprover(broker, "editing")
	req := patchtool.OutsideWorkspaceRequest{RequestedPath: "../x.txt", ResolvedPath: "/tmp/x.txt", WorkingDirectory: "/tmp/w"}
	type out struct {
		approval patchtool.OutsideWorkspaceApproval
		err      error
	}
	done := make(chan out, 1)

	go func() {
		approval, err := approver.Approve(context.Background(), req)
		done <- out{approval: approval, err: err}
	}()

	pending := waitForPendingApprovals(t, broker, 1)
	if err := broker.Submit(pending[0].ID, testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowSession, "")); err != nil {
		t.Fatalf("submit allow-session approval: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("approve: %v", result.err)
		}
		if result.approval.Decision != patchtool.OutsideWorkspaceDecisionAllowSession {
			t.Fatalf("unexpected first approval decision: %+v", result.approval)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for allow-session approval")
	}

	secondDone := make(chan out, 1)
	go func() {
		approval, err := approver.Approve(context.Background(), req)
		secondDone <- out{approval: approval, err: err}
	}()

	select {
	case result := <-secondDone:
		if result.err != nil {
			t.Fatalf("second approve: %v", result.err)
		}
		if result.approval.Decision != patchtool.OutsideWorkspaceDecisionAllowSession {
			t.Fatalf("unexpected cached approval decision: %+v", result.approval)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected cached allow-session approval to return immediately")
	}

	if pending := broker.Pending(); len(pending) != 0 {
		t.Fatalf("expected no second queued approval after allow-session cache, got %+v", pending)
	}
}

func waitForPendingApprovals(t *testing.T, broker *askquestion.AskQuestionBroker, want int) []askquestion.AskQuestionRequest {
	t.Helper()
	var pending []askquestion.AskQuestionRequest
	if testsetup.Until(time.Now().Add(2*time.Second), 5*time.Millisecond, func() bool {
		pending = broker.Pending()
		return len(pending) == want
	}) {
		return pending
	}
	return broker.Pending()
}
