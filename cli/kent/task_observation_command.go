package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"core/shared/config"
	"core/shared/serverapi"
)

func taskWaitSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskObservationSubcommand(args, stdout, stderr, serverapi.WorkflowTaskObservationWait)
}

func taskWatchSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	return taskObservationSubcommand(args, stdout, stderr, serverapi.WorkflowTaskObservationWatch)
}

func taskObservationSubcommand(args []string, stdout io.Writer, stderr io.Writer, mode serverapi.WorkflowTaskObservationMode) int {
	fs := newCommandFlagSet(config.Command+" task "+string(mode), stderr, leafCommandUsage(
		config.Command+" task "+string(mode)+" <task>",
		"Wait for a Workflow Task outcome.",
	))
	project := fs.String("project", ".", "project path or ID")
	positionals, ok, code := parseInterspersedPositionals(fs, args)
	if !ok {
		return code
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "task reference is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWorkflowCommandSession(stderr, func(cfg config.App, remote workflowCommandRemote) int {
		detail, err := resolveWorkflowTask(ctx, cfg, remote, *project, positionals[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		response, err := remote.ObserveWorkflowTask(ctx, serverapi.WorkflowTaskObservationRequest{
			TaskID: detail.Summary.ID, ProjectID: detail.Summary.ProjectID, Mode: mode,
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, context.Canceled) {
				return 130
			}
			return 1
		}
		return writeTaskObservation(stdout, response, *project)
	})
}

func writeTaskObservation(stdout io.Writer, response serverapi.WorkflowTaskObservationResponse, projectRef string) int {
	exitCode := 0
	questionCount := 0
	for _, outcome := range response.Outcomes {
		if outcome.Kind == serverapi.WorkflowTaskObservationQuestion {
			questionCount++
		}
	}
	for index, outcome := range response.Outcomes {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		switch outcome.Kind {
		case serverapi.WorkflowTaskObservationDone:
			fmt.Fprintf(stdout, "Task %s entered Done status\n", response.TaskShortID)
		case serverapi.WorkflowTaskObservationQuestion:
			if outcome.Question == nil {
				return 1
			}
			hint := "kent question answer --task " + response.TaskShortID
			if questionCount > 1 && outcome.SessionID != nil {
				hint = "kent question answer --session " + *outcome.SessionID
			}
			if outcome.Question.Approval != nil || outcome.Question.Ask != nil && len(outcome.Question.Ask.Suggestions) > 0 {
				hint += " --option <number>"
			} else {
				hint += " --commentary \"<answer>\""
			}
			if strings.TrimSpace(projectRef) != "" && projectRef != "." && questionCount == 1 {
				hint += " --project " + shellQuote(projectRef)
			}
			writeTaskOutcomeDiscriminator(stdout, outcome)
			writeObservedQuestion(stdout, *outcome.Question, hint)
		case serverapi.WorkflowTaskObservationExecutionError, serverapi.WorkflowTaskObservationInterrupted:
			if outcome.Failure == nil {
				return 1
			}
			writeTaskOutcomeDiscriminator(stdout, outcome)
			fmt.Fprintln(stdout, outcome.Failure.Reason)
			if outcome.Failure.Diagnostic != nil {
				fmt.Fprintln(stdout, *outcome.Failure.Diagnostic)
			}
			if outcome.Kind == serverapi.WorkflowTaskObservationExecutionError {
				exitCode = 1
			} else if exitCode == 0 {
				exitCode = 130
			}
		}
		if outcome.Kind != serverapi.WorkflowTaskObservationDone && outcome.Kind != serverapi.WorkflowTaskObservationQuestion {
			continue
		}
	}
	return exitCode
}

func writeTaskOutcomeDiscriminator(stdout io.Writer, outcome serverapi.WorkflowTaskObservationOutcome) {
	if outcome.SessionID != nil {
		fmt.Fprintf(stdout, "Session %s", *outcome.SessionID)
	} else if outcome.ScriptPath != nil {
		fmt.Fprintf(stdout, "Script %s", *outcome.ScriptPath)
	} else {
		return
	}
	if outcome.NodeKey != nil {
		fmt.Fprintf(stdout, " (Node %s)", *outcome.NodeKey)
	}
	fmt.Fprintln(stdout, ":")
}
