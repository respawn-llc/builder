package commands

import (
	"strings"

	"core/shared/runtimeinput"
)

type PromptCommandCatalogEntry struct {
	Name    string
	Preview string
}

func NewDefaultRegistryWithPromptCatalog(entries []PromptCommandCatalogEntry) *Registry {
	r := NewDefaultRegistry()
	for _, entry := range entries {
		name, err := runtimeinput.ParsePromptCommandName(entry.Name)
		if err != nil || name.String() == "prompt:review" || name.String() == "prompt:init" {
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
