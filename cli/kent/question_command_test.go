package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
	listAsks          func(context.Context) error
	approvalResponses []serverapi.ApprovalListPendingBySessionResponse
	answerRequests    []serverapi.PromptAnswerBatchRequest
	watchRequests     []serverapi.PromptFollowUpWatchRequest
	batchErr          error
	watchErr          error
	followUpErr       error
	watchNext         func(context.Context) error
	followUpKind      serverapi.PromptFollowUpEventKind
	outcome           serverapi.PromptAnswerBatchOutcome
	watchNexts        int
	remoteClosed      bool
	subscription      *stubPromptFollowUpSubscription
}

type stubPromptFollowUpSubscription struct {
	remote *stubQuestionCommandRemote
	closed bool
}

type stubQuestionTaskRemote struct {
	apicontract.ProjectViewService
	apicontract.WorkflowService
	task               serverapi.WorkflowTaskDetail
	taskRequests       []serverapi.WorkflowTaskGetRequest
	attentionResponses []serverapi.WorkflowTaskAttentionListResponse
	attentionRequests  []serverapi.WorkflowTaskAttentionListRequest
	*stubQuestionCommandRemote
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

func (r *stubQuestionTaskRemote) ResolveProjectPath(
	_ context.Context,
	_ serverapi.ProjectResolvePathRequest,
) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{
		Binding: &serverapi.ProjectBinding{ProjectID: "project-1"},
	}, nil
}

func (r *stubQuestionCommandRemote) ListPendingAsksBySession(
	ctx context.Context,
	req serverapi.AskListPendingBySessionRequest,
) (serverapi.AskListPendingBySessionResponse, error) {
	r.listRequests = append(r.listRequests, req)
	if r.listAsks != nil {
		if err := r.listAsks(ctx); err != nil {
			return serverapi.AskListPendingBySessionResponse{}, err
		}
	}
	if len(r.listResponses) == 0 {
		return serverapi.AskListPendingBySessionResponse{}, nil
	}
	response := r.listResponses[0]
	r.listResponses = r.listResponses[1:]
	return response, nil
}

func (r *stubQuestionCommandRemote) AnswerPromptBatch(_ context.Context, req serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error) {
	if len(r.watchRequests) == 0 {
		return serverapi.PromptAnswerBatchResponse{}, errors.New("batch opened before follow-up watch")
	}
	r.answerRequests = append(r.answerRequests, req)
	if r.batchErr != nil {
		return serverapi.PromptAnswerBatchResponse{}, r.batchErr
	}
	outcome := r.outcome
	if outcome == "" {
		outcome = serverapi.PromptAnswerBatchOutcomeResolved
	}
	return serverapi.PromptAnswerBatchResponse{Results: []serverapi.PromptAnswerBatchResult{{PromptID: req.Entries[0].PromptID, Outcome: outcome}}}, nil
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
	r.watchRequests = append(r.watchRequests, req)
	if r.watchErr != nil {
		return nil, r.watchErr
	}
	kind := r.followUpKind
	if kind == "" {
		kind = serverapi.PromptFollowUpNoPreparedSuccessor
	}
	r.followUpKind = kind
	r.subscription = &stubPromptFollowUpSubscription{remote: r}
	return r.subscription, nil
}

func (r *stubQuestionCommandRemote) Close() error {
	r.remoteClosed = true
	return nil
}

func (s *stubPromptFollowUpSubscription) Next(ctx context.Context) (serverapi.PromptFollowUpEvent, error) {
	r := s.remote
	r.watchNexts++
	if r.watchNext != nil {
		if err := r.watchNext(ctx); err != nil {
			return serverapi.PromptFollowUpEvent{}, err
		}
	}
	return serverapi.PromptFollowUpEvent{Kind: r.followUpKind}, r.followUpErr
}

func (s *stubPromptFollowUpSubscription) Close() error { s.closed = true; return nil }

func requireQuestionBatchEntry(t *testing.T, request serverapi.PromptAnswerBatchRequest) serverapi.PromptAnswerBatchEntry {
	t.Helper()
	if len(request.Entries) != 1 || request.Entries[0].QuestionAnswer == nil {
		t.Fatalf("batch request = %+v, want one Question answer", request)
	}
	return request.Entries[0]
}

