package commands

import "strings"

const promptArgumentsPlaceholder = "$ARGUMENTS"

type promptCommandSpec struct {
	Name         string
	Description  string
	Prompt       string
	FreshSession bool
}

func registerPromptCommands(r *Registry, specs []promptCommandSpec) {
	if r == nil {
		return
	}
	for _, spec := range specs {
		commandName := spec.Name
		commandDescription := spec.Description
		commandPrompt := spec.Prompt
		freshSession := spec.FreshSession
		activeRunPolicy := ActiveRunPolicyRequiresIdle
		if freshSession {
			activeRunPolicy = ActiveRunPolicyAllowed
		}
		r.RegisterWithOptions(commandName, commandDescription, RegisterOptions{ActiveRunPolicy: activeRunPolicy, PreservePromptHistoryDraft: true}, func(args string) Result {
			return Result{
				Handled:           true,
				Action:            ActionNone,
				SubmitUser:        true,
				User:              buildPromptSubmission(commandPrompt, args),
				FreshConversation: freshSession,
			}
		})
	}
}

func buildPromptSubmission(prompt, args string) string {
	trimmedArgs := strings.TrimSpace(args)
	if strings.Contains(prompt, promptArgumentsPlaceholder) {
		return strings.ReplaceAll(prompt, promptArgumentsPlaceholder, trimmedArgs)
	}
	if trimmedArgs == "" {
		return prompt
	}
	base := strings.TrimRight(prompt, "\n")
	if base == "" {
		return trimmedArgs
	}
	return base + "\n\n" + trimmedArgs
}
