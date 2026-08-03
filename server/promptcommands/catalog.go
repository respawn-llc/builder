package promptcommands

import "core/shared/runtimeinput"

type CatalogEntry = runtimeinput.PromptCommandCatalogEntry

func (s Service) Catalog() ([]CatalogEntry, error) {
	if err := s.validateRoots(ErrorKindCatalogRead); err != nil {
		return nil, err
	}
	entries, err := s.scan()
	if err != nil {
		return nil, err
	}
	for _, builtin := range builtinPromptCommands() {
		for i := range entries {
			if entries[i].Name == builtin.kind.Name() {
				entries = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		entries = append(entries, CatalogEntry{Name: builtin.kind.Name(), Preview: preview(builtin.content)})
	}
	return entries, nil
}
