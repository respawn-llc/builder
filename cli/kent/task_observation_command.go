package main

import (
	"bytes"
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
	var diagnostics bytes.Buffer
	fs := newCommandFlagSet(config.Command+" task "+string(mode), &diagnostics, leafCommandUsage(
		config.Command+" task "+string(mode)+" <task>",
		"Wait for a Workflow Task outcome.",
	))
	project := fs.String("project", ".", "project path or ID")
	jsonOut := fs.Bool("json", false, "write a stable JSON envelope")
	positionals, ok, code := parseInterspersedPositionals(fs, args)
	if !ok {
		if *jsonOut {
			return writeObservationUsage(stdout, strings.TrimSpace(diagnostics.String()))
		}
		_, _ = io.Copy(stderr, &diagnostics)
		return code
	}
	if len(positionals) != 1 {
		if *jsonOut {
			return writeObservationUsage(stdout, "task reference is required")
		}
		fmt.Fprintln(stderr, "task reference is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *jsonOut {
		return taskObservationJSON(ctx, stdout, mode, *project, positionals[0])
	}
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
		return writeTaskObservation(stdout, stderr, response, *project)
	})
}

func taskObservationJSON(ctx context.Context, stdout io.Writer, mode serverapi.WorkflowTaskObservationMode, projectRef string, ref string) int {
	cfg, remote, err := workflowCommandRemoteOpener(ctx, ".")
	operation := observationOperationTaskWait
	if mode == serverapi.WorkflowTaskObservationWatch {
		operation = observationOperationTaskWatch
	}
	if err != nil {
		envelope, code := projectObservationError(operation, "", ctx, err)
		return emitObservationJSON(stdout, envelope, code)
	}
	var envelope observationJSONEnvelope
	exitCode := 0
	detail, err := resolveWorkflowTask(ctx, cfg, remote, projectRef, ref)
	if err != nil {
		envelope, exitCode = projectObservationError(operation, "", ctx, err)
	} else {
		response, observeErr := remote.ObserveWorkflowTask(ctx, serverapi.WorkflowTaskObservationRequest{
			TaskID: detail.Summary.ID, ProjectID: detail.Summary.ProjectID, Mode: mode,
		})
		if observeErr != nil {
			envelope, exitCode = projectObservationError(operation, detail.Summary.ID, ctx, observeErr)
		} else {
			envelope, exitCode = projectTaskObservationJSON(detail.Summary.ID, response)
		}
	}
	if closeErr := remote.Close(); closeErr != nil {
		envelope.Warnings = append(envelope.Warnings, closeErr.Error())
	}
	return emitObservationJSON(stdout, envelope, exitCode)
}

func writeTaskObservation(stdout io.Writer, stderr io.Writer, response serverapi.WorkflowTaskObservationResponse, projectRef string) int {
	if stderr == nil {
		stderr = io.Discard
	}
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
				fmt.Fprintf(stderr, "invalid task observation response: %s outcome has no question payload\n", outcome.Kind)
				return 1
			}
			hintArgs := []string{config.Command, "question", "answer", "--task", response.TaskShortID}
			if questionCount > 1 && outcome.SessionID != nil {
				hintArgs = []string{config.Command, "question", "answer", "--session", *outcome.SessionID}
			}
			hintArgs = append(hintArgs, observationQuestionAnswerArgs(*outcome.Question)...)
			if strings.TrimSpace(projectRef) != "" && projectRef != "." && questionCount == 1 {
				hintArgs = append(hintArgs, "--project", projectRef)
			}
			writeTaskOutcomeDiscriminator(stdout, outcome)
			writeObservedQuestion(stdout, *outcome.Question, commandString(hintArgs))
		case serverapi.WorkflowTaskObservationExecutionError, serverapi.WorkflowTaskObservationInterrupted:
			if outcome.Failure == nil {
				fmt.Fprintf(stderr, "invalid task observation response: %s outcome has no failure payload\n", outcome.Kind)
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
