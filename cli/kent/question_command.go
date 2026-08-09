package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

const questionCommandTimeout = 5 * time.Second

const (
	noPendingQuestionsText      = "No questions pending"
	noPendingQuestionAnswerText = "No pending questions at the moment for that session"
	questionAnswerDoneText      = "Done, session resumed"
	nextQuestionPrefix          = "Next question: "
	questionSuggestionsHeading  = "Suggestions:"
	recommendedSuggestionSuffix = " (recommended)"
)

type questionCommandRemote interface {
	ListPendingAsksBySession(context.Context, serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error)
	ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error)
	AnswerPromptBatch(context.Context, serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error)
	SubscribeFollowUp(context.Context, serverapi.PromptFollowUpWatchRequest) (serverapi.PromptFollowUpSubscription, error)
	Close() error
}

type questionCommandRemoteOpener func(
	context.Context,
	string,
) (questionCommandRemote, error)

type questionCommand struct {
	openRemote questionCommandRemoteOpener
}

type questionCommandSelector struct {
	SessionID  *runtimeids.SessionID
	TaskRef    *string
	ProjectRef string
	Command    string
}

type taskQuestionSessionCandidate struct {
	SessionID   runtimeids.SessionID
	SessionName *string
	Questions   []questionCommandPendingQuestion
}

type questionCommandPendingQuestion struct {
	PromptID               clientui.PromptID
	SessionID              runtimeids.SessionID
	StepID                 runtimeids.StepID
	Kind                   serverapi.WorkflowAttentionQuestionKind
	Question               string
	Suggestions            []string
	RecommendedOptionIndex *int
	Approval               *clientui.PendingApproval
}

func questionSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return questionCommand{openRemote: openQuestionCommandRemote}.run(args, stdout, stderr)
}

func (c questionCommand) run(args []string, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) > 0 {
		switch args[0] {
		case "answer":
			return c.answerSubcommand(args[1:], stdout, stderr)
		case "--help", "-h":
			questionUsage.write(newCommandFlagSet(config.Command+" question", stderr, questionUsage))
			return 0
		}
	}
	return c.showSubcommand(args, stdout, stderr)
}

func (c questionCommand) showSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" question", stderr, questionShowUsage)
	var sessionFlag *string
	registerOptionalStringFlag(fs, "session", "session to inspect", &sessionFlag)
	var taskFlag *string
	registerOptionalStringFlag(fs, "task", "workflow task whose pending question to inspect", &taskFlag)
	projectFlag := fs.String("project", ".", "project ID or attached workspace path used to resolve a task short ID")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "question does not accept positional arguments")
		return 2
	}
	selector, err := resolveQuestionCommandSelector(sessionFlag, taskFlag, *projectFlag, config.Command+" question")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if selector.SessionID != nil {
		return c.showSessionQuestion(*selector.SessionID, stdout, stderr)
	}
	return c.showTaskQuestion(selector, stdout, stderr)
}

func (c questionCommand) showSessionQuestion(sessionID runtimeids.SessionID, stdout io.Writer, stderr io.Writer) int {
	return c.withRemote(stderr, sessionID, func(remote questionCommandRemote) int {
		question, ok, err := listPendingSessionPrompt(context.Background(), remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, noPendingQuestionsText)
			return 0
		}
		writePendingQuestion(stdout, question, false)
		return 0
	})
}

func (c questionCommand) answerSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" question answer", stderr, questionAnswerUsage)
	var sessionFlag *string
	registerOptionalStringFlag(fs, "session", "session whose pending question to answer", &sessionFlag)
	var taskFlag *string
	registerOptionalStringFlag(fs, "task", "workflow task whose pending question to answer", &taskFlag)
	projectFlag := fs.String("project", ".", "project ID or attached workspace path used to resolve a task short ID")
	var option *int
	registerOptionalPositiveIntFlag(fs, "option", "one-based suggestion number", &option)
	var commentary *string
	registerOptionalStringFlag(fs, "commentary", "freeform answer or optional suggestion commentary", &commentary)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "question answer does not accept positional arguments")
		return 2
	}
	if commentary != nil {
		trimmedCommentary := strings.TrimSpace(*commentary)
		if trimmedCommentary == "" {
			fmt.Fprintln(stderr, "question answer requires non-blank --commentary when provided")
			return 2
		}
		commentary = &trimmedCommentary
	}
	if option == nil && commentary == nil {
		fmt.Fprintln(stderr, "question answer requires --option or non-blank --commentary")
		return 2
	}
	selector, err := resolveQuestionCommandSelector(sessionFlag, taskFlag, *projectFlag, config.Command+" question answer")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if selector.SessionID != nil {
		return c.answerSessionQuestion(*selector.SessionID, option, commentary, stdout, stderr)
	}
	return c.answerTaskQuestion(selector, option, commentary, stdout, stderr)
}

