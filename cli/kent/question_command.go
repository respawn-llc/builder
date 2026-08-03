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

	"core/shared/apicontract"
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
	AccessOptions          []clientui.ApprovalOption
	CreatedAt              time.Time
	Access                 bool
}

type questionCommandApprovalRemote interface {
	apicontract.ApprovalViewService
	apicontract.PromptControlService
}

type questionCommandRuntimeRemote interface {
	apicontract.RuntimeControlService
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
		questions, err := listPendingSessionPrompts(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(questions) == 0 {
			fmt.Fprintln(stdout, noPendingQuestionsText)
			return 0
		}
		writePendingQuestion(stdout, questions[0], false)
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
		questions, err := listPendingSessionPrompts(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(questions) == 0 {
			fmt.Fprintln(stderr, noPendingQuestionAnswerText)
			return 1
		}
		pending := questions[0]
		if pending.Access {
			return c.answerSessionApproval(remote, sessionID, pending, option, commentary, stdout, stderr)
		}
		request := serverapi.AskAnswerRequest{
			ClientRequestID:      uuid.NewString(),
			SessionID:            sessionID.String(),
			AskID:                pending.AskID,
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
		next, err := listPendingSessionPrompts(remote, sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(next) == 0 {
			fmt.Fprintln(stdout, questionAnswerDoneText)
			return 0
		}
		writePendingQuestion(stdout, next[0], true)
		return 0
	})
}

func (c questionCommand) answerSessionApproval(remote questionCommandRemote, sessionID runtimeids.SessionID, pending questionCommandPendingQuestion, option *int, commentary *string, stdout io.Writer, stderr io.Writer) int {
	approvalRemote, ok := remote.(questionCommandApprovalRemote)
	if !ok {
		fmt.Fprintln(stderr, "approval control is unavailable")
		return 1
	}
	if option == nil || *option < 1 || *option > len(pending.AccessOptions) {
		fmt.Fprintln(stderr, "access requests require a valid --option")
		return 2
	}
	decision := pending.AccessOptions[*option-1].Decision
	commentaryValue, _ := textutil.OptionalExact(commentary)
	answerCtx, stopAnswer := questionAnswerContext()
	defer stopAnswer()
	effects, err := client.PlanApprovalCommentary(decision, commentaryValue)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := executeQuestionApprovalPlan(answerCtx, remote, sessionID.String(), effects, func() error {
		return approvalRemote.AnswerApproval(answerCtx, serverapi.ApprovalAnswerRequest{
			ClientRequestID: uuid.NewString(),
			SessionID:       sessionID.String(),
			ApprovalID:      pending.AskID,
			Decision:        decision,
			Commentary: func() string {
				if decision == clientui.ApprovalDecisionDeny {
					return commentaryValue
				}
				return ""
			}(),
		})
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	next, err := listPendingSessionPrompts(remote, sessionID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(next) == 0 {
		fmt.Fprintln(stdout, questionAnswerDoneText)
		return 0
	}
	writePendingQuestion(stdout, next[0], true)
	return 0
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
		commentaryValue, _ := textutil.OptionalExact(commentary)
		if question.Access {
			if option == nil || *option < 1 || *option > len(question.AccessOptions) {
				fmt.Fprintln(stderr, "access requests require a valid --option")
				return 2
			}
		}
		var accessDecision clientui.ApprovalDecision
		if question.Access {
			accessDecision = question.AccessOptions[*option-1].Decision
		}
		request := serverapi.WorkflowTaskQuestionAnswerRequest{
			ClientRequestID:      uuid.NewString(),
			TaskID:               taskID,
			AskID:                question.AskID,
			SelectedOptionNumber: option,
		}
		if question.Access {
			request.Approval = &serverapi.WorkflowTaskQuestionApprovalAnswer{
				Decision: accessDecision,
				Commentary: func() string {
					if accessDecision == clientui.ApprovalDecisionDeny {
						return commentaryValue
					}
					return ""
				}(),
			}
			request.SelectedOptionNumber = nil
			request.FreeformAnswer = ""
		}
		if commentary != nil && !question.Access {
			request.FreeformAnswer = *commentary
		}
		answerCtx, stopAnswer := questionAnswerContext()
		defer stopAnswer()
		if question.Access {
			effects, planErr := client.PlanApprovalCommentary(
				accessDecision,
				commentaryValue,
			)
			if planErr != nil {
				fmt.Fprintln(stderr, planErr)
				return 2
			}
			if err := executeQuestionApprovalPlan(answerCtx, remote, candidate.SessionID.String(), effects, func() error {
				return remote.AnswerWorkflowTaskQuestion(answerCtx, request)
			}); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		} else {
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

func listPendingSessionPrompts(remote questionCommandRemote, sessionID runtimeids.SessionID) ([]questionCommandPendingQuestion, error) {
	response, err := listPendingSessionQuestions(remote, sessionID)
	if err != nil {
		return nil, err
	}
	questions := make([]questionCommandPendingQuestion, 0, len(response.Asks))
	for _, ask := range response.Asks {
		question, err := pendingSessionQuestion(ask)
		if err != nil {
			return nil, err
		}
		question.CreatedAt = ask.CreatedAt
		questions = append(questions, question)
	}
	if approvalRemote, ok := remote.(apicontract.ApprovalViewService); ok {
		ctx, cancel := context.WithTimeout(context.Background(), questionCommandTimeout)
		defer cancel()
		approvals, err := approvalRemote.ListPendingApprovalsBySession(ctx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionID.String()})
		if err != nil {
			return nil, err
		}
		for _, approval := range approvals.Approvals {
			questions = append(questions, questionCommandPendingQuestion{
				AskID:         approval.ApprovalID,
				Question:      approval.Question,
				AccessOptions: append([]clientui.ApprovalOption(nil), approval.Options...),
				CreatedAt:     approval.CreatedAt,
				Access:        true,
			})
		}
	}
	sort.SliceStable(questions, func(i, j int) bool {
		if !questions[i].CreatedAt.Equal(questions[j].CreatedAt) {
			return questions[i].CreatedAt.Before(questions[j].CreatedAt)
		}
		return questions[i].AskID < questions[j].AskID
	})
	return questions, nil
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
	candidates, err := taskQuestionCandidates(response.Items)
	if err != nil {
		return nil, err
	}
	approvalRemote, ok := remote.(apicontract.ApprovalViewService)
	if !ok {
		return candidates, nil
	}
	approvalCache := make(map[string][]clientui.PendingApproval)
	for _, item := range response.Items {
		if item.Kind != string(serverapi.WorkflowTaskAttentionKindQuestion) ||
			item.Question == nil ||
			item.Question.Kind != serverapi.WorkflowAttentionQuestionKindApproval ||
			item.SessionID == nil ||
			item.ApprovalID == nil {
			continue
		}
		sessionIDValue := *item.SessionID
		approvals, loaded := approvalCache[sessionIDValue]
		if !loaded {
			response, err := approvalRemote.ListPendingApprovalsBySession(rpcCtx, serverapi.ApprovalListPendingBySessionRequest{SessionID: sessionIDValue})
			if err != nil {
				return nil, err
			}
			approvals = append([]clientui.PendingApproval(nil), response.Approvals...)
			approvalCache[sessionIDValue] = approvals
		}
		approval, ok := serverapi.FindPendingApproval(approvals, *item.ApprovalID)
		if !ok {
			continue
		}
		sessionID, err := runtimeids.ParseSessionID(*item.SessionID)
		if err != nil {
			return nil, err
		}
		var candidate *taskQuestionSessionCandidate
		for index := range candidates {
			if candidates[index].SessionID == sessionID {
				candidate = &candidates[index]
				break
			}
		}
		if candidate == nil {
			candidates = append(candidates, taskQuestionSessionCandidate{SessionID: sessionID, SessionName: textutil.Pointer(item.SessionName)})
			candidate = &candidates[len(candidates)-1]
		}
		candidate.Questions = append(candidate.Questions, questionCommandPendingQuestion{
			AskID:         approval.ApprovalID,
			Question:      approval.Question,
			AccessOptions: append([]clientui.ApprovalOption(nil), approval.Options...),
			CreatedAt:     approval.CreatedAt,
			Access:        true,
		})
	}
	for index := range candidates {
		sort.SliceStable(candidates[index].Questions, func(left, right int) bool {
			if !candidates[index].Questions[left].CreatedAt.Equal(candidates[index].Questions[right].CreatedAt) {
				return candidates[index].Questions[left].CreatedAt.Before(candidates[index].Questions[right].CreatedAt)
			}
			return candidates[index].Questions[left].AskID < candidates[index].Questions[right].AskID
		})
	}
	return candidates, nil
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
		candidates[index].Questions = append(candidates[index].Questions, questionCommandPendingQuestion{
			AskID:                  *item.QuestionID,
			Question:               *item.Message,
			Suggestions:            append([]string(nil), item.Suggestions...),
			RecommendedOptionIndex: textutil.Pointer(item.RecommendedOptionIndex),
			CreatedAt:              time.UnixMilli(item.OccurredAtUnixMs),
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
		CreatedAt:              ask.CreatedAt,
	}, nil
}

func writePendingQuestion(stdout io.Writer, ask questionCommandPendingQuestion, next bool) {
	if next {
		fmt.Fprint(stdout, nextQuestionPrefix)
	}
	fmt.Fprintln(stdout, ask.Question)
	if ask.Access {
		fmt.Fprintln(stdout, questionSuggestionsHeading)
		for index, option := range ask.AccessOptions {
			fmt.Fprintf(stdout, "%d. %s\n", index+1, option.Label)
		}
		return
	}
	if len(ask.Suggestions) == 0 {
		return
	}
	fmt.Fprintln(stdout, questionSuggestionsHeading)
	for index, suggestion := range ask.Suggestions {
		suffix := ""
		if ask.RecommendedOptionIndex != nil && *ask.RecommendedOptionIndex == index+1 {
			suffix = recommendedSuggestionSuffix
		}
		fmt.Fprintf(stdout, "%d. %s%s\n", index+1, suggestion, suffix)
	}
}

func writeObservedQuestion(stdout io.Writer, question serverapi.RuntimeObservationQuestion, answerHint string) {
	writePendingQuestion(stdout, questionCommandPendingQuestion{
		AskID:                  question.QuestionID,
		Question:               question.Text,
		Suggestions:            append([]string(nil), question.Suggestions...),
		RecommendedOptionIndex: textutil.Pointer(question.RecommendedOptionIndex),
		AccessOptions:          append([]clientui.ApprovalOption(nil), question.AccessOptions...),
		Access:                 question.Kind == serverapi.RuntimeObservationQuestionAccessRequest,
	}, false)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, answerHint)
}

func observedQuestionAnswerHint(question serverapi.RuntimeObservationQuestion, selector string) string {
	if question.Kind == serverapi.RuntimeObservationQuestionAccessRequest || len(question.Suggestions) > 0 {
		return fmt.Sprintf("Answer with: kent question answer %s --option <number>", selector)
	}
	return fmt.Sprintf("Answer with: kent question answer %s --commentary \"<answer>\"", selector)
}

func executeQuestionApprovalPlan(
	ctx context.Context,
	remote any,
	sessionID string,
	effects []client.ApprovalCommentaryEffect,
	answerApproval func() error,
) error {
	var runtimeRemote questionCommandRuntimeRemote
	for _, effect := range effects {
		switch effect.Kind {
		case client.ApprovalCommentaryEffectRuntimeInput:
			if runtimeRemote == nil {
				var ok bool
				runtimeRemote, ok = remote.(questionCommandRuntimeRemote)
				if !ok {
					return errors.New("runtime input control is unavailable for approval commentary")
				}
			}
			if _, err := runtimeRemote.SubmitUserTurn(ctx, client.NewRuntimeUserTurnRequest(sessionID, effect.Commentary)); err != nil {
				return err
			}
		case client.ApprovalCommentaryEffectApproval:
			if answerApproval == nil {
				return errors.New("approval answer is unavailable")
			}
			return answerApproval()
		default:
			return errors.New("approval commentary effect is invalid")
		}
	}
	return errors.New("approval plan did not contain an approval effect")
}
