package main

import (
	"fmt"
	"io"
	"os"

	"core/prompts"
	"core/shared/sessionenv"
)

func denyAgentHumanOnlyTaskAction(stderr io.Writer) bool {
	if _, ok := sessionenv.LookupSessionID(os.LookupEnv); !ok {
		return false
	}
	fmt.Fprintln(stderr, prompts.WorkflowHumanOnlyTaskActionDeniedPrompt)
	return true
}
