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

type candidateEntry struct {
	name string
	path string
}

type candidateDecision uint8

const (
	candidateSkip candidateDecision = iota
	candidateClaim
	candidateStop
)

type candidateTraversalError struct {
	dir   string
	cause error
}

func (e *candidateTraversalError) Error() string {
	return fmt.Sprintf("read prompt directory %s: %v", e.dir, e.cause)
}

func (e *candidateTraversalError) Unwrap() error {
	return e.cause
}

func (s Service) walkCandidates(matches func(string) bool, fn func(candidateEntry) (candidateDecision, error)) error {
	seen := make(map[string]struct{})
	for _, dir := range s.searchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return &candidateTraversalError{dir: dir, cause: err}
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
			if matches != nil && !matches(command) {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			regular, err := regularPromptFile(path)
			if err != nil {
				return &candidateTraversalError{dir: dir, cause: fmt.Errorf("stat prompt file %s: %w", path, err)}
			}
			if !regular {
				continue
			}
			if _, ok := seen[command]; ok {
				continue
			}
			decision, err := fn(candidateEntry{name: command, path: path})
			if err != nil {
				return err
			}
			switch decision {
			case candidateClaim:
				seen[command] = struct{}{}
			case candidateStop:
				return nil
			}
		}
	}
	return nil
}

func regularPromptFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func (s Service) scan() ([]CatalogEntry, error) {
	result := make([]CatalogEntry, 0)
	err := s.walkCandidates(nil, func(entry candidateEntry) (candidateDecision, error) {
		preview, err := previewFile(entry.path)
		if err != nil {
			commandName := entry.name
			return candidateSkip, &Error{Kind: ErrorKindCatalogRead, Command: &commandName, cause: fmt.Errorf("read prompt file %s: %w", entry.path, err)}
		}
		if preview == "" {
			return candidateSkip, nil
		}
		result = append(result, CatalogEntry{Name: entry.name, Preview: preview})
		return candidateClaim, nil
	})
	if err != nil {
		var traversalErr *candidateTraversalError
		if errors.As(err, &traversalErr) {
			return nil, &Error{Kind: ErrorKindCatalogRead, cause: traversalErr}
		}
		return nil, err
	}
	return result, nil
}

func (s Service) findCandidate(command string) (candidate, bool, error) {
	name, err := runtimeinput.ParsePromptCommandName(command)
	if err != nil {
		return candidate{}, false, nil
	}
	var found *candidate
	err = s.walkCandidates(func(command string) bool {
		return command == name.String()
	}, func(entry candidateEntry) (candidateDecision, error) {
		content, readErr := os.ReadFile(entry.path)
		if readErr != nil {
			commandName := name.String()
			return candidateSkip, &Error{Kind: ErrorKindCommandRead, Command: &commandName, cause: fmt.Errorf("read prompt file %s: %w", entry.path, readErr)}
		}
		if strings.TrimSpace(string(content)) == "" {
			return candidateSkip, nil
		}
		value := candidate{name: entry.name, content: string(content)}
		found = &value
		return candidateStop, nil
	})
	if err != nil {
		var traversalErr *candidateTraversalError
		if errors.As(err, &traversalErr) {
			commandName := name.String()
			return candidate{}, false, &Error{Kind: ErrorKindCommandRead, Command: &commandName, cause: traversalErr}
		}
		return candidate{}, false, err
	}
	if found == nil {
		return candidate{}, false, nil
	}
	return *found, true, nil
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
			if outputRunes >= 255 {
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
