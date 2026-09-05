package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/runtimewire"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/textutil"
)

const (
	outsideWorkspaceAllowOnceSuggestion    = runtimewire.OutsideWorkspaceAllowOnceSuggestion
	outsideWorkspaceAllowSessionSuggestion = runtimewire.OutsideWorkspaceAllowSessionSuggestion
	outsideWorkspaceDenySuggestion         = runtimewire.OutsideWorkspaceDenySuggestion
)

func testOutsideWorkspaceApprovalResolution(
	decision askquestion.AskQuestionApprovalDecision,
	commentary *string,
) askquestion.AskQuestionApproval {
	return askquestion.AskQuestionApproval{
		Decision:   decision,
		Commentary: commentary,
	}
}

func testFileAccessApprovalRequest() askquestion.FileAccessApprovalRequest {
	return askquestion.FileAccessApprovalRequest{
		WorkingDirectory: "/tmp/w",
		Targets: []askquestion.FileAccessTarget{{
			RequestedPath: "../x.txt",
			ResolvedPath:  "/tmp/x.txt",
		}},
	}
}

func testFileAccessApprovalContext(toolCallID string) context.Context {
	ctx := askquestion.WithExecutionIdentity(context.Background(), askquestion.ExecutionIdentity{
		RunID:      "11111111-1111-4111-8111-111111111111",
		StepID:     "22222222-2222-4222-8222-222222222222",
		ToolCallID: clientui.ToolCallID(toolCallID),
	})
	return askquestion.WithApprovalLifecycle(ctx, askquestion.NewApprovalLifecycle())
}

func TestOutsideWorkspaceApprovalFromResolution(t *testing.T) {
	approvedCommentary := "approved, but keep it small"
	deniedCommentary := "no because this is protected"
	tests := []struct {
		name       string
		resolution askquestion.AskQuestionResolution
		want       askquestion.FileAccessApproval
	}{
		{name: "allow once", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, nil), want: askquestion.FileAccessApproval{Kind: askquestion.FileAccessApprovalAllowOnce}},
		{name: "allow session", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowSession, nil), want: askquestion.FileAccessApproval{Kind: askquestion.FileAccessApprovalAllowSession}},
		{name: "deny", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionDeny, nil), want: askquestion.FileAccessApproval{Kind: askquestion.FileAccessApprovalDeny}},
		{name: "allow once with commentary", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, &approvedCommentary), want: askquestion.FileAccessApproval{Kind: askquestion.FileAccessApprovalAllowOnce, Commentary: &approvedCommentary}},
		{name: "deny with commentary", resolution: testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionDeny, &deniedCommentary), want: askquestion.FileAccessApproval{Kind: askquestion.FileAccessApprovalDeny, Commentary: &deniedCommentary}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runtimewire.OutsideWorkspaceApprovalFromResolution(tc.resolution)
			if err != nil {
				t.Fatalf("parse approval response: %v", err)
			}
			if got.Kind != tc.want.Kind ||
				!textutil.EqualOptional(got.Commentary, tc.want.Commentary) {
				t.Fatalf("decision mismatch: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestOutsideWorkspaceApprovalFromResolutionRejectsMissingOrInvalidPayload(t *testing.T) {
	blankCommentary := ""
	tests := []struct {
		name       string
		resolution askquestion.AskQuestionResolution
	}{
		{name: "missing payload"},
		{name: "invalid decision", resolution: askquestion.AskQuestionApproval{Decision: "maybe"}},
		{name: "blank commentary", resolution: askquestion.AskQuestionApproval{
			Decision:   askquestion.AskQuestionApprovalDecisionAllowOnce,
			Commentary: &blankCommentary,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := runtimewire.OutsideWorkspaceApprovalFromResolution(tc.resolution); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestOutsideWorkspaceApproverCachesSessionDecision(t *testing.T) {
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
		return testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowSession, nil), nil
	})

	approver := runtimewire.NewOutsideWorkspaceApprover(broker)
	req := testFileAccessApprovalRequest()

	first, err := approver.Approve(testFileAccessApprovalContext("cache-session"), req)
	if err != nil {
		t.Fatalf("approve first call: %v", err)
	}
	if first.Kind != askquestion.FileAccessApprovalAllowSession {
		t.Fatalf("unexpected first decision: %v", first)
	}
	second, err := approver.Approve(context.Background(), req)
	if err != nil {
		t.Fatalf("approve second call: %v", err)
	}
	if second.Kind != askquestion.FileAccessApprovalSessionCached {
		t.Fatalf("unexpected second decision: %v", second)
	}
	if askCalls != 1 {
		t.Fatalf("expected one ask call, got %d", askCalls)
	}
}

func TestOutsideWorkspaceApproverPropagatesAskError(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, askquestion.AskQuestionRequest) (askquestion.AskQuestionResolution, error) {
		return nil, errors.New("ask failed")
	})

	approver := runtimewire.NewOutsideWorkspaceApprover(broker)
	_, err := approver.Approve(
		testFileAccessApprovalContext("propagate-error"),
		testFileAccessApprovalRequest(),
	)
	if err == nil {
		t.Fatal("expected ask error")
	}
}

func TestOutsideWorkspaceApproverQueuedApprovalBlocksUntilSubmitted(t *testing.T) {
	broker := askquestion.NewAskQuestionBroker()
	approver := runtimewire.NewOutsideWorkspaceApprover(broker)
	req := testFileAccessApprovalRequest()
	type out struct {
		approval askquestion.FileAccessApproval
		err      error
	}
	done := make(chan out, 1)

	go func() {
		approval, err := approver.Approve(testFileAccessApprovalContext("queued-deny"), req)
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
	select {
	case result := <-done:
		t.Fatalf("approval returned before submission: %+v", result)
	default:
	}

	if err := broker.Submit(pending[0].ToolCallID, testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionDeny, textutil.Value("no"))); err != nil {
		t.Fatalf("submit denial: %v", err)
	}
	if err := broker.Submit(pending[0].ToolCallID, testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowOnce, nil)); err == nil {
		t.Fatal("expected duplicate approval resolution to fail")
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("approve: %v", result.err)
		}
		if result.approval.Kind != askquestion.FileAccessApprovalDeny {
			t.Fatalf("unexpected approval decision: %+v", result.approval)
		}
		if result.approval.Commentary == nil || *result.approval.Commentary != "no" {
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
	approver := runtimewire.NewOutsideWorkspaceApprover(broker)
	req := testFileAccessApprovalRequest()
	type out struct {
		approval askquestion.FileAccessApproval
		err      error
	}
	done := make(chan out, 1)

	go func() {
		approval, err := approver.Approve(testFileAccessApprovalContext("queued-session"), req)
		done <- out{approval: approval, err: err}
	}()

	pending := waitForPendingApprovals(t, broker, 1)
	if err := broker.Submit(pending[0].ToolCallID, testOutsideWorkspaceApprovalResolution(askquestion.AskQuestionApprovalDecisionAllowSession, nil)); err != nil {
		t.Fatalf("submit allow-session approval: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("approve: %v", result.err)
		}
		if result.approval.Kind != askquestion.FileAccessApprovalAllowSession {
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
		if result.approval.Kind != askquestion.FileAccessApprovalSessionCached {
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
