package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/serverapi"
)

type goalTimeoutRemote struct {
	goalCommandRemote
	failRead bool
}

func (r goalTimeoutRemote) ShowGoal(context.Context, serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if r.failRead {
		return serverapi.RuntimeGoalShowResponse{}, context.DeadlineExceeded
	}
	return serverapi.RuntimeGoalShowResponse{}, nil
}

func (goalTimeoutRemote) SetGoal(context.Context, serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, context.DeadlineExceeded
}

func (goalTimeoutRemote) PauseGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, context.DeadlineExceeded
}

func (goalTimeoutRemote) ResumeGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, context.DeadlineExceeded
}

func (goalTimeoutRemote) CompleteGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, context.DeadlineExceeded
}

func (goalTimeoutRemote) ClearGoal(context.Context, serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, context.DeadlineExceeded
}

func (goalTimeoutRemote) Close() error { return nil }

func TestGoalCommandsPresentTimeoutWithoutReportingSuccess(t *testing.T) {
	for _, phase := range []string{"connect", "read", "mutation"} {
		for _, action := range []string{"show", "set", "pause", "resume", "complete", "clear"} {
			if phase == "read" && action != "show" && action != "complete" || phase == "mutation" && action == "show" {
				continue
			}
			t.Run(phase+"/"+action, func(t *testing.T) {
				previous := goalCommandRemoteOpener
				t.Cleanup(func() { goalCommandRemoteOpener = previous })
				goalCommandRemoteOpener = func(context.Context) (goalCommandRemote, error) {
					if phase == "connect" {
						return nil, context.DeadlineExceeded
					}
					return goalTimeoutRemote{failRead: phase == "read"}, nil
				}
				args := []string{action, "--session", "session-1"}
				if action == "set" {
					args = append(args, "finish the task")
				}
				if action == "complete" {
					args = append(args, "--confirm")
				}
				var stdout, stderr bytes.Buffer
				if code := goalSubcommand(args, &stdout, &stderr); code != 1 {
					t.Fatalf("exit code = %d", code)
				}
				if stdout.Len() != 0 {
					t.Fatal("timed-out command reported success")
				}
				presentation := client.PresentGoalRequestError(context.DeadlineExceeded)
				if strings.TrimSpace(stderr.String()) != presentation.Error() {
					t.Fatalf("command did not use the timeout presentation: %s", stderr.String())
				}
			})
		}
	}
}

func TestGoalTimeoutPresentationPreservesCause(t *testing.T) {
	for _, cause := range []error{
		context.DeadlineExceeded,
		fmt.Errorf("request failed: %w", context.DeadlineExceeded),
		&net.DNSError{IsTimeout: true},
	} {
		presented := client.PresentGoalRequestError(cause)
		var timeout client.GoalRequestTimeoutError
		if !errors.As(presented, &timeout) {
			t.Fatalf("expected timeout presentation for %T, got %T", cause, presented)
		}
		if !errors.Is(presented, cause) {
			t.Fatal("timeout presentation lost diagnostic cause")
		}
		var mutationTimeout client.GoalRequestTimeoutError
		if !errors.As(goalMutationCommandError("session-1", cause), &mutationTimeout) {
			t.Fatal("mutation did not present the timeout")
		}
	}
	cause := errors.New("invalid goal")
	if client.PresentGoalRequestError(cause) != cause {
		t.Fatal("unrelated error was replaced")
	}
}

type goalDeadlineRemote struct {
	goalCommandRemote
	t         *testing.T
	deadline  time.Time
	completed bool
}

func (r *goalDeadlineRemote) ShowGoal(ctx context.Context, _ serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok || deadline != r.deadline {
		r.t.Fatal("goal read must share the connection's command deadline")
	}
	return serverapi.RuntimeGoalShowResponse{}, nil
}

func (r *goalDeadlineRemote) CompleteGoal(ctx context.Context, _ serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	deadline, ok := ctx.Deadline()
	if !ok || deadline != r.deadline {
		r.t.Fatal("goal completion must use the remaining command budget")
	}
	r.completed = true
	return serverapi.RuntimeGoalShowResponse{}, nil
}

func (r *goalDeadlineRemote) Close() error { return nil }

func TestGoalCompleteSharesFifteenSecondBudget(t *testing.T) {
	previous := goalCommandRemoteOpener
	t.Cleanup(func() { goalCommandRemoteOpener = previous })
	remote := &goalDeadlineRemote{t: t}
	goalCommandRemoteOpener = func(ctx context.Context) (goalCommandRemote, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("goal connection must have a command deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 14*time.Second || remaining > 15*time.Second {
			t.Fatalf("expected a fifteen-second command budget, got %s", remaining)
		}
		remote.deadline = deadline
		return remote, nil
	}
	if code := goalSubcommand([]string{"complete", "--session", "session-1", "--confirm"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !remote.completed {
		t.Fatal("goal was not completed")
	}
}
