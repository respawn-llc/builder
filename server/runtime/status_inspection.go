package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"core/server/skillcatalog"

	"gopkg.in/yaml.v3"
)

type SkillInspection struct {
	Name        string
	Description string
	Path        string
	SourceKind  string
	Loaded      bool
	Disabled    bool
	Shadowed    bool
	Reason      string
}

func (e *Engine) CompactionCount() int {
	return e.compactionRuntimeState().Count()
}

func InspectSkills(workspaceRoot, globalConfigDir string, disabledSkills map[string]bool) ([]SkillInspection, error) {
	result, err := skillcatalog.Discover(skillcatalog.Options{
		WorkspaceRoot:  workspaceRoot,
		ConfigRoot:     globalConfigDir,
		DisabledSkills: disabledSkills,
		ReadDir:        readSkillsDir,
	})
	if err != nil {
		return nil, err
	}
	inspections := make([]SkillInspection, 0, len(result.Inspections))
	for _, inspection := range result.Inspections {
		inspections = append(inspections, SkillInspection{
			Name:        inspection.Name,
			Description: inspection.Description,
			Path:        inspection.Path,
			SourceKind:  string(inspection.SourceKind),
			Loaded:      inspection.Loaded,
			Disabled:    inspection.Disabled,
			Shadowed:    inspection.Shadowed,
			Reason:      inspection.Reason,
		})
	}
	return inspections, nil
}

func inspectSkillAtPath(fallbackName, skillPath string) SkillInspection {
	resolvedPath := filepath.ToSlash(skillPath)
	if canonical, err := filepath.EvalSymlinks(skillPath); err == nil {
		resolvedPath = filepath.ToSlash(canonical)
	}

	contents, err := os.ReadFile(skillPath)
	if err != nil {
		reason := "could not read SKILL.md"
		if os.IsNotExist(err) {
			reason = "missing SKILL.md"
		}
		return SkillInspection{
			Name:   sanitizeSkillSingleLine(fallbackName),
			Path:   resolvedPath,
			Loaded: false,
			Reason: reason,
		}
	}

	frontmatter, ok := extractSkillFrontmatter(string(contents))
	if !ok {
		return SkillInspection{
			Name:   sanitizeSkillSingleLine(fallbackName),
			Path:   resolvedPath,
			Loaded: false,
			Reason: "missing or invalid frontmatter",
		}
	}

	var parsed skillFrontmatter
	if err := yamlUnmarshal([]byte(frontmatter), &parsed); err != nil {
		return SkillInspection{
			Name:   sanitizeSkillSingleLine(fallbackName),
			Path:   resolvedPath,
			Loaded: false,
			Reason: "invalid frontmatter YAML",
		}
	}

	name := sanitizeSkillSingleLine(parsed.Name)
	if name == "" {
		name = sanitizeSkillSingleLine(fallbackName)
	}
	description := sanitizeSkillSingleLine(parsed.Description)
	if name == "" || description == "" {
		return SkillInspection{
			Name:   name,
			Path:   resolvedPath,
			Loaded: false,
			Reason: "missing name or description",
		}
	}

	return SkillInspection{
		Name:        name,
		Description: description,
		Path:        resolvedPath,
		Loaded:      true,
	}
}

func InstalledAgentsPaths(workspaceRoot, globalConfigDir string) ([]string, error) {
	paths, err := agentsInjectionPaths(workspaceRoot, globalConfigDir)
	if err != nil {
		return nil, err
	}
	installed := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, statErr := os.Stat(path); statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, fmt.Errorf("stat AGENTS.md %q: %w", path, statErr)
		}
		resolved := path
		if canonical, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
			resolved = canonical
		}
		installed = append(installed, filepath.ToSlash(strings.TrimSpace(resolved)))
	}
	sort.Strings(installed)
	return installed, nil
}

var yamlUnmarshal = func(data []byte, out any) error {
	return yaml.Unmarshal(data, out)
}