func requireQuestionWatch(t *testing.T, remote *stubQuestionCommandRemote, wantNext int) {
	t.Helper()
	if len(remote.watchRequests) != 1 {
		t.Fatalf("watch requests = %d, want 1", len(remote.watchRequests))
	}
	if !remote.remoteClosed ||
		remote.watchErr == nil && (remote.subscription == nil || !remote.subscription.closed || remote.watchNexts != wantNext) {
		t.Fatalf("subscription = %+v, want closed with %d reads", remote, wantNext)
	}
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

func questionCommandWithTaskRemote(remote *stubQuestionTaskRemote) questionCommand {
	command, _ := questionCommandWithRemote(remote.stubQuestionCommandRemote)
	return command
}

func questionTaskSelector(taskRef string) questionCommandSelector {
	return questionCommandSelector{
		TaskRef:    &taskRef,
		ProjectRef: ".",
		Command:    config.Command + " question",
	}
}

func unsetSessionIDEnvironmentForTest(t *testing.T) {
	t.Helper()
	previous, present := os.LookupEnv(sessionenv.SessionIDEnv)
	if err := os.Unsetenv(sessionenv.SessionIDEnv); err != nil {
		t.Fatalf("unset %s: %v", sessionenv.SessionIDEnv, err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(sessionenv.SessionIDEnv, previous)
		} else {
			_ = os.Unsetenv(sessionenv.SessionIDEnv)
		}
	})
}

func pendingAsk(sessionID, askID, question string, suggestions ...string) clientui.PendingAsk {
	return clientui.PendingAsk{
		PromptID:    clientui.PromptID(askID),
		SessionID:   mustQuestionCommandSessionID(sessionID),
		StepID:      questionCommandStepID(),
		Question:    question,
		Suggestions: suggestions,
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
	return serverapi.WorkflowAttentionItem{
		Kind:        "question",
		TaskID:      taskID,
		SessionName: &sessionName,
		Message:     &question,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{
			SessionID: mustQuestionCommandSessionID(sessionID),
			StepID:    questionCommandStepID(),
			PromptID:  clientui.PromptID(askID),
			Kind:      serverapi.WorkflowAttentionQuestionKindOrdinary,
		},
		OccurredAtUnixMs: occurredAt,
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

func questionCommandStepID() runtimeids.StepID {
	stepID, err := runtimeids.ParseStepID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		panic(err)
	}
	return stepID
}

func mustQuestionCommandSessionID(raw string) runtimeids.SessionID {
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		panic(err)
	}
	return sessionID
}

func TestQuestionsAliasDispatchesQuestionCommand(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)

	var stdout, stderr bytes.Buffer
	exitCode := rootCommand([]string{"questions", "--help"}, bytes.NewReader(nil), &stdout, &stderr)

	if exitCode != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestQuestionAnswerSubmitsThenReconcilesWithoutWorkflowDeadline(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	readCount := 0
	checkTransportDeadline := func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("post-answer transport deadline is missing")
		}
		return nil
	}
	remote := &stubQuestionCommandRemote{
		outcome:      serverapi.PromptAnswerBatchOutcomeSkipped,
		followUpKind: serverapi.PromptFollowUpExecutionClosed,
		listResponses: []serverapi.AskListPendingBySessionResponse{
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "First?", "Yes", "No")}},
			{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-2", "Second?", "A", "B")}},
		},
		listAsks: func(ctx context.Context) error {
			readCount++
			if readCount == 2 {
				return checkTransportDeadline(ctx)
			}
			return nil
		},
	}
	remote.watchNext = func(ctx context.Context) error {
		if len(remote.answerRequests) != 1 {
			return errors.New("follow-up read started before batch completion")
		}
		if _, ok := ctx.Deadline(); ok {
			return errors.New("post-acceptance follow-up inherited a workflow deadline")
		}
		return nil
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
	if request.SessionID.String() != sessionID || request.StepID != questionCommandStepID() || entry.PromptID != "ask-1" {
		t.Fatalf("answer target = %+v", request)
	}
	if len(remote.watchRequests) != 1 ||
		remote.watchRequests[0].SessionID != request.SessionID ||
		remote.watchRequests[0].StepID != request.StepID ||
		remote.watchRequests[0].PromptID != entry.PromptID {
		t.Fatalf("watch request = %+v, answer request = %+v", remote.watchRequests, request)
	}
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 2 {
		t.Fatalf("selected option = %v, want 2", entry.QuestionAnswer.SelectedOptionNumber)
	}
	if entry.QuestionAnswer.Freeform == nil || *entry.QuestionAnswer.Freeform != "Because it is safer" {
		t.Fatalf("commentary = %v", entry.QuestionAnswer.Freeform)
	}
	if len(remote.listRequests) != 2 {
		t.Fatalf("list requests = %+v, want initial and post-answer reads", remote.listRequests)
	}
	requireQuestionWatch(t, remote, 1)
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
	entry := requireQuestionBatchEntry(t, remote.answerRequests[0])
	if entry.QuestionAnswer.SelectedOptionNumber == nil || *entry.QuestionAnswer.SelectedOptionNumber != 1 ||
		entry.QuestionAnswer.Freeform != nil {
		t.Fatalf("answer request = %+v", remote.answerRequests[0])
	}
}

