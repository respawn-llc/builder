package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"core/shared/valuecopy"
)

const taskCommentListDefaultPageSize = 100

func taskCommentSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs := newCommandFlagSet(config.Command+" task comment", stderr, taskCommentUsage)
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "add":
		return taskCommentAddSubcommand(args[1:], stdout, stderr)
	case "list":
		return taskCommentListSubcommand(args[1:], stdout, stderr)
	case "replace":
		return taskCommentReplaceSubcommand(args[1:], stdout, stderr)
	case "delete":
		return taskCommentDeleteSubcommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown task comment command: %s\n\n", args[0])
		fs := newCommandFlagSet(config.Command+" task comment", stderr, taskCommentUsage)
		fs.Usage()
		return 2
	}
}

func taskCommentAddSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task comment add", stderr, taskCommentAddUsage)
	body := fs.String("body", "", "comment body")
	bodyFile := fs.String("body-file", "", "read the comment body from this file")
	author := fs.String("author", "", "author kind; defaults to user or the calling workflow agent")
	authorID := fs.String("author-id", "", "author name or role shown with the comment")
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task comment add requires <short-id-or-task-id>")
		return 2
	}
	commentBody, err := readTaskBodyFlag(*body, *bodyFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	commentAuthor := taskCommentAuthorForAdd(context.Background(), remote, taskID, *author, flagWasProvided(fs, "author"))
	if trimmedAuthorID := strings.TrimSpace(*authorID); trimmedAuthorID != "" {
		commentAuthor.ID = trimmedAuthorID
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.AddWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentAddRequest{TaskID: taskID, Body: commentBody, Author: commentAuthor.Kind, AuthorID: commentAuthor.ID})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "comment_id\t%s\ntask_id\t%s\n", resp.Comment.ID, resp.Comment.TaskID)
	return 0
}

type taskCommentAuthor struct {
	Kind string
	ID   string
}

func taskCommentAuthorForAdd(ctx context.Context, remote workflowCommandRemote, taskID string, explicitAuthor string, explicit bool) taskCommentAuthor {
	if explicit {
		return taskCommentAuthor{Kind: strings.TrimSpace(explicitAuthor)}
	}
	sessionID, ok := sessionenv.LookupSessionID(os.LookupEnv)
	if !ok {
		return taskCommentAuthor{Kind: "user"}
	}
	detail, err := getWorkflowTaskByID(ctx, remote, taskID)
	if err == nil {
		if authorID := workflowTaskAgentAuthorID(detail, sessionID); authorID != "" {
			return taskCommentAuthor{Kind: "agent", ID: authorID}
		}
	}
	return taskCommentAuthor{Kind: "agent", ID: sessionAgentAuthorID(ctx, remote, sessionID)}
}

func workflowTaskAgentAuthorID(task serverapi.WorkflowTaskDetail, sessionID string) string {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return ""
	}
	nodeKeyByID := map[string]string{}
	for _, placement := range task.Placements {
		if strings.TrimSpace(placement.NodeID) != "" && strings.TrimSpace(placement.NodeKey) != "" {
			nodeKeyByID[placement.NodeID] = strings.TrimSpace(placement.NodeKey)
		}
	}
	run, ok := workflowTaskAgentRun(task, trimmedSessionID)
	if !ok {
		return ""
	}
	if role := strings.TrimSpace(run.Role); role != "" {
		return role
	}
	if nodeKey := nodeKeyByID[strings.TrimSpace(run.NodeID)]; nodeKey != "" {
		return fmt.Sprintf("Node %s agent", nodeKey)
	}
	if nodeID := strings.TrimSpace(run.NodeID); nodeID != "" {
		return fmt.Sprintf("Node %s agent", nodeID)
	}
	if sessionName := strings.TrimSpace(run.SessionName); sessionName != "" {
		return fmt.Sprintf("Session %s agent", sessionName)
	}
	return fmt.Sprintf("Session %s agent", trimmedSessionID)
}

