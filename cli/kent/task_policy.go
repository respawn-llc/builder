package main

import (
	"fmt"
	"io"
	"os"

	"core/prompts"
	"core/shared/sessionenv"
)

func isAgentShell() bool {
	_, ok := sessionenv.LookupSessionID(os.LookupEnv)
	return ok
}

func denyAgentHumanOnlyTaskAction(stderr io.Writer) bool {
	if !isAgentShell() {
		return false
	}
	fmt.Fprintln(stderr, prompts.WorkflowHumanOnlyTaskActionDeniedPrompt)
	return true
}
