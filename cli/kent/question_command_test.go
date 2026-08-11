package main

import (
	"bytes"
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"

	"github.com/google/uuid"
)

type questionWorkflowStub struct {
	apicontract.WorkflowService
	responses []serverapi.WorkflowTaskAttentionListResponse
	requests  []serverapi.WorkflowTaskAttentionListRequest
}

func (s *questionWorkflowStub) ListWorkflowTaskAttention(
	_ context.Context,
	req serverapi.WorkflowTaskAttentionListRequest,
) (serverapi.WorkflowTaskAttentionListResponse, error) {
	s.requests = append(s.requests, req)
	if len(s.responses) == 0 {
		return serverapi.WorkflowTaskAttentionListResponse{}, nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

type questionApprovalStub struct {
	apicontract.ApprovalViewService
	apicontract.PromptControlService
	responses []serverapi.ApprovalListPendingBySessionResponse
	answers   []serverapi.ApprovalAnswerRequest
}

func (s *questionApprovalStub) ListPendingApprovalsBySession(
	context.Context,
	serverapi.ApprovalListPendingBySessionRequest,
) (serverapi.ApprovalListPendingBySessionResponse, error) {
	if len(s.responses) == 0 {
		return serverapi.ApprovalListPendingBySessionResponse{}, nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *questionApprovalStub) AnswerApproval(_ context.Context, req serverapi.ApprovalAnswerRequest) error {
	s.answers = append(s.answers, req)
	return nil
}

type questionSessionStub struct {
	apicontract.AskViewService
	apicontract.ApprovalViewService
	apicontract.PromptControlService
	askResponses      []serverapi.AskListPendingBySessionResponse
	approvalResponses []serverapi.ApprovalListPendingBySessionResponse
	askAnswers        []serverapi.AskAnswerRequest
}

func (s *questionSessionStub) ListPendingAsksBySession(
	context.Context,
	serverapi.AskListPendingBySessionRequest,
) (serverapi.AskListPendingBySessionResponse, error) {
	if len(s.askResponses) == 0 {
		return serverapi.AskListPendingBySessionResponse{}, nil
	}
	response := s.askResponses[0]
	s.askResponses = s.askResponses[1:]
	return response, nil
}

func (s *questionSessionStub) ListPendingApprovalsBySession(
	context.Context,
	serverapi.ApprovalListPendingBySessionRequest,
) (serverapi.ApprovalListPendingBySessionResponse, error) {
	if len(s.approvalResponses) == 0 {
		return serverapi.ApprovalListPendingBySessionResponse{}, nil
	}
	response := s.approvalResponses[0]
	s.approvalResponses = s.approvalResponses[1:]
	return response, nil
}

func (s *questionSessionStub) AnswerAsk(_ context.Context, request serverapi.AskAnswerRequest) error {
	s.askAnswers = append(s.askAnswers, request)
	return nil
}

func (*questionSessionStub) AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error {
	return nil
}

func TestQuestionAnswerInputAndSelectorValidation(t *testing.T) {
	sessionID := uuid.NewString()
	for _, args := range [][]string{
		{"answer", "--session", sessionID},
		{"answer", "--session", sessionID, "--option", "1", "--commentary", " \t "},
		{"answer", "--session", sessionID, "--option", "0"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runQuestionCommand(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}

	task := "KENT-1"
	for _, selectors := range []struct {
		session *string
		task    *string
	}{
		{},
		{session: &sessionID, task: &task},
	} {
		if _, err := resolveQuestionCommandSelector(selectors.session, selectors.task, ".", config.Command+" question"); err == nil {
			t.Fatalf("selectors=%+v accepted", selectors)
		}
	}

	t.Setenv(sessionenv.SessionIDEnv, sessionID)
	if _, err := resolveQuestionCommandSelector(&sessionID, nil, ".", config.Command+" question"); err == nil {
		t.Fatal("current Session accepted as a question target")
	}
}

func TestQuestionCandidateSelectionRejectsAmbiguityAndSelfTarget(t *testing.T) {
	first := mustQuestionSessionID(t, uuid.NewString())
	second := mustQuestionSessionID(t, uuid.NewString())
	taskRef := "KENT-1"
	selector := questionCommandSelector{TaskRef: &taskRef, Command: config.Command + " question"}
	var stderr bytes.Buffer
	candidate, code := selectTaskQuestionCandidate(selector, []taskQuestionSessionCandidate{
		{SessionID: first, Questions: []questionCommandPendingQuestion{{AskID: "ask-1"}}},
		{SessionID: second, Questions: []questionCommandPendingQuestion{{AskID: "ask-2"}}},
	}, &stderr)
	if candidate != nil || code != 1 || stderr.Len() == 0 {
		t.Fatalf("ambiguous selection candidate=%+v code=%d stderr=%q", candidate, code, stderr.String())
	}

	t.Setenv(sessionenv.SessionIDEnv, first.String())
	stderr.Reset()
	candidate, code = selectTaskQuestionCandidate(selector, []taskQuestionSessionCandidate{{
		SessionID: first, Questions: []questionCommandPendingQuestion{{AskID: "ask-1"}},
	}}, &stderr)
	if candidate != nil || code != 2 || stderr.Len() == 0 {
		t.Fatalf("self selection candidate=%+v code=%d stderr=%q", candidate, code, stderr.String())
	}
}

func TestTaskQuestionCandidatesSelectOldestAndOmitStaleApproval(t *testing.T) {
	sessionID := uuid.NewString()
	newer := ordinaryQuestionAttention(sessionID, "ask-new", "Newer?", 20)
	older := ordinaryQuestionAttention(sessionID, "ask-old", "Older?", 10)
	recommended := 2
	newer.Suggestions = []string{"A", "B"}
	newer.RecommendedOptionIndex = &recommended

	approvals := &questionApprovalStub{}
	candidates, err := taskQuestionCandidatesWithRemote(t.Context(), approvals, []serverapi.WorkflowAttentionItem{
		newer,
		older,
		approvalQuestionAttention(sessionID, "stale-approval", 5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || len(candidates[0].Questions) != 2 {
		t.Fatalf("candidates=%+v", candidates)
	}
	if candidates[0].Questions[0].AskID != "ask-old" ||
		candidates[0].Questions[0].RecommendedOptionIndex != nil ||
		candidates[0].Questions[1].RecommendedOptionIndex == nil ||
		*candidates[0].Questions[1].RecommendedOptionIndex != 2 {
		t.Fatalf("ordered questions=%+v", candidates[0].Questions)
	}
}

func TestApprovalProjectionMapsOptionAndCommentary(t *testing.T) {
	sessionID := mustQuestionSessionID(t, uuid.NewString())
	commentary := "  scoped access  "
	option := 2
	approval := &clientui.PendingApproval{
		ApprovalID: "approval-1",
		SessionID:  sessionID.String(),
		Question:   "Allow?",
		Options: []clientui.ApprovalOption{
			{Decision: clientui.ApprovalDecisionDeny, Label: "Deny"},
			{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"},
		},
	}
	remote := &questionApprovalStub{}
	if err := answerApprovalQuestion(remote, sessionID, approval, &option, &commentary); err != nil {
		t.Fatal(err)
	}
	if len(remote.answers) != 1 {
		t.Fatalf("answers=%+v", remote.answers)
	}
	answer := remote.answers[0]
	if answer.SessionID != sessionID.String() || answer.ApprovalID != "approval-1" ||
		answer.Decision != clientui.ApprovalDecisionAllowOnce ||
		answer.Commentary == nil || *answer.Commentary != "scoped access" {
		t.Fatalf("answer=%+v", answer)
	}
	if err := answerApprovalQuestion(remote, sessionID, approval, nil, nil); !isQuestionAnswerUsageError(err) {
		t.Fatalf("missing option error=%T %v", err, err)
	}
	outOfRange := 3
	if err := answerApprovalQuestion(remote, sessionID, approval, &outOfRange, nil); !isQuestionAnswerUsageError(err) {
		t.Fatalf("range error=%T %v", err, err)
	}
}

func TestSessionQuestionAnswerSubmissionAndFollowUp(t *testing.T) {
	sessionID := mustQuestionSessionID(t, uuid.NewString())
	option := 2
	commentary := "because"
	stub := &questionSessionStub{
		askResponses: []serverapi.AskListPendingBySessionResponse{
			{Asks: []clientui.PendingAsk{{
				SessionID: sessionID.String(),
				AskID:     "ask-1",
				Question:  "First?",
				Suggestions: []string{
					"one",
					"two",
				},
			}}},
			{Asks: []clientui.PendingAsk{{
				SessionID: sessionID.String(),
				AskID:     "ask-2",
				Question:  "Second?",
			}}},
		},
		approvalResponses: []serverapi.ApprovalListPendingBySessionResponse{{}, {}},
	}
	var stdout, stderr bytes.Buffer
	if code := answerSessionQuestionWithServices(
		stub,
		stub,
		stub,
		sessionID,
		&option,
		&commentary,
		&stdout,
		&stderr,
	); code != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(stub.askAnswers) != 1 {
		t.Fatalf("answers=%+v", stub.askAnswers)
	}
	answer := stub.askAnswers[0]
	if answer.SessionID != sessionID.String() ||
		answer.AskID != "ask-1" ||
		answer.SelectedOptionNumber == nil ||
		*answer.SelectedOptionNumber != option ||
		answer.FreeformAnswer != commentary {
		t.Fatalf("answer=%+v", answer)
	}

	stub = &questionSessionStub{
		askResponses:      []serverapi.AskListPendingBySessionResponse{{}},
		approvalResponses: []serverapi.ApprovalListPendingBySessionResponse{{}},
	}
	stdout.Reset()
	stderr.Reset()
	if code := answerSessionQuestionWithServices(
		stub,
		stub,
		stub,
		sessionID,
		&option,
		nil,
		&stdout,
		&stderr,
	); code != 1 || stdout.Len() != 0 || stderr.Len() == 0 || len(stub.askAnswers) != 0 {
		t.Fatalf("no-pending exit=%d stdout=%q stderr=%q answers=%+v", code, stdout.String(), stderr.String(), stub.askAnswers)
	}
}

func TestTaskQuestionFollowUpUsesSameSessionSuccessor(t *testing.T) {
	sessionID := mustQuestionSessionID(t, uuid.NewString())
	otherSessionID := mustQuestionSessionID(t, uuid.NewString())
	candidates := []taskQuestionSessionCandidate{
		{
			SessionID: otherSessionID,
			Questions: []questionCommandPendingQuestion{{AskID: "other"}},
		},
		{
			SessionID: sessionID,
			Questions: []questionCommandPendingQuestion{{AskID: "next"}},
		},
	}
	followUp := taskQuestionFollowUp(candidates, sessionID)
	if followUp == nil || followUp.AskID != "next" {
		t.Fatalf("follow-up=%+v", followUp)
	}
	if followUp := taskQuestionFollowUp(candidates, mustQuestionSessionID(t, uuid.NewString())); followUp != nil {
		t.Fatalf("unexpected follow-up=%+v", followUp)
	}

	workflows := &questionWorkflowStub{responses: []serverapi.WorkflowTaskAttentionListResponse{{
		Items: []serverapi.WorkflowAttentionItem{
			ordinaryQuestionAttention(otherSessionID.String(), "other", "Other?", 1),
			ordinaryQuestionAttention(sessionID.String(), "next", "Next?", 2),
		},
	}}}
	var stdout, stderr bytes.Buffer
	if code := writeTaskQuestionFollowUp(workflows, &questionApprovalStub{}, "task-1", sessionID, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() == 0 || stderr.Len() != 0 || len(workflows.requests) != 1 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	workflows.responses = []serverapi.WorkflowTaskAttentionListResponse{{}}
	stdout.Reset()
	if code := writeTaskQuestionFollowUp(workflows, &questionApprovalStub{}, "task-1", sessionID, &stdout, &stderr); code != 0 ||
		stdout.Len() == 0 {
		t.Fatalf("done exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestQuestionDispatchAndHelpRemainAvailable(t *testing.T) {
	for _, args := range [][]string{
		{"question", "--help"},
		{"questions", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		if code := rootCommand(args, bytes.NewReader(nil), &stdout, &stderr); code != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func ordinaryQuestionAttention(sessionID string, askID string, question string, occurredAt int64) serverapi.WorkflowAttentionItem {
	name := "Implementer"
	return serverapi.WorkflowAttentionItem{
		ID: askID, Kind: string(serverapi.WorkflowTaskAttentionKindQuestion),
		TaskID: "task-1", SessionID: &sessionID, SessionName: &name, QuestionID: &askID,
		Message: &question, OccurredAtUnixMs: occurredAt,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{Kind: serverapi.WorkflowAttentionQuestionKindOrdinary},
	}
}

func approvalQuestionAttention(sessionID string, approvalID string, occurredAt int64) serverapi.WorkflowAttentionItem {
	return serverapi.WorkflowAttentionItem{
		ID: approvalID, Kind: string(serverapi.WorkflowTaskAttentionKindQuestion),
		TaskID: "task-1", SessionID: &sessionID, QuestionID: &approvalID, OccurredAtUnixMs: occurredAt,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{Kind: serverapi.WorkflowAttentionQuestionKindApproval},
	}
}

func mustQuestionSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
