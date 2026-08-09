package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"

	"github.com/google/uuid"
)

type stubQuestionCommandRemote struct {
	listResponses     []serverapi.AskListPendingBySessionResponse
	listRequests      []serverapi.AskListPendingBySessionRequest
	approvalResponses []serverapi.ApprovalListPendingBySessionResponse
	answerRequests    []serverapi.PromptAnswerBatchRequest
	watchRequests     []serverapi.PromptFollowUpWatchRequest
	operations        []string
	batchErr          error
	watchErr          error
	followUpErr       error
	outcome           serverapi.PromptAnswerBatchOutcome
}

type stubQuestionTaskRemote struct {
	apicontract.WorkflowService
	task               serverapi.WorkflowTaskDetail
	taskRequests       []serverapi.WorkflowTaskGetRequest
	attentionResponses []serverapi.WorkflowTaskAttentionListResponse
	attentionRequests  []serverapi.WorkflowTaskAttentionListRequest
	listResponses      []serverapi.AskListPendingBySessionResponse
	approvalResponses  []serverapi.ApprovalListPendingBySessionResponse
	answerRequests     []serverapi.PromptAnswerBatchRequest
	watchRequests      []serverapi.PromptFollowUpWatchRequest
}

func (r *stubQuestionTaskRemote) GetWorkflowTask(
	_ context.Context,
	req serverapi.WorkflowTaskGetRequest,
) (serverapi.WorkflowTaskGetResponse, error) {
	r.taskRequests = append(r.taskRequests, req)
	return serverapi.WorkflowTaskGetResponse{Task: r.task}, nil
}

func (r *stubQuestionTaskRemote) ListWorkflowTaskAttention(
	_ context.Context,
	req serverapi.WorkflowTaskAttentionListRequest,
) (serverapi.WorkflowTaskAttentionListResponse, error) {
	r.attentionRequests = append(r.attentionRequests, req)
	if len(r.attentionResponses) == 0 {
		return serverapi.WorkflowTaskAttentionListResponse{}, nil
	}
	response := r.attentionResponses[0]
	r.attentionResponses = r.attentionResponses[1:]
	return response, nil
}

func (r *stubQuestionTaskRemote) AnswerPromptBatch(
	_ context.Context,
	req serverapi.PromptAnswerBatchRequest,
) (serverapi.PromptAnswerBatchResponse, error) {
	r.answerRequests = append(r.answerRequests, req)
	return resolvedQuestionBatchResponse(req), nil
}

func (r *stubQuestionTaskRemote) ListPendingApprovalsBySession(
	_ context.Context,
	_ serverapi.ApprovalListPendingBySessionRequest,
) (serverapi.ApprovalListPendingBySessionResponse, error) {
	if len(r.approvalResponses) == 0 {
		return serverapi.ApprovalListPendingBySessionResponse{}, nil
	}
	response := r.approvalResponses[0]
	r.approvalResponses = r.approvalResponses[1:]
	return response, nil
}

func (r *stubQuestionTaskRemote) ListPendingAsksBySession(
	_ context.Context,
	_ serverapi.AskListPendingBySessionRequest,
) (serverapi.AskListPendingBySessionResponse, error) {
	if len(r.listResponses) == 0 {
		return serverapi.AskListPendingBySessionResponse{}, nil
	}
	response := r.listResponses[0]
	r.listResponses = r.listResponses[1:]
	return response, nil
}

func (r *stubQuestionTaskRemote) SubscribeFollowUp(
	context.Context,
	serverapi.PromptFollowUpWatchRequest,
) (serverapi.PromptFollowUpSubscription, error) {
	return &stubQuestionFollowUpSubscription{event: serverapi.PromptFollowUpEvent{Kind: serverapi.PromptFollowUpNoPreparedSuccessor}}, nil
}

func (r *stubQuestionTaskRemote) ResolveProjectPath(
	_ context.Context,
	_ serverapi.ProjectResolvePathRequest,
) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{
		Binding: &serverapi.ProjectBinding{ProjectID: "project-1"},
	}, nil
}

func (r *stubQuestionTaskRemote) Close() error {
	return nil
}

