package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	"core/prompts"
	"core/server/skillcatalog"
)

const skillsAvailableHeader = "Available skills:"

var skillsPrompt = strings.TrimSpace(prompts.SkillsPrompt)

func formatSkillDiscoveryWarning(issue skillcatalog.Issue) string {
	name := strings.TrimSpace(issue.Name)
	if name == "" {
		name = filepath.Base(strings.TrimSpace(issue.Path))
	}
	if strings.TrimSpace(issue.Path) == "" {
		return fmt.Sprintf("Skipped skill %q: %s", name, issue.Reason)
	}
	return fmt.Sprintf("Skipped skill %q at %s: %s", name, issue.Path, issue.Reason)
}

func renderSkillsContext(skills []skillcatalog.Skill) string {
	lines := make([]string, 0, len(skills)+2)
	lines = append(lines, skillsPrompt)
	lines = append(lines, skillsAvailableHeader)
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- %s: %s . %s", skill.Name, skill.Path, skill.Description))
	}
	return strings.Join(lines, "\n")
}