func workflowTaskAgentRun(task serverapi.WorkflowTaskDetail, sessionID string) (serverapi.WorkflowRun, bool) {
	currentRunIDs := map[string]bool{}
	for _, runID := range task.Status.RunIDs {
		if strings.TrimSpace(runID) != "" {
			currentRunIDs[strings.TrimSpace(runID)] = true
		}
	}
	var selected serverapi.WorkflowRun
	selectedScore := workflowTaskAgentRunScore{}
	found := false
	for _, run := range task.Runs {
		if strings.TrimSpace(run.SessionID) != sessionID {
			continue
		}
		score := workflowTaskAgentRunScore{
			Current:       currentRunIDs[strings.TrimSpace(run.ID)],
			Unfinished:    run.CompletedAtUnixMs == nil && run.InterruptedAtUnixMs == nil,
			StartedAt:     run.StartedAtUnixMs,
			CompletedAt:   run.CompletedAtUnixMs,
			InterruptedAt: run.InterruptedAtUnixMs,
			Generation:    run.Generation,
			ID:            strings.TrimSpace(run.ID),
		}
		if !found || score.betterThan(selectedScore) {
			selected = run
			selectedScore = score
			found = true
		}
	}
	return selected, found
}

type workflowTaskAgentRunScore struct {
	Current       bool
	Unfinished    bool
	StartedAt     *int64
	CompletedAt   *int64
	InterruptedAt *int64
	Generation    int64
	ID            string
}

func (s workflowTaskAgentRunScore) betterThan(other workflowTaskAgentRunScore) bool {
	switch {
	case s.Current != other.Current:
		return s.Current
	case s.Unfinished != other.Unfinished:
		return s.Unfinished
	case valuecopy.CompareOptional(s.StartedAt, other.StartedAt) != 0:
		return valuecopy.CompareOptional(s.StartedAt, other.StartedAt) > 0
	case valuecopy.CompareOptional(s.CompletedAt, other.CompletedAt) != 0:
		return valuecopy.CompareOptional(s.CompletedAt, other.CompletedAt) > 0
	case valuecopy.CompareOptional(s.InterruptedAt, other.InterruptedAt) != 0:
		return valuecopy.CompareOptional(s.InterruptedAt, other.InterruptedAt) > 0
	case s.Generation != other.Generation:
		return s.Generation > other.Generation
	default:
		return s.ID > other.ID
	}
}

type sessionMainViewGetter interface {
	GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
}

func sessionAgentAuthorID(ctx context.Context, remote workflowCommandRemote, sessionID string) string {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if getter, ok := remote.(sessionMainViewGetter); ok {
		rpcCtx, cancel := context.WithTimeout(ctx, workflowCommandTimeout)
		defer cancel()
		resp, err := getter.GetSessionMainView(rpcCtx, serverapi.SessionMainViewRequest{SessionID: trimmedSessionID})
		if err == nil {
			if sessionName := strings.TrimSpace(resp.MainView.Session.SessionName); sessionName != "" {
				return fmt.Sprintf("Session %s agent", sessionName)
			}
		}
	}
	return fmt.Sprintf("Session %s agent", trimmedSessionID)
}

func taskCommentListSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task comment list", stderr, taskCommentListUsage)
	projectRef := fs.String("project", ".", "project ID or attached workspace path used to resolve a short ID")
	pageSize := fs.Int("page-size", taskCommentListDefaultPageSize, "maximum number of comments to return")
	pageToken := fs.String("page-token", "", "continue from a previous comment list response")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task comment list requires <short-id-or-task-id>")
		return 2
	}
	if *pageSize < 1 {
		fmt.Fprintln(stderr, "task comment list requires --page-size to be positive")
		return 2
	}
	cfg, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	taskID, err := resolveWorkflowTaskID(context.Background(), cfg, remote, *projectRef, positionals[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	resp, err := remote.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskCommentListRequest{TaskID: taskID, PageSize: *pageSize, PageToken: *pageToken})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeTaskCommentList(stdout, resp.Comments)
	if strings.TrimSpace(resp.NextPageToken) != "" {
		fmt.Fprintf(stderr, "Next page token: `%s`\n", resp.NextPageToken)
	}
	return 0
}