func (c questionCommand) answerSessionQuestion(
	sessionID runtimeids.SessionID,
	option *int,
	commentary *string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	return c.withRemote(stderr, sessionID, func(remote questionCommandRemote) int {
		question, ok, err := listPendingSessionPrompt(context.Background(), remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stderr, noPendingQuestionAnswerText)
			return 1
		}
		return answerQuestionThroughBatch(
			remote,
			question,
			option,
			commentary,
			func(ctx context.Context) (questionCommandPendingQuestion, bool, error) {
				return listPendingSessionPrompt(ctx, remote, sessionID)
			},
			stdout,
			stderr,
		)
	})
}

func (c questionCommand) showTaskQuestion(selector questionCommandSelector, stdout io.Writer, stderr io.Writer) int {
	return withQuestionTaskRemote(selector, stderr, func(remote workflowCommandRemote, taskID string) int {
		candidates, err := listTaskQuestionCandidates(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		candidate, exitCode := selectTaskQuestionCandidate(selector, candidates, stderr)
		if exitCode != 0 {
			return exitCode
		}
		if candidate == nil {
			fmt.Fprintln(stdout, noPendingQuestionsText)
			return 0
		}
		expected := candidate.Questions[0]
		return c.withRemote(stderr, candidate.SessionID, func(promptRemote questionCommandRemote) int {
			question, ok, err := readPendingSessionPromptByKey(context.Background(), promptRemote, expected)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if !ok {
				fmt.Fprintln(stdout, noPendingQuestionsText)
				return 0
			}
			writePendingQuestion(stdout, question, false)
			return 0
		})
	})
}

func (c questionCommand) answerTaskQuestion(
	selector questionCommandSelector,
	option *int,
	commentary *string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	return withQuestionTaskRemote(selector, stderr, func(remote workflowCommandRemote, taskID string) int {
		candidates, err := listTaskQuestionCandidates(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		candidate, exitCode := selectTaskQuestionCandidate(selector, candidates, stderr)
		if exitCode != 0 {
			return exitCode
		}
		if candidate == nil {
			fmt.Fprintln(stderr, noPendingQuestionAnswerText)
			return 1
		}
		expected := candidate.Questions[0]
		return c.withRemote(stderr, candidate.SessionID, func(promptRemote questionCommandRemote) int {
			question, ok, err := readPendingSessionPromptByKey(context.Background(), promptRemote, expected)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if !ok {
				fmt.Fprintln(stderr, noPendingQuestionAnswerText)
				return 1
			}
			return answerQuestionThroughBatch(
				promptRemote,
				question,
				option,
				commentary,
				func(ctx context.Context) (questionCommandPendingQuestion, bool, error) {
					return readTaskQuestionFollowUp(ctx, remote, promptRemote, taskID, candidate.SessionID)
				},
				stdout,
				stderr,
			)
		})
	})
}

func questionAnswerContext() (context.Context, context.CancelFunc) {
	signalCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	ctx, cancel := context.WithTimeout(signalCtx, questionCommandTimeout)
	return ctx, func() {
		cancel()
		stopSignal()
	}
}

type questionAnswerUsageError struct {
	message string
}

func (e *questionAnswerUsageError) Error() string {
	return e.message
}

func isQuestionAnswerUsageError(err error) bool {
	var usageErr *questionAnswerUsageError
	return errors.As(err, &usageErr)
}

func answerQuestionThroughBatch(
	remote questionCommandRemote,
	question questionCommandPendingQuestion,
	option *int,
	commentary *string,
	followUp func(context.Context) (questionCommandPendingQuestion, bool, error),
	stdout io.Writer,
	stderr io.Writer,
) int {
	answer, err := questionBatchAnswer(question, option, commentary)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if isQuestionAnswerUsageError(err) {
			return 2
		}
		return 1
	}
	entry, err := serverapi.PromptAnswerBatchEntryFrom(question.PromptID, answer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	answerCtx, stopAnswer := questionAnswerContext()
	defer stopAnswer()
	watch, err := remote.SubscribeFollowUp(answerCtx, serverapi.PromptFollowUpWatchRequest{
		SessionID: question.SessionID,
		StepID:    question.StepID,
		PromptID:  question.PromptID,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = watch.Close() }()
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: question.SessionID,
		StepID:    question.StepID,
		Entries:   []serverapi.PromptAnswerBatchEntry{entry},
	}
	response, err := remote.AnswerPromptBatch(answerCtx, request)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(request, response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := watch.Next(answerCtx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	next, ok, err := followUp(answerCtx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if ok {
		writePendingQuestion(stdout, next, true)
	} else {
		fmt.Fprintln(stdout, questionAnswerDoneText)
	}
	return 0
}

func questionBatchAnswer(
	question questionCommandPendingQuestion,
	option *int,
	commentary *string,
) (serverapi.PromptAnswer, error) {
	if question.Kind == serverapi.WorkflowAttentionQuestionKindApproval {
		if question.Approval == nil {
			return serverapi.PromptAnswer{}, errors.New("pending Approval has no authoritative options")
		}
		if option == nil {
			return serverapi.PromptAnswer{}, &questionAnswerUsageError{message: "question answer requires --option for an access request"}
		}
		if *option < 1 || *option > len(question.Approval.Options) {
			return serverapi.PromptAnswer{}, &questionAnswerUsageError{message: "question answer option is out of range"}
		}
		return serverapi.ApprovalPromptAnswer(serverapi.PromptApprovalAnswer{
			Decision:   question.Approval.Options[*option-1].Decision,
			Commentary: optionalQuestionCommentary(commentary),
		}), nil
	}
	if option != nil && (*option < 1 || *option > len(question.Suggestions)) {
		return serverapi.PromptAnswer{}, &questionAnswerUsageError{message: "question answer option is out of range"}
	}
	return serverapi.QuestionPromptAnswer(serverapi.PromptQuestionAnswer{
		SelectedOptionNumber: option,
		Freeform:             optionalQuestionCommentary(commentary),
	}), nil
}

func readTaskQuestionFollowUp(
	ctx context.Context,
	remote workflowCommandRemote,
	promptRemote questionCommandRemote,
	taskID string,
	sessionID runtimeids.SessionID,
) (questionCommandPendingQuestion, bool, error) {
	refreshed, err := listTaskQuestionCandidates(ctx, remote, taskID)
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	for _, next := range refreshed {
		if next.SessionID == sessionID && len(next.Questions) > 0 {
			return readPendingSessionPromptByKey(ctx, promptRemote, next.Questions[0])
		}
	}
	return questionCommandPendingQuestion{}, false, nil
}

func listPendingSessionQuestions(
	ctx context.Context,
	remote questionCommandRemote,
	sessionID runtimeids.SessionID,
) (serverapi.AskListPendingBySessionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, questionCommandTimeout)
	defer cancel()
	return remote.ListPendingAsksBySession(ctx, serverapi.AskListPendingBySessionRequest{
		SessionID: sessionID.String(),
	})
}

func listPendingSessionPrompt(
	ctx context.Context,
	remote questionCommandRemote,
	sessionID runtimeids.SessionID,
) (questionCommandPendingQuestion, bool, error) {
	asks, err := listPendingSessionQuestions(ctx, remote, sessionID)
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	var approvals serverapi.ApprovalListPendingBySessionResponse
	rpcCtx, cancel := context.WithTimeout(ctx, questionCommandTimeout)
	approvals, err = remote.ListPendingApprovalsBySession(rpcCtx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID.String()})
	cancel()
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	prompt, ok := serverapi.FirstPendingPromptObservation(asks.Asks, approvals.Approvals)
	if !ok {
		return questionCommandPendingQuestion{}, false, nil
	}
	return pendingQuestionFromObservation(prompt.Question)
}

func readPendingSessionPromptByKey(
	ctx context.Context,
	remote questionCommandRemote,
	expected questionCommandPendingQuestion,
) (questionCommandPendingQuestion, bool, error) {
	asks, err := listPendingSessionQuestions(ctx, remote, expected.SessionID)
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	rpcCtx, cancel := context.WithTimeout(ctx, questionCommandTimeout)
	approvals, err := remote.ListPendingApprovalsBySession(
		rpcCtx,
		serverapi.ApprovalListPendingBySessionRequest{SessionID: expected.SessionID.String()},
	)
	cancel()
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	if expected.Kind == serverapi.WorkflowAttentionQuestionKindOrdinary {
		for _, ask := range asks.Asks {
			if ask.SessionID == expected.SessionID &&
				ask.StepID == expected.StepID &&
				ask.PromptID == expected.PromptID {
				question, err := pendingSessionQuestion(ask)
				return question, err == nil, err
			}
		}
		return questionCommandPendingQuestion{}, false, nil
	}
	for _, approval := range approvals.Approvals {
		if approval.SessionID == expected.SessionID &&
			approval.StepID == expected.StepID &&
			approval.PromptID == expected.PromptID {
			return pendingSessionApproval(approval)
		}
	}
	return questionCommandPendingQuestion{}, false, nil
}

func pendingQuestionFromObservation(
	question serverapi.ObservationQuestion,
) (questionCommandPendingQuestion, bool, error) {
	if question.Ask != nil {
		pending, err := pendingSessionQuestion(*question.Ask)
		return pending, err == nil, err
	}
	if question.Approval != nil {
		return pendingSessionApproval(*question.Approval)
	}
	return questionCommandPendingQuestion{}, false, errors.New("pending prompt has no Question or Approval")
}

func optionalQuestionCommentary(commentary *string) *string {
	if commentary == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*commentary)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func withQuestionTaskRemote(
	selector questionCommandSelector,
	stderr io.Writer,
	run func(workflowCommandRemote, string) int,
) int {
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		if selector.TaskRef == nil {
			fmt.Fprintln(stderr, "question task selector is required")
			return 2
		}
		taskID, err := resolveWorkflowTaskID(
			context.Background(),
			cfg,
			remote,
			selector.ProjectRef,
			*selector.TaskRef,
		)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return run(remote, taskID)
	})
}

func listTaskQuestionCandidates(
	ctx context.Context,
	remote workflowCommandRemote,
	taskID string,
) ([]taskQuestionSessionCandidate, error) {
	rpcCtx, cancel := context.WithTimeout(ctx, questionCommandTimeout)
	defer cancel()
	response, err := remote.ListWorkflowTaskAttention(rpcCtx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: taskID,
	})
	if err != nil {
		return nil, err
	}
	return taskQuestionCandidatesWithRemote(ctx, remote, response.Items)
}

