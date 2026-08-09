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
	AnswerAsk(context.Context, serverapi.AskAnswerRequest) error
	ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error)
	AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error
	Close() error
}

type questionApprovalRemote interface {
	ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error)
	AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error
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
	AskID                  string
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
	return showTaskQuestion(selector, stdout, stderr)
}

func (c questionCommand) showSessionQuestion(sessionID runtimeids.SessionID, stdout io.Writer, stderr io.Writer) int {
	return c.withRemote(stderr, sessionID, func(remote questionCommandRemote) int {
		question, ok, err := listPendingSessionPrompt(remote, sessionID)
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
	return answerTaskQuestion(selector, option, commentary, stdout, stderr)
}

func (c questionCommand) answerSessionQuestion(
	sessionID runtimeids.SessionID,
	option *int,
	commentary *string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	return c.withRemote(stderr, sessionID, func(remote questionCommandRemote) int {
		question, ok, err := listPendingSessionPrompt(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stderr, noPendingQuestionAnswerText)
			return 1
		}
		if question.Approval != nil {
			if err := answerApprovalQuestion(remote, sessionID, question.Approval, option, commentary); err != nil {
				fmt.Fprintln(stderr, err)
				if isQuestionAnswerUsageError(err) {
					return 2
				}
				return 1
			}
			next, nextOK, err := listPendingSessionPrompt(remote, sessionID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if nextOK {
				writePendingQuestion(stdout, next, true)
			} else {
				fmt.Fprintln(stdout, questionAnswerDoneText)
			}
			return 0
		}
		request := serverapi.AskAnswerRequest{
			SessionID:            sessionID.String(),
			AskID:                question.AskID,
			SelectedOptionNumber: option,
		}
		if commentary != nil {
			request.FreeformAnswer = *commentary
		}
		answerCtx, stopAnswer := questionAnswerContext()
		defer stopAnswer()
		if err := remote.AnswerAsk(answerCtx, request); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		next, nextOK, err := listPendingSessionPrompt(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !nextOK {
			fmt.Fprintln(stdout, questionAnswerDoneText)
			return 0
		}
		writePendingQuestion(stdout, next, true)
		return 0
	})
}

func showTaskQuestion(selector questionCommandSelector, stdout io.Writer, stderr io.Writer) int {
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
		writePendingQuestion(stdout, candidate.Questions[0], false)
		return 0
	})
}

func answerTaskQuestion(
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
		question := candidate.Questions[0]
		if question.Approval != nil {
			approvalRemote, ok := remote.(questionApprovalRemote)
			if !ok {
				fmt.Fprintln(stderr, "approval answer is unavailable")
				return 1
			}
			if err := answerApprovalQuestion(approvalRemote, candidate.SessionID, question.Approval, option, commentary); err != nil {
				fmt.Fprintln(stderr, err)
				if isQuestionAnswerUsageError(err) {
					return 2
				}
				return 1
			}
			return writeTaskQuestionFollowUp(remote, taskID, candidate.SessionID, stdout, stderr)
		}
		request := serverapi.WorkflowTaskQuestionAnswerRequest{
			TaskID:               taskID,
			AskID:                question.AskID,
			SelectedOptionNumber: option,
		}
		if commentary != nil {
			request.FreeformAnswer = *commentary
		}
		answerCtx, stopAnswer := questionAnswerContext()
		defer stopAnswer()
		err = remote.AnswerWorkflowTaskQuestion(answerCtx, request)
		if err != nil {
			if errors.Is(err, serverapi.ErrWorkflowTaskQuestionSelectorAmbiguous) {
				refreshed, refreshErr := listTaskQuestionCandidates(context.Background(), remote, taskID)
				if refreshErr == nil && len(refreshed) > 1 {
					writeTaskQuestionAmbiguity(stderr, selector, refreshed)
					return 1
				}
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeTaskQuestionFollowUp(remote, taskID, candidate.SessionID, stdout, stderr)
	})
}

func questionAnswerContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
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

func answerApprovalQuestion(
	approvalRemote questionApprovalRemote,
	sessionID runtimeids.SessionID,
	approval *clientui.PendingApproval,
	option *int,
	commentary *string,
) error {
	if option == nil {
		return &questionAnswerUsageError{message: "question answer requires --option for an access request"}
	}
	if *option < 1 || *option > len(approval.Options) {
		return &questionAnswerUsageError{message: "question answer option is out of range"}
	}
	answerCtx, stopAnswer := questionAnswerContext()
	defer stopAnswer()
	return approvalRemote.AnswerApproval(answerCtx, serverapi.ApprovalAnswerRequest{
		SessionID:  sessionID.String(),
		ApprovalID: approval.ApprovalID,
		Decision:   approval.Options[*option-1].Decision,
		Commentary: optionalQuestionCommentary(commentary),
	})
}

func writeTaskQuestionFollowUp(
	remote workflowCommandRemote,
	taskID string,
	sessionID runtimeids.SessionID,
	stdout io.Writer,
	stderr io.Writer,
) int {
	refreshed, err := listTaskQuestionCandidates(context.Background(), remote, taskID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, next := range refreshed {
		if next.SessionID == sessionID && len(next.Questions) > 0 {
			writePendingQuestion(stdout, next.Questions[0], true)
			return 0
		}
	}
	fmt.Fprintln(stdout, questionAnswerDoneText)
	return 0
}

func listPendingSessionQuestions(
	remote questionCommandRemote,
	sessionID runtimeids.SessionID,
) (serverapi.AskListPendingBySessionResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), questionCommandTimeout)
	defer cancel()
	return remote.ListPendingAsksBySession(ctx, serverapi.AskListPendingBySessionRequest{
		SessionID: sessionID.String(),
	})
}