func (r *stubQuestionCommandRemote) ListPendingAsksBySession(
	_ context.Context,
	req serverapi.AskListPendingBySessionRequest,
) (serverapi.AskListPendingBySessionResponse, error) {
	r.listRequests = append(r.listRequests, req)
	if len(r.listResponses) == 0 {
		return serverapi.AskListPendingBySessionResponse{}, nil
	}
	response := r.listResponses[0]
	r.listResponses = r.listResponses[1:]
	return response, nil
}

func (r *stubQuestionCommandRemote) AnswerPromptBatch(_ context.Context, req serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error) {
	r.operations = append(r.operations, "batch")
	r.answerRequests = append(r.answerRequests, req)
	if r.batchErr != nil {
		return serverapi.PromptAnswerBatchResponse{}, r.batchErr
	}
	outcome := r.outcome
	if outcome == "" {
		outcome = serverapi.PromptAnswerBatchOutcomeResolved
	}
	return serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{{
		PromptID: req.Entries[0].PromptID,
		Outcome:  outcome,
	}}}, nil
}

func (r *stubQuestionCommandRemote) ListPendingApprovalsBySession(
	_ context.Context,
	_ serverapi.ApprovalListPendingBySessionRequest,
) (serverapi.ApprovalListPendingBySessionResponse, error) {
	if len(r.approvalResponses) == 0 {
		return serverapi.ApprovalListPendingBySessionResponse{}, nil
	}
	response := r.approvalResponses[0]
	r.approvalResponses = r.approvalResponses[1:]
	return response, nil
}

func (r *stubQuestionCommandRemote) SubscribeFollowUp(_ context.Context, req serverapi.PromptFollowUpWatchRequest) (serverapi.PromptFollowUpSubscription, error) {
	r.operations = append(r.operations, "watch")
	r.watchRequests = append(r.watchRequests, req)
	if r.watchErr != nil {
		return nil, r.watchErr
	}
	return &stubQuestionFollowUpSubscription{
		event: serverapi.PromptFollowUpEvent{Kind: serverapi.PromptFollowUpNoPreparedSuccessor},
		err:   r.followUpErr,
	}, nil
}

func (r *stubQuestionCommandRemote) Close() error {
	return nil
}

type stubQuestionFollowUpSubscription struct {
	event  serverapi.PromptFollowUpEvent
	err    error
	closed bool
}

func (s *stubQuestionFollowUpSubscription) Next(context.Context) (serverapi.PromptFollowUpEvent, error) {
	return s.event, s.err
}

func (s *stubQuestionFollowUpSubscription) Close() error {
	s.closed = true
	return nil
}

func resolvedQuestionBatchResponse(request serverapi.PromptAnswerBatchRequest) serverapi.PromptAnswerBatchResponse {
	return serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{{
		PromptID: request.Entries[0].PromptID,
		Outcome:  serverapi.PromptAnswerBatchOutcomeResolved,
	}}}
}

func requireQuestionBatchEntry(t *testing.T, request serverapi.PromptAnswerBatchRequest) serverapi.PromptAnswerBatchEntry {
	t.Helper()
	if len(request.Entries) != 1 || request.Entries[0].QuestionAnswer == nil {
		t.Fatalf("batch request = %+v, want one Question answer", request)
	}
	return request.Entries[0]
}

func requireApprovalBatchEntry(t *testing.T, request serverapi.PromptAnswerBatchRequest) serverapi.PromptAnswerBatchEntry {
	t.Helper()
	if len(request.Entries) != 1 || request.Entries[0].ApprovalAnswer == nil {
		t.Fatalf("batch request = %+v, want one Approval answer", request)
	}
	return request.Entries[0]
}

func questionCommandWithRemote(remote questionCommandRemote) (questionCommand, *[]string) {
	openedSessions := []string{}
	return questionCommand{
		openRemote: func(_ context.Context, sessionID string) (questionCommandRemote, error) {
			openedSessions = append(openedSessions, sessionID)
			return remote, nil
		},
	}, &openedSessions
}

