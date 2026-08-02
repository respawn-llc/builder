package promptcommands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"core/shared/runtimeinput"
)

type candidate struct {
	name    string
	content string
}

func (s Service) scan() ([]candidate, error) {
	seen := make(map[string]struct{})
	result := make([]candidate, 0)
	for _, dir := range s.searchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, &Error{Kind: ErrorKindCatalogRead, cause: fmt.Errorf("read prompt directory %s: %w", dir, err)}
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, entry := range entries {
			name, ok := commandNameForEntry(entry.Name(), entry.IsDir())
			if !ok {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				commandName := name
				return nil, &Error{Kind: ErrorKindCatalogRead, Command: &commandName, cause: fmt.Errorf("read prompt file %s: %w", filepath.Join(dir, entry.Name()), err)}
			}
			if strings.TrimSpace(string(content)) == "" {
				continue
			}
			seen[name] = struct{}{}
			result = append(result, candidate{name: name, content: string(content)})
		}
	}
	return result, nil
}

func (s Service) findCandidate(command string) (candidate, bool, error) {
	name, err := runtimeinput.ParsePromptCommandName(command)
	if err != nil {
		return candidate{}, false, nil
	}
	for _, dir := range s.searchDirs() {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			commandName := name.String()
			return candidate{}, false, &Error{Kind: ErrorKindCommandRead, Command: &commandName, cause: fmt.Errorf("read prompt directory %s: %w", dir, readErr)}
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, entry := range entries {
			candidateName, ok := commandNameForEntry(entry.Name(), entry.IsDir())
			if !ok || candidateName != name.String() {
				continue
			}
			content, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
			if readErr != nil {
				commandName := name.String()
				return candidate{}, false, &Error{Kind: ErrorKindCommandRead, Command: &commandName, cause: fmt.Errorf("read prompt file %s: %w", filepath.Join(dir, entry.Name()), readErr)}
			}
			if strings.TrimSpace(string(content)) == "" {
				continue
			}
			return candidate{name: candidateName, content: string(content)}, true, nil
		}
	}
	return candidate{}, false, nil
}

func commandNameForEntry(entryName string, directory bool) (string, bool) {
	if directory || filepath.Ext(entryName) != ".md" {
		return "", false
	}
	identifier := runtimeinput.NormalizeIdentifier(strings.TrimSuffix(entryName, ".md"))
	if identifier == "" {
		return "", false
	}
	return "prompt:" + identifier, true
}

func (s Service) searchDirs() []string {
	return []string{
		filepath.Join(s.workspaceRoot, ".kent", "prompts"),
		filepath.Join(s.workspaceRoot, ".kent", "commands"),
		filepath.Join(s.persistenceRoot, "prompts"),
		filepath.Join(s.persistenceRoot, "commands"),
		filepath.Join(s.persistenceRoot, ".generated", "prompts"),
		filepath.Join(s.persistenceRoot, ".generated", "commands"),
	}
}