func listPendingSessionPrompt(remote questionCommandRemote, sessionID runtimeids.SessionID) (questionCommandPendingQuestion, bool, error) {
	asks, err := listPendingSessionQuestions(remote, sessionID)
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	var approvals serverapi.ApprovalListPendingBySessionResponse
	ctx, cancel := context.WithTimeout(context.Background(), questionCommandTimeout)
	approvals, err = remote.ListPendingApprovalsBySession(ctx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID.String()})
	cancel()
	if err != nil {
		return questionCommandPendingQuestion{}, false, err
	}
	prompt, ok := serverapi.FirstPendingPromptObservation(asks.Asks, approvals.Approvals)
	if !ok {
		return questionCommandPendingQuestion{}, false, nil
	}
	question := questionCommandPendingQuestion{
		AskID:    prompt.ID,
		Approval: prompt.Question.Approval,
	}
	if prompt.Question.Ask != nil {
		question.Question = prompt.Question.Ask.Question
		question.Suggestions = append([]string(nil), prompt.Question.Ask.Suggestions...)
		question.RecommendedOptionIndex = prompt.Question.Ask.RecommendedOptionIndex
	} else if prompt.Question.Approval != nil {
		question.Question = prompt.Question.Approval.Question
	}
	return question, true, nil
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

func taskQuestionCandidatesWithRemote(ctx context.Context, remote workflowCommandRemote, items []serverapi.WorkflowAttentionItem) ([]taskQuestionSessionCandidate, error) {
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
	approvalCache := make(map[runtimeids.SessionID][]clientui.PendingApproval)
	for _, item := range questions {
		questionID := item.QuestionID
		if item.SessionID == nil || questionID == nil {
			return nil, fmt.Errorf("pending question %q has incomplete identity", item.ID)
		}
		sessionID, err := runtimeids.ParseSessionID(*item.SessionID)
		if err != nil {
			return nil, fmt.Errorf("pending question %q session: %w", item.ID, err)
		}
		sessionName, err := normalizedOptionalQuestionSessionName(item.SessionName)
		if err != nil {
			return nil, fmt.Errorf("pending question %q session name: %w", item.ID, err)
		}
		question := questionCommandPendingQuestion{AskID: *questionID}
		if item.Question.Kind == serverapi.WorkflowAttentionQuestionKindOrdinary {
			if item.Message == nil {
				return nil, fmt.Errorf("pending question %q has no content", item.ID)
			}
			question.Question = *item.Message
			question.Suggestions = append([]string(nil), item.Suggestions...)
			question.RecommendedOptionIndex = textutil.Pointer(item.RecommendedOptionIndex)
		} else if approvalRemote, ok := remote.(questionApprovalRemote); ok {
			sessionApprovals, loaded := approvalCache[sessionID]
			if !loaded {
				approvals, err := approvalRemote.ListPendingApprovalsBySession(ctx, serverapi.ApprovalListPendingBySessionRequest{SessionID: *item.SessionID})
				if err != nil {
					return nil, err
				}
				sessionApprovals = approvals.Approvals
				approvalCache[sessionID] = sessionApprovals
			}
			for _, approval := range sessionApprovals {
				if approval.ApprovalID == *questionID {
					question.Question, question.Approval = approval.Question, &approval
					break
				}
			}
			if question.Approval == nil {
				continue
			}
		} else {
			return nil, errors.New("approval question cannot be read because the remote does not support approvals")
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
	if ask.RecommendedOptionIndex != nil &&
		(*ask.RecommendedOptionIndex < 1 || *ask.RecommendedOptionIndex > len(ask.Suggestions)) {
		return questionCommandPendingQuestion{}, fmt.Errorf(
			"pending question %q recommended option index %d is outside suggestions 1..%d",
			ask.AskID,
			*ask.RecommendedOptionIndex,
			len(ask.Suggestions),
		)
	}
	return questionCommandPendingQuestion{
		AskID:                  ask.AskID,
		Question:               ask.Question,
		Suggestions:            append([]string(nil), ask.Suggestions...),
		RecommendedOptionIndex: textutil.Pointer(ask.RecommendedOptionIndex),
	}, nil
}

func writePendingQuestion(stdout io.Writer, ask questionCommandPendingQuestion, next bool) {
	if next {
		fmt.Fprint(stdout, nextQuestionPrefix)
	}
	question := serverapi.ObservationQuestion{Ask: &clientui.PendingAsk{
		AskID: ask.AskID, Question: ask.Question, Suggestions: ask.Suggestions,
		RecommendedOptionIndex: ask.RecommendedOptionIndex,
	}}
	if ask.Approval != nil {
		question = serverapi.ObservationQuestion{Approval: ask.Approval}
	}
	writeObservedQuestion(stdout, question, "")
}
