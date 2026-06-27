package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"core/prompts"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"github.com/google/uuid"
)

const goalCommandTimeout = 5 * time.Second

type goalCommandRemote interface {
	ShowGoal(context.Context, serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error)
	SetGoal(context.Context, serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error)
	PauseGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)
	ResumeGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)
	CompleteGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)
	ClearGoal(context.Context, serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error)
	Close() error
}

var goalCommandRemoteOpener = openGoalCommandRemote

func goalSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs := newCommandFlagSet(config.Command+" goal", stderr, goalUsage)
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	action := strings.TrimSpace(args[0])
	switch action {
	case "show":
		return goalShowSubcommand(args[1:], stdout, stderr)
	case "set":
		return goalSetSubcommand(args[1:], stdout, stderr)
	case "pause":
		return goalStatusSubcommand("pause", args[1:], stdout, stderr)
	case "resume":
		return goalStatusSubcommand("resume", args[1:], stdout, stderr)
	case "complete":
		return goalCompleteSubcommand(args[1:], stdout, stderr)
	case "clear":
		return goalClearSubcommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown goal command: %s\n\n", action)
		fs := newCommandFlagSet(config.Command+" goal", stderr, goalUsage)
		goalUsage.write(fs)
		return 2
	}
}

func goalShowSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" goal show", stderr, goalCommandUsage)
	sessionFlag := fs.String("session", "", "target session id")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "goal show does not accept positional arguments")
		return 2
	}
	target, _, err := resolveGoalCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	remote, err := goalCommandRemoteOpener(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	defer cancel()
	resp, err := remote.ShowGoal(ctx, serverapi.RuntimeGoalShowRequest{SessionID: target})
	if err != nil {
		fmt.Fprintln(stderr, formatGoalCommandError(err))
		return 1
	}
	if *jsonOut {
		if err := json.NewEncoder(stdout).Encode(resp); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	writeGoalShowText(stdout, resp.Goal)
	return 0
}

func goalSetSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" goal set", stderr, goalCommandUsage)
	sessionFlag := fs.String("session", "", "target session id")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	target, agent, err := resolveGoalCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if objective == "" {
		fmt.Fprintln(stderr, "goal set requires an objective")
		return 2
	}
	remote, err := goalCommandRemoteOpener(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	defer cancel()
	actor := "user"
	if agent {
		actor = "agent"
	}
	resp, err := remote.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{ClientRequestID: uuid.NewString(), SessionID: target, Objective: objective, Actor: actor})
	if err != nil {
		fmt.Fprintln(stderr, formatGoalCommandError(err))
		return 1
	}
	writeGoalShowText(stdout, resp.Goal)
	return 0
}

func goalStatusSubcommand(action string, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" goal "+action, stderr, goalCommandUsage)
	sessionFlag := fs.String("session", "", "target session id")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintf(stderr, "goal %s does not accept positional arguments\n", action)
		return 2
	}
	target, agent, err := resolveGoalCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if agent {
		fmt.Fprintln(stderr, prompts.RenderGoalAgentCommandDeniedPrompt())
		return 1
	}
	remote, err := goalCommandRemoteOpener(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	defer cancel()
	req := serverapi.RuntimeGoalStatusRequest{ClientRequestID: uuid.NewString(), SessionID: target, Actor: "user"}
	var resp serverapi.RuntimeGoalShowResponse
	if action == "pause" {
		resp, err = remote.PauseGoal(ctx, req)
	} else {
		resp, err = remote.ResumeGoal(ctx, req)
	}
	if err != nil {
		fmt.Fprintln(stderr, formatGoalCommandError(err))
		return 1
	}
	writeGoalShowText(stdout, resp.Goal)
	return 0
}

func goalCompleteSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" goal complete", stderr, goalCommandUsage)
	sessionFlag := fs.String("session", "", "target session id")
	confirmed := fs.Bool("confirm", false, "confirm goal completion")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "goal complete does not accept positional arguments")
		return 2
	}
	target, agent, err := resolveGoalCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	remote, err := goalCommandRemoteOpener(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	showCtx, showCancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	current, err := remote.ShowGoal(showCtx, serverapi.RuntimeGoalShowRequest{SessionID: target})
	showCancel()
	if err != nil {
		fmt.Fprintln(stderr, formatGoalCommandError(err))
		return 1
	}
	if goalAlreadyComplete(current.Goal) {
		fmt.Fprintln(stdout, prompts.RenderGoalAlreadyCompletePrompt(current.Goal.Objective))
		return 0
	}
	if agent && !*confirmed {
		objective := ""
		if current.Goal != nil {
			objective = current.Goal.Objective
		}
		fmt.Fprintln(stderr, prompts.RenderGoalCompleteConfirmRequiredPrompt(objective))
		return 1
	}
	actor := "user"
	if agent {
		actor = "agent"
	}
	completeCtx, completeCancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	defer completeCancel()
	resp, err := remote.CompleteGoal(completeCtx, serverapi.RuntimeGoalStatusRequest{ClientRequestID: uuid.NewString(), SessionID: target, Actor: actor})
	if err != nil {
		fmt.Fprintln(stderr, formatGoalCommandError(err))
		return 1
	}
	writeGoalShowText(stdout, resp.Goal)
	return 0
}

func goalAlreadyComplete(goal *serverapi.RuntimeGoal) bool {
	return goal != nil && strings.TrimSpace(goal.Status) == "complete"
}

func goalClearSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" goal clear", stderr, goalCommandUsage)
	sessionFlag := fs.String("session", "", "target session id")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "goal clear does not accept positional arguments")
		return 2
	}
	target, agent, err := resolveGoalCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if agent {
		fmt.Fprintln(stderr, prompts.RenderGoalAgentCommandDeniedPrompt())
		return 1
	}
	remote, err := goalCommandRemoteOpener(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), goalCommandTimeout)
	defer cancel()
	if _, err := remote.ClearGoal(ctx, serverapi.RuntimeGoalClearRequest{ClientRequestID: uuid.NewString(), SessionID: target, Actor: "user"}); err != nil {
		fmt.Fprintln(stderr, formatGoalCommandError(err))
		return 1
	}
	fmt.Fprintln(stdout, "Goal cleared")
	return 0
}

func resolveGoalCommandSession(sessionFlag string) (sessionID string, agent bool, err error) {
	if envSessionID, ok := sessionenv.LookupSessionID(os.LookupEnv); ok {
		return envSessionID, true, nil
	}
	trimmed := strings.TrimSpace(sessionFlag)
	if trimmed == "" {
		return "", false, errors.New("goal command requires --session outside " + config.Product + " shell commands")
	}
	return trimmed, false, nil
}

func openGoalCommandRemote(ctx context.Context) (goalCommandRemote, error) {
	cfg, err := config.Load(".", config.LoadOptions{})
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, goalCommandTimeout)
	defer cancel()
	remote, err := client.DialConfiguredRemote(dialCtx, cfg)
	if err != nil {
		return nil, err
	}
	// When the operator selected an explicit non-default persistence root, only
	// operate on a server actually serving that root so goal commands never read
	// or mutate a different instance reachable on the same TCP endpoint.
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		_ = remote.Close()
		return nil, err
	}
	return remote, nil
}

func writeGoalShowText(stdout io.Writer, goal *serverapi.RuntimeGoal) {
	if goal == nil {
		fmt.Fprintln(stdout, "No goal")
		return
	}
	fmt.Fprintf(stdout, "Goal: %s\nStatus: %s\n", goal.Objective, goal.Status)
}

func formatGoalCommandError(err error) string {
	if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		return "live-runtime-unavailable: " + err.Error()
	}
	return err.Error()
}
