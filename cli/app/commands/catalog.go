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
