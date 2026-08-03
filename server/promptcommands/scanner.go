package promptcommands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"core/shared/runtimeinput"
)

type candidate struct {
	name    string
	content string
}

func (s Service) scan() ([]CatalogEntry, error) {
	seen := make(map[string]struct{})
	result := make([]CatalogEntry, 0)
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
			if _, builtin := runtimeinput.BuiltinPromptCommandForName(*name); builtin {
				continue
			}
			command := name.String()
			if _, ok := seen[command]; ok {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			preview, err := previewFile(path)
			if err != nil {
				commandName := command
				return nil, &Error{Kind: ErrorKindCatalogRead, Command: &commandName, cause: fmt.Errorf("read prompt file %s: %w", path, err)}
			}
			if preview == "" {
				continue
			}
			seen[command] = struct{}{}
			result = append(result, CatalogEntry{Name: command, Preview: preview})
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
			if !ok || candidateName.String() != name.String() {
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
			return candidate{name: candidateName.String(), content: string(content)}, true, nil
		}
	}
	return candidate{}, false, nil
}

func commandNameForEntry(entryName string, directory bool) (*runtimeinput.PromptCommandName, bool) {
	if directory || filepath.Ext(entryName) != ".md" {
		return nil, false
	}
	identifier, err := runtimeinput.NormalizeIdentifier(strings.TrimSuffix(entryName, ".md"))
	if err != nil {
		return nil, false
	}
	name := runtimeinput.PromptCommandName{Identifier: identifier}
	return &name, true
}

func previewFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	preview, readErr := previewReader(file)
	closeErr := file.Close()
	if readErr != nil {
		if closeErr != nil {
			return "", errors.Join(readErr, closeErr)
		}
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return preview, nil
}

func preview(content string) string {
	preview, err := previewReader(strings.NewReader(content))
	if err != nil {
		panic(fmt.Sprintf("in-memory prompt preview failed: %v", err))
	}
	return preview
}

func previewReader(reader io.Reader) (string, error) {
	buffered := bufio.NewReader(reader)
	var result strings.Builder
	outputRunes := 0
	pendingSpace := false
	for {
		r, _, err := buffered.ReadRune()
		if err == io.EOF {
			return result.String(), nil
		}
		if err != nil {
			return "", err
		}
		if unicode.IsSpace(r) {
			if outputRunes > 0 {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			if outputRunes == 256 {
				return result.String(), nil
			}
			result.WriteByte(' ')
			outputRunes++
			pendingSpace = false
		}
		if outputRunes == 256 {
			return result.String(), nil
		}
		result.WriteRune(r)
		outputRunes++
		if outputRunes == 256 {
			return result.String(), nil
		}
	}
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