func TestQuestionCommandPendingQuestionHasOnePromptIdentity(t *testing.T) {
	questionType := reflect.TypeOf(questionCommandPendingQuestion{})
	fields := make([]string, 0, questionType.NumField())
	for index := 0; index < questionType.NumField(); index++ {
		fields = append(fields, questionType.Field(index).Name)
	}
	if slices.Contains(fields, "AskID") {
		t.Fatalf("question command pending fields = %v, want no duplicate Ask identity", fields)
	}
}

func TestQuestionCommandProductionRemoteOpenerIsImmutable(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "question_command.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse question_command.go: %v", err)
	}
	foundFunction := false
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if typed.Name.Name == "openQuestionCommandRemote" {
				foundFunction = true
			}
		case *ast.GenDecl:
			for _, specification := range typed.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if name.Name == "openQuestionCommandRemote" {
						t.Fatal("openQuestionCommandRemote is mutable production state")
					}
				}
			}
		}
	}
	if !foundFunction {
		t.Fatal("openQuestionCommandRemote function is missing")
	}
}

func installQuestionTaskRemote(t *testing.T, remote workflowCommandRemote) questionCommand {
	t.Helper()
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{WorkspaceRoot: "."}, remote, nil
	}
	promptRemote, ok := remote.(questionCommandRemote)
	if !ok {
		t.Fatal("question Task remote does not implement prompt control")
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})
	command, _ := questionCommandWithRemote(promptRemote)
	return command
}

func pendingAsk(sessionID, askID, question string, suggestions ...string) clientui.PendingAsk {
	typedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	return clientui.PendingAsk{
		PromptID:    clientui.PromptID(askID),
		SessionID:   typedSessionID,
		StepID:      questionCommandStepID(),
		Question:    question,
		Suggestions: suggestions,
	}
}

func pendingApproval(
	sessionID string,
	promptID string,
	question string,
	options ...clientui.ApprovalOption,
) clientui.PendingApproval {
	typedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	return clientui.PendingApproval{
		PromptID:  clientui.PromptID(promptID),
		SessionID: typedSessionID,
		StepID:    questionCommandStepID(),
		Question:  question,
		Options:   append([]clientui.ApprovalOption(nil), options...),
	}
}

func taskQuestionAttention(
	taskID string,
	sessionID string,
	sessionName string,
	askID string,
	question string,
	occurredAt int64,
) serverapi.WorkflowAttentionItem {
	typedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	return serverapi.WorkflowAttentionItem{
		Kind:        "question",
		TaskID:      taskID,
		SessionName: &sessionName,
		Message:     &question,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{
			SessionID: typedSessionID,
			StepID:    questionCommandStepID(),
			PromptID:  clientui.PromptID(askID),
			Kind:      serverapi.WorkflowAttentionQuestionKindOrdinary,
		},
		OccurredAtUnixMs: occurredAt,
	}
}

func taskApprovalAttention(
	taskID string,
	sessionID string,
	approvalID string,
	question string,
	decisions ...clientui.ApprovalDecision,
) serverapi.WorkflowAttentionItem {
	typedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		panic(err)
	}
	return serverapi.WorkflowAttentionItem{
		Kind:    string(serverapi.WorkflowTaskAttentionKindQuestion),
		TaskID:  taskID,
		Message: &question,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{
			SessionID:         typedSessionID,
			StepID:            questionCommandStepID(),
			PromptID:          clientui.PromptID(approvalID),
			Kind:              serverapi.WorkflowAttentionQuestionKindApproval,
			ApprovalDecisions: append([]clientui.ApprovalDecision(nil), decisions...),
		},
	}
}

func TestQuestionShowsFirstPendingQuestion(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{{
			Asks: []clientui.PendingAsk{
				pendingAsk(sessionID, "ask-1", "First?", "Yes", "No"),
				pendingAsk(sessionID, "ask-2", "Second?"),
			},
		}},
	}
	command, openedSessions := questionCommandWithRemote(remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run([]string{"--session", sessionID}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("question output is empty")
	}
	if len(*openedSessions) != 1 || (*openedSessions)[0] != sessionID {
		t.Fatalf("opened sessions = %v, want %q", *openedSessions, sessionID)
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].SessionID != sessionID {
		t.Fatalf("list requests = %+v", remote.listRequests)
	}
	if len(remote.answerRequests) != 0 {
		t.Fatalf("answer requests = %+v, want none", remote.answerRequests)
	}
}