func taskQuestionCandidates(items []serverapi.WorkflowAttentionItem) ([]taskQuestionSessionCandidate, error) {
	return taskQuestionCandidatesWithRemote(context.Background(), nil, items)
}

func taskQuestionCandidatesWithRemote(_ context.Context, _ workflowCommandRemote, items []serverapi.WorkflowAttentionItem) ([]taskQuestionSessionCandidate, error) {
	questions := make([]serverapi.WorkflowAttentionItem, 0, len(items))
	for _, item := range items {
		if item.Kind != string(serverapi.WorkflowTaskAttentionKindQuestion) {
			continue
		}
		if item.Question == nil {
			return nil, fmt.Errorf("pending question %q has no typed prompt", item.ID)
		}
		if item.Question.Kind != serverapi.WorkflowAttentionQuestionKindOrdinary && item.Question.Kind != serverapi.WorkflowAttentionQuestionKindApproval {
			continue
		}
		questions = append(questions, item)
	}
	sort.Slice(questions, func(i, j int) bool {
		if questions[i].OccurredAtUnixMs != questions[j].OccurredAtUnixMs {
			return questions[i].OccurredAtUnixMs < questions[j].OccurredAtUnixMs
		}
		return questions[i].ID < questions[j].ID
	})
	candidates := make([]taskQuestionSessionCandidate, 0)
	candidateBySession := make(map[runtimeids.SessionID]int)
	for _, item := range questions {
		prompt := *item.Question
		sessionID := prompt.SessionID
		sessionName, err := normalizedOptionalQuestionSessionName(item.SessionName)
		if err != nil {
			return nil, fmt.Errorf("pending question %q session name: %w", item.ID, err)
		}
		question := questionCommandPendingQuestion{
			PromptID:  prompt.PromptID,
			SessionID: prompt.SessionID,
			StepID:    prompt.StepID,
			Kind:      prompt.Kind,
		}
		if prompt.Kind == serverapi.WorkflowAttentionQuestionKindOrdinary {
			if item.Message == nil {
				return nil, fmt.Errorf("pending question %q has no content", item.ID)
			}
			question.Question = *item.Message
			question.Suggestions = append([]string(nil), prompt.Suggestions...)
			question.RecommendedOptionIndex = textutil.Pointer(prompt.RecommendedOptionIndex)
		} else {
			if item.Message == nil || prompt.SessionID.IsZero() || prompt.StepID.IsZero() ||
				prompt.PromptID.Validate() != nil || len(prompt.ApprovalDecisions) == 0 {
				continue
			}
			question.Question = *item.Message
		}
		index, exists := candidateBySession[sessionID]
		if !exists {
			index = len(candidates)
			candidateBySession[sessionID] = index
			candidates = append(candidates, taskQuestionSessionCandidate{
				SessionID:   sessionID,
				SessionName: sessionName,
			})
		} else if !textutil.EqualOptional(candidates[index].SessionName, sessionName) {
			return nil, fmt.Errorf("pending questions for session %s disagree on the session name", sessionID)
		}
		candidates[index].Questions = append(candidates[index].Questions, question)
	}
	return candidates, nil
}

