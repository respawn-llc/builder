package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type goalRuntimeControlClient struct {
	apicontract.RuntimeControlService
	t     *testing.T
	cause error
}

func (c *goalRuntimeControlClient) respond(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
	c.t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		c.t.Fatal("goal request has no deadline")
	}
	if remaining := time.Until(deadline); remaining < 14*time.Second || remaining > 15*time.Second {
		c.t.Fatalf("goal request budget = %s, want fifteen seconds", remaining)
	}
	return serverapi.RuntimeGoalShowResponse{}, c.cause
}

func (c *goalRuntimeControlClient) ShowGoal(ctx context.Context, _ serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.respond(ctx)
}

func (c *goalRuntimeControlClient) SetGoal(ctx context.Context, _ serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.respond(ctx)
}

func (c *goalRuntimeControlClient) PauseGoal(ctx context.Context, _ serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.respond(ctx)
}

func (c *goalRuntimeControlClient) ResumeGoal(ctx context.Context, _ serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.respond(ctx)
}

func (c *goalRuntimeControlClient) CompleteGoal(ctx context.Context, _ serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.respond(ctx)
}

func (c *goalRuntimeControlClient) ClearGoal(ctx context.Context, _ serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return c.respond(ctx)
}

func TestRuntimeGoalCallsHaveGoalBudgetAndPresentTimeout(t *testing.T) {
	for _, action := range []string{"show", "set", "pause", "resume", "complete", "clear"} {
		t.Run(action, func(t *testing.T) {
			controls := &goalRuntimeControlClient{t: t, cause: context.DeadlineExceeded}
			runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, nil)
			var err error
			switch action {
			case "show":
				_, err = runtimeClient.ShowGoal()
			case "set":
				_, err = runtimeClient.SetGoal("finish the task")
			case "pause":
				_, err = runtimeClient.PauseGoal()
			case "resume":
				_, err = runtimeClient.ResumeGoal()
			case "complete":
				_, err = runtimeClient.CompleteGoal()
			case "clear":
				_, err = runtimeClient.ClearGoal()
			}
			var presented client.GoalRequestTimeoutError
			if !errors.Is(err, controls.cause) || !errors.As(err, &presented) {
				t.Fatalf("goal timeout needs presentation with its diagnostic cause preserved: %T", err)
			}
		})
	}
}

func TestRuntimeGoalReadRecoversUnavailableConnection(t *testing.T) {
	goal := &clientui.Goal{ID: "goal-1", Objective: "finish the task", Status: clientui.RuntimeGoalStatusActive}
	controls := &reconnectRetryRuntimeControlClient{
		showGoalErr:  serverapi.ErrRuntimeUnavailable,
		showGoalResp: runtimeClientTestShowResponse(goal),
	}
	runtimeClient := newTestSessionRuntimeClientWithControls(controls)
	reactivator := newRuntimeReactivator()
	recovered := false
	reactivator.SetReactivateFunc(func(context.Context) error {
		recovered = true
		return nil
	})
	runtimeClient.SetRuntimeReactivator(reactivator)
	result, err := runtimeClient.ShowGoal()
	if err != nil {
		t.Fatalf("show goal after connection recovery: %v", err)
	}
	if !recovered || result == nil || result.Goal == nil || *result.Goal != *goal {
		t.Fatalf("goal was not restored after connection recovery: %+v", result)
	}
}

func TestRuntimeClientInputMakesOneExplicitCall(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, nil).(*sessionRuntimeClient)

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		Input: runtimeinput.Text("hello"),
	}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if controls.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", controls.submitCalls)
	}
}