func TestPendingSessionQuestionPreservesRecommendationPresenceAndRejectsInvalidIndexes(t *testing.T) {
	recommended := 2
	present, err := pendingSessionQuestion(clientui.PendingAsk{
		PromptID:               "ask-present",
		SessionID:              questionCommandSessionID(t),
		StepID:                 questionCommandStepID(),
		Suggestions:            []string{"one", "two"},
		RecommendedOptionIndex: &recommended,
	})
	if err != nil {
		t.Fatalf("pending Session question with recommendation: %v", err)
	}
	if present.RecommendedOptionIndex == nil || *present.RecommendedOptionIndex != recommended {
		t.Fatalf("present recommendation = %v, want %d", present.RecommendedOptionIndex, recommended)
	}

	absent, err := pendingSessionQuestion(clientui.PendingAsk{
		PromptID:    "ask-absent",
		SessionID:   questionCommandSessionID(t),
		StepID:      questionCommandStepID(),
		Suggestions: []string{"one"},
	})
	if err != nil {
		t.Fatalf("pending Session question without recommendation: %v", err)
	}
	if absent.RecommendedOptionIndex != nil {
		t.Fatalf("absent recommendation = %v, want nil", absent.RecommendedOptionIndex)
	}

	invalid := 0
	if _, err := pendingSessionQuestion(clientui.PendingAsk{
		PromptID:               "ask-invalid",
		SessionID:              questionCommandSessionID(t),
		StepID:                 questionCommandStepID(),
		Suggestions:            []string{"one"},
		RecommendedOptionIndex: &invalid,
	}); err == nil {
		t.Fatal("accepted present zero recommendation")
	}
}

func questionCommandSessionID(t *testing.T) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func questionCommandStepID() runtimeids.StepID {
	stepID, err := runtimeids.ParseStepID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		panic(err)
	}
	return stepID
}

func TestQuestionsAliasDispatchesQuestionCommand(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)

	var stdout, stderr bytes.Buffer
	exitCode := rootCommand([]string{"questions", "--help"}, bytes.NewReader(nil), &stdout, &stderr)

	if exitCode != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestQuestionAnswerSubmitsOptionAndCommentaryThenReadsNextQuestion(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "First?", "Yes", "No")}},
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-2", "Second?", "A", "B")}},
		},
	}
	command, _ := questionCommandWithRemote(remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run([]string{
		"answer",
		"--session", sessionID,
		"--option", "2",
		"--commentary", "  Because it is safer \t",
	}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.answerRequests) != 1 {
		t.Fatalf("answer requests = %+v", remote.answerRequests)
	}
	request := remote.answerRequests[0]
	entry := requireQuestionBatchEntry(t, request)
	if request.SessionID.String() != sessionID || entry.PromptID != "ask-1" {
		t.Fatalf("answer target = %+v", request)
	}
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 2 {
		t.Fatalf("selected option = %v, want 2", entry.QuestionAnswer.SelectedOptionNumber)
	}
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != "Because it is safer" {
		t.Fatalf("commentary = %v", entry.QuestionAnswer.Freeform)
	}
	if len(remote.operations) < 2 || remote.operations[0] != "watch" || remote.operations[1] != "batch" {
		t.Fatalf("operations = %v, want watch before batch", remote.operations)
	}
	if len(remote.listRequests) != 2 {
		t.Fatalf("list requests = %+v, want initial and post-answer reads", remote.listRequests)
	}
}

func TestQuestionAnswerAcceptsFreeformWithoutOption(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "Explain?")}},
			{},
		},
	}
	command, _ := questionCommandWithRemote(remote)

	exitCode := command.run(
		[]string{"answer", "--session", sessionID, "--commentary", "Freeform answer"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.answerRequests) != 1 {
		t.Fatalf("exit=%d answer requests=%+v", exitCode, remote.answerRequests)
	}
	entry := requireQuestionBatchEntry(t, remote.answerRequests[0])
	if entry.QuestionAnswer.SelectedOptionNumber != nil ||
		entry.QuestionAnswer.Freeform == nil ||
		*entry.QuestionAnswer.Freeform != "Freeform answer" {
		t.Fatalf("answer request = %+v", remote.answerRequests[0])
	}
}

