package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"

	"github.com/google/uuid"
)

type stubQuestionCommandRemote struct {
	listResponses  []serverapi.AskListPendingBySessionResponse
	listRequests   []serverapi.AskListPendingBySessionRequest
	answerRequests []serverapi.AskAnswerRequest
}

type stubQuestionTaskRemote struct {
	apicontract.WorkflowService
	task               serverapi.WorkflowTaskDetail
	taskRequests       []serverapi.WorkflowTaskGetRequest
	attentionResponses []serverapi.WorkflowTaskAttentionListResponse
	attentionRequests  []serverapi.WorkflowTaskAttentionListRequest
	answerRequests     []serverapi.WorkflowTaskQuestionAnswerRequest
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

func (r *stubQuestionTaskRemote) AnswerWorkflowTaskQuestion(
	_ context.Context,
	req serverapi.WorkflowTaskQuestionAnswerRequest,
) error {
	r.answerRequests = append(r.answerRequests, req)
	return nil
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

func (r *stubQuestionCommandRemote) AnswerAsk(_ context.Context, req serverapi.AskAnswerRequest) error {
	r.answerRequests = append(r.answerRequests, req)
	return nil
}

func (r *stubQuestionCommandRemote) Close() error {
	return nil
}

func installQuestionCommandRemote(t *testing.T, remote questionCommandRemote) *[]string {
	t.Helper()
	openedSessions := []string{}
	previous := questionCommandRemoteOpener
	questionCommandRemoteOpener = func(_ context.Context, sessionID string) (questionCommandRemote, error) {
		openedSessions = append(openedSessions, sessionID)
		return remote, nil
	}
	t.Cleanup(func() {
		questionCommandRemoteOpener = previous
	})
	return &openedSessions
}

func installQuestionTaskRemote(t *testing.T, remote workflowCommandRemote) {
	t.Helper()
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{WorkspaceRoot: "."}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})
}

func pendingAsk(sessionID, askID, question string, suggestions ...string) clientui.PendingAsk {
	return clientui.PendingAsk{
		SessionID:   sessionID,
		AskID:       askID,
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
		SessionID:   &sessionID,
		SessionName: &sessionName,
		QuestionID:  &askID,
		Message:     &question,
		Question: &serverapi.WorkflowAttentionQuestionPrompt{
			Kind: serverapi.WorkflowAttentionQuestionKindOrdinary,
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
	openedSessions := installQuestionCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := questionSubcommand([]string{"--session", sessionID}, &stdout, &stderr)

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
		AskID:                  "ask-present",
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
		AskID:       "ask-absent",
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
		AskID:                  "ask-invalid",
		Suggestions:            []string{"one"},
		RecommendedOptionIndex: &invalid,
	}); err == nil {
		t.Fatal("accepted present zero recommendation")
	}
}

func TestQuestionsAliasDispatchesQuestionCommand(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{{}},
	}
	installQuestionCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := rootCommand([]string{"questions", "--session", sessionID}, bytes.NewReader(nil), &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.listRequests) != 1 {
		t.Fatalf("list requests = %+v", remote.listRequests)
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
	installQuestionCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := questionSubcommand([]string{
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
	if request.SessionID != sessionID || request.AskID != "ask-1" {
		t.Fatalf("answer target = %+v", request)
	}
	if request.SelectedOptionNumber == nil || *request.SelectedOptionNumber != 2 {
		t.Fatalf("selected option = %v, want 2", request.SelectedOptionNumber)
	}
	if request.FreeformAnswer != "Because it is safer" {
		t.Fatalf("commentary = %q", request.FreeformAnswer)
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
	installQuestionCommandRemote(t, remote)

	exitCode := questionSubcommand(
		[]string{"answer", "--session", sessionID, "--commentary", "Freeform answer"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.answerRequests) != 1 {
		t.Fatalf("exit=%d answer requests=%+v", exitCode, remote.answerRequests)
	}
	if remote.answerRequests[0].SelectedOptionNumber != nil ||
		remote.answerRequests[0].FreeformAnswer != "Freeform answer" {
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
	installQuestionCommandRemote(t, remote)

	exitCode := questionSubcommand(
		[]string{"answer", "--session", sessionID, "--option", "1"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.answerRequests) != 1 {
		t.Fatalf("exit=%d answer requests=%+v", exitCode, remote.answerRequests)
	}
	request := remote.answerRequests[0]
	if request.SelectedOptionNumber == nil || *request.SelectedOptionNumber != 1 ||
		request.FreeformAnswer != "" {
		t.Fatalf("answer request = %+v", request)
	}
}

func TestQuestionAnswerWithoutPendingQuestionFailsWithoutSubmitting(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	sessionID := uuid.NewString()
	remote := &stubQuestionCommandRemote{
		listResponses: []serverapi.AskListPendingBySessionResponse{{}},
	}
	installQuestionCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := questionSubcommand(
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
	openedSessions := installQuestionCommandRemote(t, remote)

	exitCode := questionSubcommand(
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
	openedSessions := installQuestionCommandRemote(t, remote)

	exitCode := questionSubcommand(
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
	openedSessions := installQuestionCommandRemote(t, remote)

	exitCode := questionSubcommand(
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
	}
	installQuestionTaskRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := questionSubcommand(
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
	if request.TaskID != taskID || request.AskID != "ask-1" || request.FreeformAnswer != "Proceed" {
		t.Fatalf("answer request = %+v", request)
	}
	if len(remote.attentionRequests) != 2 {
		t.Fatalf("attention requests = %+v", remote.attentionRequests)
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
	installQuestionTaskRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := questionSubcommand(
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
	installQuestionTaskRemote(t, remote)

	exitCode := questionSubcommand(
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
	}
	installQuestionTaskRemote(t, remote)

	exitCode := questionSubcommand(
		[]string{"answer", "--task", taskID, "--commentary", "Answer"},
		io.Discard,
		io.Discard,
	)

	if exitCode != 0 || len(remote.answerRequests) != 1 {
		t.Fatalf("exit=%d answer requests=%+v", exitCode, remote.answerRequests)
	}
	if remote.answerRequests[0].AskID != "ask-old" {
		t.Fatalf("answered ask = %q, want oldest", remote.answerRequests[0].AskID)
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
	withRecommendation.RecommendedOptionIndex = &recommendedOptionIndex

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

func TestQuestionByTaskResolvesProjectScopedShortID(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &stubQuestionTaskRemote{
		task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "KENT-335"},
		},
		attentionResponses: []serverapi.WorkflowTaskAttentionListResponse{{}},
	}
	installQuestionTaskRemote(t, remote)

	exitCode := questionSubcommand(
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
