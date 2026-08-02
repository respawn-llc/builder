package promptcommands

import (
	"strings"

	"core/shared/runtimeinput"
)

type CatalogEntry = runtimeinput.PromptCommandCatalogEntry

func (s Service) Catalog() ([]CatalogEntry, error) {
	if err := s.validateRoots(ErrorKindCatalogRead); err != nil {
		return nil, err
	}
	candidates, err := s.scan()
	if err != nil {
		return nil, err
	}
	entries := make([]CatalogEntry, 0, len(candidates))
	for _, candidate := range candidates {
		entries = append(entries, CatalogEntry{
			Name:    candidate.name,
			Preview: preview(candidate.content),
		})
	}
	return entries, nil
}

func preview(content string) string {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return ""
	}
	compact := strings.Join(fields, " ")
	runes := []rune(compact)
	if len(runes) > 256 {
		runes = runes[:256]
	}
	return string(runes)
}