func TestQuestionAnswerAcceptsOptionWithoutCommentary(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "Choose?", "One", "Two")}},
			{},
		},
	}
	command, _ := questionCommandWithRemote(remote)

	exitCode := command.run(
		[]string{"answer", "--session", sessionID, "--option", "1"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.answerRequests) != 1 {
		t.Fatalf("exit=%d answer requests=%+v", exitCode, remote.answerRequests)
	}
	request := remote.answerRequests[0]
	entry := requireQuestionBatchEntry(t, request)
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 1 ||
		entry.QuestionAnswer.Freeform != nil {
		t.Fatalf("answer request = %+v", request)
	}
}

func TestQuestionAnswerAcceptsSkippedRaceAndStillReadsAuthoritativeFollowUp(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		outcome: serverapi.PromptAnswerBatchOutcomeSkipped,
		listResponses: []serverapi.AskListPendingBySessionResponse{
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "Choose?", "One")}},
			{},
		},
	}
	command, _ := questionCommandWithRemote(remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run(
		[]string{"answer", "--session", sessionID, "--option", "1"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 || stdout.Len() == 0 || len(remote.listRequests) != 2 {
		t.Fatalf("exit=%d stdout=%q stderr=%q reads=%d", exitCode, stdout.String(), stderr.String(), len(remote.listRequests))
	}
}

func TestQuestionAnswerFailuresDoNotAuthorizeMutationOrDone(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	tests := []struct {
		name      string
		configure func(*stubQuestionCommandRemote)
		wantBatch int
		wantReads int
	}{
		{
			name: "watch",
			configure: func(remote *stubQuestionCommandRemote) {
				remote.watchErr = errors.New("unknown prompt")
			},
			wantBatch: 0,
			wantReads: 1,
		},
		{
			name: "batch",
			configure: func(remote *stubQuestionCommandRemote) {
				remote.batchErr = errors.New("answer failed")
			},
			wantBatch: 1,
			wantReads: 1,
		},
		{
			name: "follow-up",
			configure: func(remote *stubQuestionCommandRemote) {
				remote.followUpErr = errors.New("watch failed")
			},
			wantBatch: 1,
			wantReads: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &stubQuestionCommandRemote{
				listResponses: []serverapi.AskListPendingBySessionResponse{{
					Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "Choose?", "One")},
				}},
			}
			test.configure(remote)
			command, _ := questionCommandWithRemote(remote)
			var stdout, stderr bytes.Buffer

			exitCode := command.run(
				[]string{"answer", "--session", sessionID, "--option", "1"},
				&stdout,
				&stderr,
			)

			if exitCode != 1 || stdout.Len() != 0 || stderr.Len() == 0 ||
				len(remote.answerRequests) != test.wantBatch ||
				len(remote.listRequests) != test.wantReads {
				t.Fatalf(
					"exit=%d stdout=%q stderr=%q batches=%d reads=%d",
					exitCode,
					stdout.String(),
					stderr.String(),
					len(remote.answerRequests),
					len(remote.listRequests),
				)
			}
		})
	}
}

func TestQuestionAnswerWithoutPendingQuestionFailsWithoutSubmitting(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{{}},
	}
	command, _ := questionCommandWithRemote(remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run(
		[]string{"answer", "--session", sessionID, "--option", "1"},
		&stdout,
		&stderr,
	)

	if exitCode != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.answerRequests) != 0 {
		t.Fatalf("answer requests = %+v, want none", remote.answerRequests)
	}
}

func TestQuestionAnswerRequiresOptionOrCommentary(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{}
	command, openedSessions := questionCommandWithRemote(remote)

	exitCode := command.run(
		[]string{"answer", "--session", sessionID},
		io.Discard,
		io.Discard,
	)

	if exitCode != 2 {
		t.Fatalf("exit=%d, want usage error", exitCode)
	}
	if len(*openedSessions) != 0 {
		t.Fatalf("opened sessions = %v, want none", *openedSessions)
	}
}