func normalizedOptionalQuestionSessionName(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, errors.New("session name must be non-blank when present")
	}
	return &trimmed, nil
}

func selectTaskQuestionCandidate(
	selector questionCommandSelector,
	candidates []taskQuestionSessionCandidate,
	stderr io.Writer,
) (*taskQuestionSessionCandidate, int) {
	if len(candidates) == 0 {
		return nil, 0
	}
	if len(candidates) > 1 {
		writeTaskQuestionAmbiguity(stderr, selector, candidates)
		return nil, 1
	}
	if err := rejectSelfTarget(candidates[0].SessionID, selector.Command); err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 2
	}
	return &candidates[0], 0
}

func writeTaskQuestionAmbiguity(
	stderr io.Writer,
	selector questionCommandSelector,
	candidates []taskQuestionSessionCandidate,
) {
	taskRef := ""
	if selector.TaskRef != nil {
		taskRef = *selector.TaskRef
	}
	fmt.Fprintf(stderr, "Multiple sessions have pending questions for task %s:\n", taskRef)
	for _, candidate := range candidates {
		name := "Unnamed session"
		if candidate.SessionName != nil {
			name = *candidate.SessionName
		}
		fmt.Fprintf(stderr, "- %s (%s)\n", name, candidate.SessionID)
	}
}

