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

	"github.com/google/uuid"
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
	Close() error
}

var questionCommandRemoteOpener = openQuestionCommandRemote

type questionCommandSelector struct {
	SessionID  *runtimeids.SessionID
	TaskRef    *string
	ProjectRef string
	Command    string
}

type taskQuestionSessionCandidate struct {
	SessionID   runtimeids.SessionID
	SessionName *string
	Questions   []clientui.PendingAsk
}

func questionSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) > 0 {
		switch args[0] {
		case "answer":
			return questionAnswerSubcommand(args[1:], stdout, stderr)
		case "--help", "-h":
			questionUsage.write(newCommandFlagSet(config.Command+" question", stderr, questionUsage))
			return 0
		}
	}
	return questionShowSubcommand(args, stdout, stderr)
}

func questionShowSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
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
		return showSessionQuestion(*selector.SessionID, stdout, stderr)
	}
	return showTaskQuestion(selector, stdout, stderr)
}

func showSessionQuestion(sessionID runtimeids.SessionID, stdout io.Writer, stderr io.Writer) int {
	return withQuestionCommandRemote(stderr, sessionID, func(remote questionCommandRemote) int {
		response, err := listPendingSessionQuestions(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(response.Asks) == 0 {
			fmt.Fprintln(stdout, noPendingQuestionsText)
			return 0
		}
		writePendingQuestion(stdout, response.Asks[0], false)
		return 0
	})
}

func questionAnswerSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
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
	if option == nil && (commentary == nil || strings.TrimSpace(*commentary) == "") {
		fmt.Fprintln(stderr, "question answer requires --option or non-blank --commentary")
		return 2
	}
	selector, err := resolveQuestionCommandSelector(sessionFlag, taskFlag, *projectFlag, config.Command+" question answer")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if selector.SessionID != nil {
		return answerSessionQuestion(*selector.SessionID, option, commentary, stdout, stderr)
	}
	return answerTaskQuestion(selector, option, commentary, stdout, stderr)
}

func answerSessionQuestion(
	sessionID runtimeids.SessionID,
	option *int,
	commentary *string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	return withQuestionCommandRemote(stderr, sessionID, func(remote questionCommandRemote) int {
		pending, err := listPendingSessionQuestions(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(pending.Asks) == 0 {
			fmt.Fprintln(stderr, noPendingQuestionAnswerText)
			return 1
		}
		request := serverapi.AskAnswerRequest{
			ClientRequestID:      uuid.NewString(),
			SessionID:            sessionID.String(),
			AskID:                pending.Asks[0].AskID,
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
		next, err := listPendingSessionQuestions(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(next.Asks) == 0 {
			fmt.Fprintln(stdout, questionAnswerDoneText)
			return 0
		}
		writePendingQuestion(stdout, next.Asks[0], true)
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
		request := serverapi.WorkflowTaskQuestionAnswerRequest{
			ClientRequestID:      uuid.NewString(),
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
		refreshed, err := listTaskQuestionCandidates(context.Background(), remote, taskID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, next := range refreshed {
			if next.SessionID == candidate.SessionID && len(next.Questions) > 0 {
				writePendingQuestion(stdout, next.Questions[0], true)
				return 0
			}
		}
		fmt.Fprintln(stdout, questionAnswerDoneText)
		return 0
	})
}

func questionAnswerContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
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
	return taskQuestionCandidates(response.Items)
}

func taskQuestionCandidates(items []serverapi.WorkflowAttentionItem) ([]taskQuestionSessionCandidate, error) {
	questions := make([]serverapi.WorkflowAttentionItem, 0, len(items))
	for _, item := range items {
		if item.Kind != string(serverapi.WorkflowTaskAttentionKindQuestion) {
			continue
		}
		if item.Question == nil {
			return nil, fmt.Errorf("pending question %q has no typed prompt", item.ID)
		}
		if item.Question.Kind != serverapi.WorkflowAttentionQuestionKindOrdinary {
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
		if item.SessionID == nil || item.QuestionID == nil || item.Message == nil {
			return nil, fmt.Errorf("pending question %q has incomplete identity or content", item.ID)
		}
		sessionID, err := runtimeids.ParseSessionID(*item.SessionID)
		if err != nil {
			return nil, fmt.Errorf("pending question %q session: %w", item.ID, err)
		}
		sessionName, err := normalizedOptionalQuestionSessionName(item.SessionName)
		if err != nil {
			return nil, fmt.Errorf("pending question %q session name: %w", item.ID, err)
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
		recommended := 0
		if item.RecommendedOptionIndex != nil {
			recommended = *item.RecommendedOptionIndex
		}
		candidates[index].Questions = append(candidates[index].Questions, clientui.PendingAsk{
			AskID:                  *item.QuestionID,
			SessionID:              sessionID.String(),
			Question:               *item.Message,
			Suggestions:            append([]string(nil), item.Suggestions...),
			RecommendedOptionIndex: recommended,
			CreatedAt:              time.UnixMilli(item.OccurredAtUnixMs).UTC(),
		})
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

func withQuestionCommandRemote(
	stderr io.Writer,
	sessionID runtimeids.SessionID,
	run func(questionCommandRemote) int,
) int {
	remote, err := questionCommandRemoteOpener(context.Background(), sessionID.String())
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

func writePendingQuestion(stdout io.Writer, ask clientui.PendingAsk, next bool) {
	if next {
		fmt.Fprint(stdout, nextQuestionPrefix)
	}
	fmt.Fprintln(stdout, ask.Question)
	if len(ask.Suggestions) == 0 {
		return
	}
	fmt.Fprintln(stdout, questionSuggestionsHeading)
	for index, suggestion := range ask.Suggestions {
		suffix := ""
		if ask.RecommendedOptionIndex == index+1 {
			suffix = recommendedSuggestionSuffix
		}
		fmt.Fprintf(stdout, "%d. %s%s\n", index+1, suggestion, suffix)
	}
}