func TestQuestionAnswerRejectsExplicitBlankCommentaryWithOption(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{}
	command, openedSessions := questionCommandWithRemote(remote)

	exitCode := command.run(
		[]string{"answer", "--session", sessionID, "--option", "1", "--commentary", " \t "},
		io.Discard,
		io.Discard,
	)

	if exitCode != 2 {
		t.Fatalf("exit=%d, want usage error", exitCode)
	}
	if len(*openedSessions) != 0 {
		t.Fatalf("opened sessions = %v, want none", *openedSessions)
	}
}

func TestQuestionRejectsCurrentAgentSession(t *testing.T) {
	sessionID := uuid.NewString()
	previous, present := os.LookupEnv(sessionenv.SessionIDEnv)
	if err := os.Setenv(sessionenv.SessionIDEnv, sessionID); err != nil {
		t.Fatalf("set %s: %v", sessionenv.SessionIDEnv, err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(sessionenv.SessionIDEnv, previous)
		} else {
			_ = os.Unsetenv(sessionenv.SessionIDEnv)
		}
	})
	remote := &stubQuestionCommandRemote{}
	command, openedSessions := questionCommandWithRemote(remote)

	exitCode := command.run(
		[]string{"--session", sessionID},
		io.Discard,
		io.Discard,
	)

	if exitCode != 2 {
		t.Fatalf("exit=%d, want usage error", exitCode)
	}
	if len(*openedSessions) != 0 {
		t.Fatalf("opened sessions = %v, want none", *openedSessions)
	}
}

func TestQuestionAnswerByTaskSelectsUniqueSession(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	sessionID := uuid.NewString()
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{
			{Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-1", "Continue?", 1),
			}},
			{},
		},
		listResponses: []serverapi.AskListPendingBySessionResponse{{
			Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "Continue?")},
		}},
	}
	command := installQuestionTaskRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run(
		[]string{"answer", "--task", taskID, "--commentary", "  Proceed \n"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.answerRequests) != 1 {
		t.Fatalf("answer requests = %+v", remote.answerRequests)
	}
	request := remote.answerRequests[0]
	entry := requireQuestionBatchEntry(t, request)
	if entry.PromptID != "ask-1" ||
		entry.QuestionAnswer.Freeform == nil ||
		*entry.QuestionAnswer.Freeform != "Proceed" {
		t.Fatalf("answer request = %+v", request)
	}
	if len(remote.attentionRequests) != 2 {
		t.Fatalf("attention requests = %+v", remote.attentionRequests)
	}
}

func TestQuestionByTaskApprovalReadsSuccessorQuestion(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	sessionID := uuid.NewString()
	approvalID := "approval-1"
	successorQuestion := "Next?"
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{
			{Items: []serverapi.WorkflowAttentionItem{
				taskApprovalAttention(
					taskID,
					sessionID,
					approvalID,
					"Allow access?",
					clientui.ApprovalDecisionAllowOnce,
				),
			}},
			{Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-next", successorQuestion, 2),
			}},
		},
		listResponses: []serverapi.AskListPendingBySessionResponse{
			{},
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-next", successorQuestion)}},
		},
		approvalResponses: []serverapi.ApprovalListPendingBySessionResponse{
			{Approvals: []clientui.PendingApproval{pendingApproval(
				sessionID,
				approvalID,
				"Allow access?",
				clientui.ApprovalOption{
					Decision: clientui.ApprovalDecisionAllowOnce,
					Label:    "Allow once",
				},
			)}},
			{},
		},
	}
	command := installQuestionTaskRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run(
		[]string{"answer", "--task", taskID, "--option", "1"},
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.answerRequests) != 1 {
		t.Fatalf("approval requests = %+v", remote.answerRequests)
	}
	request := remote.answerRequests[0]
	entry := requireApprovalBatchEntry(t, request)
	if request.SessionID.String() != sessionID || entry.PromptID != clientui.PromptID(approvalID) ||
		entry.ApprovalAnswer.Decision != clientui.ApprovalDecisionAllowOnce ||
		entry.ApprovalAnswer.Commentary != nil {
		t.Fatalf("approval request = %+v", request)
	}
	if !strings.Contains(stdout.String(), successorQuestion) {
		t.Fatalf("successor question output = %q", stdout.String())
	}
	if len(remote.attentionRequests) != 2 {
		t.Fatalf("attention requests = %+v", remote.attentionRequests)
	}
}

