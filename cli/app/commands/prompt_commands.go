package commands

import "core/shared/runtimeinput"

func registerBuiltinPromptCommands(r *Registry) {
	if r == nil {
		return
	}
	for _, command := range []struct {
		name        string
		description string
		identity    string
	}{
		{name: "review", description: "Run code review", identity: "prompt:review"},
		{name: "init", description: "Run repository initialization prompt", identity: "prompt:init"},
	} {
		command := command
		r.RegisterWithOptions(command.name, command.description, RegisterOptions{
			ActiveRunPolicy:            ActiveRunPolicyAllowed,
			PreservePromptHistoryDraft: true,
		}, func(args string) Result {
			return Result{
				Handled:           true,
				Action:            ActionNone,
				PromptCommand:     &runtimeinput.PromptCommand{Name: command.identity, Arguments: args},
				FreshConversation: true,
			}
		})
	}
}
