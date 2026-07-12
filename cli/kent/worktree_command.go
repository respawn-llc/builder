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

	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

const worktreeCommandTimeout = 5 * time.Second

type worktreeCommandRemote interface {
	GetWorktreeStatus(context.Context, serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error)
	Close() error
}

var worktreeCommandRemoteOpener = openWorktreeCommandRemote

func worktreeSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		return worktreeStatusSubcommand(nil, stdout, stderr)
	}
	switch args[0] {
	case "status":
		return worktreeStatusSubcommand(args[1:], stdout, stderr)
	case "--help", "-h":
		worktreeUsage.write(newCommandFlagSet(config.Command+" worktree", stderr, worktreeUsage))
		return 0
	default:
		fmt.Fprintf(stderr, "unknown worktree command: %s\n", args[0])
		return 2
	}
}

func worktreeStatusSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" worktree status", stderr, worktreeStatusUsage)
	sessionFlag := fs.String("session", "", "session to inspect; required outside Kent shell commands")
	jsonOut := fs.Bool("json", false, "write the status response as JSON")
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "worktree status does not accept positional arguments")
		return 2
	}
	sessionID, err := resolveWorktreeCommandSession(*sessionFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	remote, err := worktreeCommandRemoteOpener(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = remote.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), worktreeCommandTimeout)
	defer cancel()
	status, err := remote.GetWorktreeStatus(ctx, serverapi.WorktreeStatusRequest{SessionID: sessionID})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		if err := json.NewEncoder(stdout).Encode(status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, status.Worktree.RecordedRoot)
	for _, problem := range status.Problems {
		fmt.Fprintln(stdout, problem.Kind)
	}
	return 0
}

func resolveWorktreeCommandSession(sessionFlag string) (string, error) {
	if sessionID, ok := sessionenv.LookupSessionID(os.LookupEnv); ok {
		return sessionID, nil
	}
	if trimmed := strings.TrimSpace(sessionFlag); trimmed != "" {
		return trimmed, nil
	}
	return "", errors.New("worktree command requires --session outside Kent shell commands")
}

func openWorktreeCommandRemote(ctx context.Context) (worktreeCommandRemote, error) {
	cfg, err := config.Load(".", config.LoadOptions{})
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, worktreeCommandTimeout)
	defer cancel()
	remote, err := client.DialConfiguredRemote(dialCtx, cfg)
	if err != nil {
		return nil, err
	}
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		_ = remote.Close()
		return nil, err
	}
	return remote, nil
}