func TestQuestionByTaskUsesAuthoritativeApprovalOptionLabels(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	sessionID := uuid.NewString()
	approvalID := "approval-1"
	authoritativeLabel := "Grant this workspace once"
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{
			Items: []serverapi.WorkflowAttentionItem{
				taskApprovalAttention(
					taskID,
					sessionID,
					approvalID,
					"Allow access?",
					clientui.ApprovalDecisionAllowOnce,
				),
			},
		}},
		approvalResponses: []serverapi.ApprovalListPendingBySessionResponse{{
			Approvals: []clientui.PendingApproval{pendingApproval(
				sessionID,
				approvalID,
				"Allow access?",
				clientui.ApprovalOption{
					Decision: clientui.ApprovalDecisionAllowOnce,
					Label:    authoritativeLabel,
				},
			)},
		}},
	}
	command := installQuestionTaskRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run([]string{"--task", taskID}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), authoritativeLabel) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestQuestionByTaskRejectsAmbiguousSessionsWithoutAnswering(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	firstSessionID := uuid.NewString()
	secondSessionID := uuid.NewString()
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{
			Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, firstSessionID, "Planner", "ask-1", "Plan?", 1),
				taskQuestionAttention(taskID, secondSessionID, "Implementer", "ask-2", "Build?", 2),
			},
		}},
	}
	command := installQuestionTaskRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := command.run(
		[]string{"answer", "--task", taskID, "--option", "1"},
		&stdout,
		&stderr,
	)

	if exitCode != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.answerRequests) != 0 {
		t.Fatalf("answer requests = %+v, want none", remote.answerRequests)
	}
}

func TestQuestionByTaskRejectsCurrentAgentSession(t *testing.T) {
	taskID := "task-1"
	sessionID := uuid.NewString()
	previous, present := os.LookupEnv(sessionenv.SessionIDEnv)
	if err := os.Setenv(sessionenv.SessionIDEnv, sessionID); err != nil {
		t.Fatalf("set %s: %v", sessionenv.SessionIDEnv, err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(sessionenv.SessionIDEnv, previous)
		} else {
			_ = os.Unsetenv(sessionenv.SessionIDEnv)
		}
	})
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{
			Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Current", "ask-1", "Continue?", 1),
			},
		}},
	}
	command := installQuestionTaskRemote(t, remote)

	exitCode := command.run(
		[]string{"--task", taskID},
		io.Discard,
		io.Discard,
	)

	if exitCode != 2 {
		t.Fatalf("exit=%d, want usage error", exitCode)
	}
}

func TestQuestionByTaskUsesOldestQuestionInSelectedSession(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	sessionID := uuid.NewString()
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{
			Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-new", "Newer?", 2),
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-old", "Older?", 1),
			},
		}, {}},
		listResponses: []serverapi.AskListPendingBySessionResponse{{
			Asks: []clientui.PendingAsk{
				pendingAsk(sessionID, "ask-new", "Newer?"),
				pendingAsk(sessionID, "ask-old", "Older?"),
			},
		}},
	}
	command := installQuestionTaskRemote(t, remote)

	exitCode := command.run(
		[]string{"answer", "--task", taskID, "--commentary", "Answer"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.answerRequests) != 1 {
		t.Fatalf("exit=%d answer requests=%+v", exitCode, remote.answerRequests)
	}
	if entry := requireQuestionBatchEntry(t, remote.answerRequests[0]); entry.PromptID != "ask-old" {
		t.Fatalf("answered ask = %q, want oldest", entry.PromptID)
	}
}