func registerOptionalPositiveIntFlag(fs *flag.FlagSet, name string, usage string, target **int) {
	fs.Func(name, usage, func(raw string) error {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("--%s must be a positive integer", name)
		}
		*target = &parsed
		return nil
	})
}

func registerOptionalStringFlag(fs *flag.FlagSet, name string, usage string, target **string) {
	fs.Func(name, usage, func(raw string) error {
		*target = &raw
		return nil
	})
}

func resolveQuestionCommandSelector(
	rawSessionID *string,
	rawTaskRef *string,
	projectRef string,
	command string,
) (questionCommandSelector, error) {
	sessionPresent := rawSessionID != nil
	taskPresent := rawTaskRef != nil
	if sessionPresent == taskPresent {
		return questionCommandSelector{}, errors.New("question command requires exactly one of --session or --task")
	}
	selector := questionCommandSelector{
		ProjectRef: strings.TrimSpace(projectRef),
		Command:    strings.TrimSpace(command),
	}
	if selector.ProjectRef == "" {
		return questionCommandSelector{}, errors.New("--project requires a non-blank project selector")
	}
	if sessionPresent {
		sessionID, err := parseCLILiveSessionID(*rawSessionID)
		if err != nil {
			return questionCommandSelector{}, err
		}
		if err := rejectSelfTarget(sessionID, selector.Command); err != nil {
			return questionCommandSelector{}, err
		}
		selector.SessionID = &sessionID
		return selector, nil
	}
	taskRef := strings.TrimSpace(*rawTaskRef)
	if taskRef == "" {
		return questionCommandSelector{}, errors.New("--task requires a non-blank task selector")
	}
	selector.TaskRef = &taskRef
	return selector, nil
}

