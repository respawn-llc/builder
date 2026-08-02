package commands

import "core/shared/textutil"

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
	return textutil.ExpandPromptTemplate(prompt, args)
}