func TestQuestionAnswerFailuresDoNotAuthorizeMutationOrDone(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	tests := []struct {
		name                string
		remote              *stubQuestionCommandRemote
		wantBatch, wantNext int
	}{
		{name: "watch", remote: &stubQuestionCommandRemote{watchErr: errors.New("unknown prompt")}},
		{name: "batch", remote: &stubQuestionCommandRemote{batchErr: errors.New("answer failed")}, wantBatch: 1},
		{name: "follow-up", remote: &stubQuestionCommandRemote{followUpErr: errors.New("watch failed")}, wantBatch: 1, wantNext: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := test.remote
			remote.listResponses = []serverapi.AskListPendingBySessionResponse{{Asks: []clientui.PendingAsk{
				pendingAsk(sessionID, "ask-1", "Choose?", "One"),
			}}}
			command, _ := questionCommandWithRemote(remote)
			var stdout, stderr bytes.Buffer

			exitCode := command.run([]string{"answer", "--session", sessionID, "--option", "1"}, &stdout, &stderr)

			if exitCode != 1 || stdout.Len() != 0 || stderr.Len() == 0 ||
				len(remote.answerRequests) != test.wantBatch ||
				len(remote.listRequests) != 1 {
				t.Fatalf("exit=%d stdout=%q stderr=%q batches=%d reads=%d", exitCode, stdout.String(), stderr.String(), len(remote.answerRequests), len(remote.listRequests))
			}
			requireQuestionWatch(t, remote, test.wantNext)
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
	command, openedSessions := questionCommandWithRemote(&stubQuestionCommandRemote{})
	exitCode := command.run(
		[]string{"answer", "--session", sessionID},
		io.Discard,
		io.Discard,
	)
	if exitCode != 2 || len(*openedSessions) != 0 {
		t.Fatalf("exit=%d opened=%v", exitCode, *openedSessions)
	}
}

func TestQuestionAnswerRejectsExplicitBlankCommentaryWithOption(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	command, openedSessions := questionCommandWithRemote(&stubQuestionCommandRemote{})
	exitCode := command.run(
		[]string{"answer", "--session", sessionID, "--option", "1", "--commentary", " \t "},
		io.Discard,
		io.Discard,
	)
	if exitCode != 2 || len(*openedSessions) != 0 {
		t.Fatalf("exit=%d opened=%v", exitCode, *openedSessions)
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
		stubQuestionCommandRemote: &stubQuestionCommandRemote{
			listResponses: []serverapi.AskListPendingBySessionResponse{{
				Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-1", "Continue?")},
			}},
		},
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{
			{Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-1", "Continue?", 1),
			}},
			{},
		},
	}
	command := questionCommandWithTaskRemote(remote)
	commentary := "Proceed"

	var stdout, stderr bytes.Buffer
	exitCode := command.answerResolvedTaskQuestion(
		questionTaskSelector(taskID),
		remote,
		taskID,
		nil,
		&commentary,
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
	if request.SessionID.String() != sessionID ||
		request.StepID != questionCommandStepID() ||
		entry.PromptID != "ask-1" ||
		entry.QuestionAnswer.Freeform == nil ||
		*entry.QuestionAnswer.Freeform != "Proceed" {
		t.Fatalf("answer request = %+v", request)
	}
	if len(remote.attentionRequests) != 2 {
		t.Fatalf("attention requests = %+v", remote.attentionRequests)
	}
	requireQuestionWatch(t, remote.stubQuestionCommandRemote, 1)
}

func TestQuestionByTaskApprovalReadsSuccessorQuestion(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	sessionID := uuid.NewString()
	approvalID := "approval-1"
	successorQuestion := "Next?"
	attention := taskQuestionAttention(taskID, sessionID, "Implementer", approvalID, "Allow access?", 1)
	attention.Question.Kind = serverapi.WorkflowAttentionQuestionKindApproval
	attention.Question.ApprovalDecisions = []clientui.ApprovalDecision{clientui.ApprovalDecisionAllowOnce}
	approval := clientui.PendingApproval{
		PromptID: clientui.PromptID(approvalID), SessionID: mustQuestionCommandSessionID(sessionID),
		StepID: questionCommandStepID(), Question: "Allow access?",
		Options: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce}},
	}
	remote := &stubQuestionTaskRemote{
		stubQuestionCommandRemote: &stubQuestionCommandRemote{
			listResponses: []serverapi.AskListPendingBySessionResponse{
				{},
				{Asks: []clientui.PendingAsk{pendingAsk(sessionID, "ask-next", successorQuestion)}},
			},
			approvalResponses: []serverapi.ApprovalListPendingBySessionResponse{
				{Approvals: []clientui.PendingApproval{approval}},
				{Approvals: []clientui.PendingApproval{approval}},
				{},
			},
		},
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{
			{Items: []serverapi.WorkflowAttentionItem{attention}},
			{Items: []serverapi.WorkflowAttentionItem{attention}},
			{Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-next", successorQuestion, 2),
			}},
		},
	}
	command := questionCommandWithTaskRemote(remote)
	selector := questionTaskSelector(taskID)

	var stdout, stderr bytes.Buffer
	if exitCode := command.showResolvedTaskQuestion(selector, remote, taskID, &stdout, &stderr); exitCode != 0 ||
		stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), clientui.ApprovalDecisionLabel(clientui.ApprovalDecisionAllowOnce)) {
		t.Fatalf("show exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	stdout.Reset()
	option := 1
	exitCode := command.answerResolvedTaskQuestion(
		selector,
		remote,
		taskID,
		&option,
		nil,
		&stdout,
		&stderr,
	)

	if exitCode != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.answerRequests) != 1 {
		t.Fatalf("approval requests = %+v", remote.answerRequests)
	}
	request := remote.answerRequests[0]
	if len(request.Entries) != 1 || request.Entries[0].ApprovalAnswer == nil {
		t.Fatalf("approval batch request = %+v", request)
	}
	entry := request.Entries[0]
	if request.SessionID.String() != sessionID || request.StepID != questionCommandStepID() ||
		entry.PromptID != clientui.PromptID(approvalID) ||
		entry.ApprovalAnswer.Decision != clientui.ApprovalDecisionAllowOnce ||
		entry.ApprovalAnswer.Commentary != nil {
		t.Fatalf("approval request = %+v", request)
	}
	if len(remote.attentionRequests) != 3 {
		t.Fatalf("attention requests = %+v", remote.attentionRequests)
	}
	requireQuestionWatch(t, remote.stubQuestionCommandRemote, 1)
}

