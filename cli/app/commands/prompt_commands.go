package commands

import "core/shared/runtimeinput"

func registerBuiltinPromptCommands(r *Registry) {
	if r == nil {
		return
	}
	for _, command := range runtimeinput.BuiltinPromptCommands() {
		command := command
		r.RegisterWithOptions(command.Alias(), builtinPromptDescription(command), RegisterOptions{
			ActiveRunPolicy:            ActiveRunPolicyAllowed,
			PreservePromptHistoryDraft: true,
		}, func(args string) Result {
			prompt := runtimeinput.NewBuiltinPromptCommand(command, args)
			return Result{
				Handled:           true,
				Action:            ActionNone,
				PromptCommand:     &prompt,
				FreshConversation: true,
			}
		})
	}
}

func builtinPromptDescription(command runtimeinput.BuiltinPromptCommand) string {
	switch command {
	case runtimeinput.BuiltinPromptCommandReview:
		return "Run code review"
	case runtimeinput.BuiltinPromptCommandInit:
		return "Run repository initialization prompt"
	default:
		return ""
	}
}