func writeTaskCommentList(stdout io.Writer, comments []serverapi.WorkflowTaskComment) {
	if len(comments) == 0 {
		return
	}
	fmt.Fprintf(stdout, "Comments (%d):\n", len(comments))
	writeTaskCommentBlocks(stdout, comments)
}

func taskCommentReplaceSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task comment replace", stderr, taskCommentReplaceUsage)
	body := fs.String("body", "", "replacement comment body")
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task comment replace requires <comment-id>")
		return 2
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	if err := remote.ReplaceWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentReplaceRequest{CommentID: positionals[0], Body: *body}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "replaced_comment_id\t%s\n", positionals[0])
	return 0
}

func taskCommentDeleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" task comment delete", stderr, taskCommentDeleteUsage)
	positionals, flagArgs := takeLeadingPositionals(args, 1)
	if ok, exitCode := parseCommandFlags(fs, flagArgs); !ok {
		return exitCode
	}
	positionals = append(positionals, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task comment delete requires <comment-id>")
		return 2
	}
	if denyAgentHumanOnlyTaskAction(stderr) {
		return 1
	}
	_, remote, err := workflowCommandRemoteOpener(context.Background(), ".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommandTimeout)
	defer cancel()
	if err := remote.DeleteWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentDeleteRequest{CommentID: positionals[0]}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted_comment_id\t%s\n", positionals[0])
	return 0
}

func writeTaskDetailComments(stdout io.Writer, shortID string, comments []serverapi.WorkflowTaskComment) {
	if len(comments) == 0 {
		return
	}
	if len(comments) >= 10 {
		fmt.Fprintf(stdout, "Comments under this task: %d. `"+config.Command+" task comment list %s` to show them.\n", len(comments), shortID)
		return
	}
	sortedComments := sortedTaskCommentsByCreatedAt(comments)
	fmt.Fprintf(stdout, "Comments (%d):\n", len(sortedComments))
	writeTaskCommentBlocks(stdout, sortedComments)
}

func sortedTaskCommentsByCreatedAt(comments []serverapi.WorkflowTaskComment) []serverapi.WorkflowTaskComment {
	sortedComments := append([]serverapi.WorkflowTaskComment(nil), comments...)
	sort.SliceStable(sortedComments, func(i, j int) bool {
		if sortedComments[i].CreatedAtUnixMs == sortedComments[j].CreatedAtUnixMs {
			return sortedComments[i].ID > sortedComments[j].ID
		}
		return sortedComments[i].CreatedAtUnixMs > sortedComments[j].CreatedAtUnixMs
	})
	return sortedComments
}

func writeTaskCommentBlocks(stdout io.Writer, sortedComments []serverapi.WorkflowTaskComment) {
	for i, comment := range sortedComments {
		if i > 0 {
			fmt.Fprintln(stdout, "---")
		}
		fmt.Fprintf(stdout, "%s at %s UTC:\n%s\n", readableTaskCommentAuthor(comment), time.UnixMilli(comment.CreatedAtUnixMs).UTC().Format(time.RFC3339), comment.Body)
	}
}

func readableTaskCommentAuthor(comment serverapi.WorkflowTaskComment) string {
	author := strings.TrimSpace(comment.Author)
	authorID := strings.TrimSpace(comment.AuthorID)
	if strings.EqualFold(author, "user") {
		if authorID != "" {
			return authorID
		}
		return "User"
	}
	if authorID != "" {
		return authorID
	}
	if author == "" {
		return "Unknown"
	}
	return strings.ToUpper(author[:1]) + author[1:]
}
