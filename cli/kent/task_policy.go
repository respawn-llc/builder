package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"core/prompts"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func denyAgentHumanOnlyTaskAction(stderr io.Writer) bool {
	if _, ok := sessionenv.LookupSessionID(os.LookupEnv); !ok {
		return false
	}
	fmt.Fprintln(stderr, prompts.WorkflowHumanOnlyTaskActionDeniedPrompt)
	return true
}

func workflowTaskInvokingSessionID() (*runtimeids.SessionID, error) {
	raw, ok := sessionenv.LookupSessionID(os.LookupEnv)
	if !ok {
		return nil, nil
	}
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		return nil, fmt.Errorf("resolve invoking Session: %w", err)
	}
	return &sessionID, nil
}

func writeWorkflowTaskMutationSelfTargetError(stderr io.Writer, err error) bool {
	var denied *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(err, &denied) {
		return false
	}
	fmt.Fprintln(stderr, prompts.WorkflowTaskMutationSelfTargetDeniedPrompt)
	return true
}
