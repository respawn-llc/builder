package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"core/server/skillcatalog"
	"core/shared/config"
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

func InspectSkills(workspaceRoot, globalConfigDir string, policy config.SkillPolicy) ([]SkillInspection, error) {
	result, err := skillcatalog.Discover(skillcatalog.Options{
		WorkspaceRoot: workspaceRoot,
		ConfigRoot:    globalConfigDir,
		Policy:        policy,
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