func TestQuestionByTaskRejectsAmbiguousSessionsWithoutAnswering(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	taskID := "task-1"
	firstSessionID := uuid.NewString()
	secondSessionID := uuid.NewString()
	remote := &stubQuestionTaskRemote{
		stubQuestionCommandRemote: &stubQuestionCommandRemote{},
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
	command := questionCommandWithTaskRemote(remote)
	option := 1

	var stdout, stderr bytes.Buffer
	exitCode := command.answerResolvedTaskQuestion(
		questionTaskSelector(taskID),
		remote,
		taskID,
		&option,
		nil,
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
		stubQuestionCommandRemote: &stubQuestionCommandRemote{},
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{
			Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Current", "ask-1", "Continue?", 1),
			},
		}},
	}
	command := questionCommandWithTaskRemote(remote)

	exitCode := command.showResolvedTaskQuestion(
		questionTaskSelector(taskID),
		remote,
		taskID,
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
		stubQuestionCommandRemote: &stubQuestionCommandRemote{
			listResponses: []serverapi.AskListPendingBySessionResponse{{
				Asks: []clientui.PendingAsk{
					pendingAsk(sessionID, "ask-new", "Newer?"),
					pendingAsk(sessionID, "ask-old", "Older?"),
				},
			}},
		},
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: taskID},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{
			Items: []serverapi.WorkflowAttentionItem{
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-new", "Newer?", 2),
				taskQuestionAttention(taskID, sessionID, "Implementer", "ask-old", "Older?", 1),
			},
		}, {}},
	}
	command := questionCommandWithTaskRemote(remote)
	commentary := "Answer"

	exitCode := command.answerResolvedTaskQuestion(
		questionTaskSelector(taskID),
		remote,
		taskID,
		nil,
		&commentary,
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

func TestQuestionByTaskResolvesProjectScopedShortID(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &stubQuestionTaskRemote{
		stubQuestionCommandRemote: &stubQuestionCommandRemote{},
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "KENT-335"},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{}},
	}
	command := questionCommandWithTaskRemote(remote)
	selector := questionTaskSelector("KENT-335")
	resolvedTaskID, err := resolveWorkflowTaskID(
		context.Background(),
		config.App{WorkspaceRoot: "."},
		remote,
		remote,
		selector.ProjectRef,
		*selector.TaskRef,
	)
	if err != nil {
		t.Fatalf("resolve task: %v", err)
	}

	exitCode := command.showResolvedTaskQuestion(
		selector,
		remote,
		resolvedTaskID,
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
