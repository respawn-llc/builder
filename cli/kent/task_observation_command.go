package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"core/shared/config"
	"core/shared/serverapi"
)

func taskWaitSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskObservationSubcommand(args, stdout, stderr, serverapi.WorkflowTaskObservationModeWait)
}

func taskWatchSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskObservationSubcommand(args, stdout, stderr, serverapi.WorkflowTaskObservationModeWatch)
}

func taskObservationSubcommand(args []string, stdout io.Writer, stderr io.Writer, mode serverapi.WorkflowTaskObservationMode) int {
	fs := newCommandFlagSet(config.Command+" task "+string(mode), stderr, leafCommandUsage(
		config.Command+" task "+string(mode)+" <task>",
		"Wait for a Workflow Task outcome.",
	))
	projectRef := fs.String("project", ".", "project path or ID")
	positionals, ok, exitCode := parseInterspersedPositionals(fs, args)
	if !ok {
		return exitCode
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task reference is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		detail, err := resolveWorkflowTask(ctx, cfg, remote, *projectRef, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		response, err := remote.ObserveWorkflowTask(ctx, serverapi.WorkflowTaskObservationRequest{
			TaskID:    detail.Summary.ID,
			ProjectID: detail.Summary.ProjectID,
			Mode:      mode,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, context.Canceled) {
				return 130
			}
			return 1
		}
		return writeTaskObservationWithProject(stdout, response, *projectRef)
	})
}

func writeTaskObservation(stdout io.Writer, response serverapi.WorkflowTaskObservationResponse) int {
	return writeTaskObservationWithProject(stdout, response, "")
}

func writeTaskObservationWithProject(stdout io.Writer, response serverapi.WorkflowTaskObservationResponse, projectRef string) int {
	exitCode := 0
	for index, outcome := range response.Outcomes {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		switch outcome.Kind {
		case serverapi.RuntimeObservationOutcomeTaskDone:
			taskShortID, _ := response.Target.TaskShortIDValue()
			fmt.Fprintf(stdout, "Task %s entered Done status\n", taskShortID)
		case serverapi.RuntimeObservationOutcomeQuestion:
			if outcome.Question == nil {
				continue
			}
			exitCode = reducedObservationExitCode(exitCode, writeObservedOutcome(stdout, outcome, observationQuestionHint(response, outcome, projectRef)))
		default:
			exitCode = reducedObservationExitCode(exitCode, writeObservedOutcome(stdout, outcome, ""))
		}
	}
	return exitCode
}