func TestTaskQuestionCandidatesPreserveRecommendationAbsence(t *testing.T) {
	sessionID := uuid.NewString()
	withoutRecommendation := taskQuestionAttention(
		"task-1",
		sessionID,
		"Implementer",
		"ask-1",
		"First?",
		1,
	)
	withRecommendation := taskQuestionAttention(
		"task-1",
		sessionID,
		"Implementer",
		"ask-2",
		"Second?",
		2,
	)
	recommendedOptionIndex := 2
	withRecommendation.Question.RecommendedOptionIndex = &recommendedOptionIndex

	candidates, err := taskQuestionCandidates([]serverapi.WorkflowAttentionItem{
		withoutRecommendation,
		withRecommendation,
	})
	if err != nil {
		t.Fatalf("task question candidates: %v", err)
	}
	if len(candidates) != 1 || len(candidates[0].Questions) != 2 {
		t.Fatalf("task question candidates = %+v, want one Session with two Questions", candidates)
	}
	if candidates[0].Questions[0].RecommendedOptionIndex != nil {
		t.Fatalf(
			"absent recommendation = %v, want nil",
			*candidates[0].Questions[0].RecommendedOptionIndex,
		)
	}
	if got := candidates[0].Questions[1].RecommendedOptionIndex; got == nil || *got != 2 {
		t.Fatalf("present recommendation = %v, want 2", got)
	}
}

func TestTaskQuestionCandidatesIgnoreWorkflowTransitionApprovals(t *testing.T) {
	approvalID := "transition-1"
	candidates, err := taskQuestionCandidates([]serverapi.WorkflowAttentionItem{{
		Kind:       "approval",
		ApprovalID: &approvalID,
	}})
	if err != nil {
		t.Fatalf("task question candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("transition approval candidates = %+v, want none", candidates)
	}
}

func TestQuestionTaskStaleLiveApprovalIsOmittedForShowAndAnswer(t *testing.T) {
	sessionID := "00000000-0000-4000-8000-000000000001"
	questionID := "approval-1"
	prompt := serverapi.WorkflowAttentionQuestionPrompt{Kind: serverapi.WorkflowAttentionQuestionKindApproval}
	item := serverapi.WorkflowAttentionItem{
		Kind:       string(serverapi.WorkflowTaskAttentionKindQuestion),
		SessionID:  &sessionID,
		QuestionID: &questionID,
		Question:   &prompt,
	}
	for _, test := range []struct {
		name string
		args []string
		want int
	}{
		{name: "show", args: []string{"--task", "KENT-335"}, want: 0},
		{name: "answer", args: []string{"answer", "--task", "KENT-335", "--commentary", "allow"}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			unsetSessionIDEnvironmentForTest(t)
			remote := &stubQuestionTaskRemote{
				task: serverapi.WorkflowTaskDetail{
					Summary: serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "KENT-335"},
				},
				attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{Items: []serverapi.WorkflowAttentionItem{item}}},
			}
			command := installQuestionTaskRemote(t, remote)
			var stdout, stderr bytes.Buffer
			if got := command.run(test.args, &stdout, &stderr); got != test.want {
				t.Fatalf("exit=%d, stdout=%q, stderr=%q", got, stdout.String(), stderr.String())
			}
		})
	}
}

func TestQuestionByTaskResolvesProjectScopedShortID(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "KENT-335"},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{}},
	}
	command := installQuestionTaskRemote(t, remote)

	exitCode := command.run(
		[]string{"--task", "KENT-335"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.taskRequests) != 1 {
		t.Fatalf("exit=%d task requests=%+v", exitCode, remote.taskRequests)
	}
	request := remote.taskRequests[0]
	if request.ProjectID != "project-1" || request.ShortID != "KENT-335" {
		t.Fatalf("task request = %+v", request)
	}
}

func TestQuestionRequiresExactlyOneSelector(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()

	for _, args := range [][]string{
		nil,
		{"--session", sessionID, "--task", "task-1"},
	} {
		exitCode := questionSubcommand(args, io.Discard, io.Discard)
		if exitCode != 2 {
			t.Fatalf("args=%v exit=%d, want usage error", args, exitCode)
		}
	}
}
