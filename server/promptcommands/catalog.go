package promptcommands

import "core/shared/runtimeinput"

type CatalogEntry = runtimeinput.PromptCommandCatalogEntry

func (s Service) Catalog() ([]CatalogEntry, error) {
	if err := s.validateRoots(ErrorKindCatalogRead); err != nil {
		return nil, err
	}
	return s.scan()
}
