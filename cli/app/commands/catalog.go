package commands

import (
	"strings"

	"core/shared/runtimeinput"
)

type PromptCommandCatalogEntry = runtimeinput.PromptCommandCatalogEntry

func NewDefaultRegistryWithPromptCatalog(entries []PromptCommandCatalogEntry) *Registry {
	r := NewDefaultRegistry()
	for _, entry := range entries {
		name, err := runtimeinput.ParsePromptCommandName(entry.Name)
		if err != nil {
			continue
		}
		if name.Identifier == "review" || name.Identifier == "init" {
			if registered, ok := r.handlers[name.Identifier]; ok {
				registered.command.Description = strings.TrimSpace(entry.Preview)
				r.handlers[name.Identifier] = registered
				continue
			}
		}
		commandName := name.String()
		r.handlers[commandName] = registeredCommand{
			command: Command{
				Name:                       commandName,
				Description:                strings.TrimSpace(entry.Preview),
				PreservePromptHistoryDraft: true,
			},
			handler: func(args string) Result {
				return Result{
					Handled:       true,
					PromptCommand: &runtimeinput.PromptCommand{Name: commandName, Arguments: args},
				}
			},
		}
	}
	return r
}