func (c questionCommand) withRemote(
	stderr io.Writer,
	sessionID runtimeids.SessionID,
	run func(questionCommandRemote) int,
) int {
	remote, err := c.openRemote(context.Background(), sessionID.String())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	return run(remote)
}

func openQuestionCommandRemote(ctx context.Context, sessionID string) (questionCommandRemote, error) {
	configRoot, err := nearestCommandConfigRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(configRoot, config.LoadOptions{})
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, questionCommandTimeout)
	defer cancel()
	remote, err := client.DialConfiguredRemoteForSession(dialCtx, cfg, sessionID)
	if err != nil {
		return nil, err
	}
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		_ = remote.Close()
		return nil, err
	}
	return remote, nil
}

func pendingSessionQuestion(ask clientui.PendingAsk) (questionCommandPendingQuestion, error) {
	if err := ask.PromptID.Validate(); err != nil {
		return questionCommandPendingQuestion{}, err
	}
	if ask.SessionID.IsZero() || ask.StepID.IsZero() {
		return questionCommandPendingQuestion{}, errors.New("pending question has incomplete batch identity")
	}
	if ask.RecommendedOptionIndex != nil &&
		(*ask.RecommendedOptionIndex < 1 || *ask.RecommendedOptionIndex > len(ask.Suggestions)) {
		return questionCommandPendingQuestion{}, fmt.Errorf(
			"pending question %q recommended option index %d is outside suggestions 1..%d",
			ask.PromptID,
			*ask.RecommendedOptionIndex,
			len(ask.Suggestions),
		)
	}
	return questionCommandPendingQuestion{
		PromptID:               ask.PromptID,
		SessionID:              ask.SessionID,
		StepID:                 ask.StepID,
		Kind:                   serverapi.WorkflowAttentionQuestionKindOrdinary,
		Question:               ask.Question,
		Suggestions:            append([]string(nil), ask.Suggestions...),
		RecommendedOptionIndex: textutil.Pointer(ask.RecommendedOptionIndex),
	}, nil
}

func pendingSessionApproval(
	approval clientui.PendingApproval,
) (questionCommandPendingQuestion, bool, error) {
	if err := (serverapi.ObservationQuestion{Approval: &approval}).Validate(); err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	cloned := approval
	cloned.Options = append([]clientui.ApprovalOption(nil), approval.Options...)
	return questionCommandPendingQuestion{
		PromptID:  approval.PromptID,
		SessionID: approval.SessionID,
		StepID:    approval.StepID,
		Kind:      serverapi.WorkflowAttentionQuestionKindApproval,
		Question:  approval.Question,
		Approval:  &cloned,
	}, true, nil
}

func writePendingQuestion(stdout io.Writer, ask questionCommandPendingQuestion, next bool) {
	if next {
		fmt.Fprint(stdout, nextQuestionPrefix)
	}
	question := serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{
		PromptID: ask.PromptID, SessionID: ask.SessionID, StepID: ask.StepID,
		Question: ask.Question, Suggestions: ask.Suggestions,
		RecommendedOptionIndex: ask.RecommendedOptionIndex,
	}}
	if ask.Approval != nil {
		question = serverapi.ObservationQuestion{Approval: ask.Approval}
	}
	writeObservedQuestion(stdout, question, "")
}
