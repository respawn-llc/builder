package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"core/prompts"
	generatedassets "core/prompts"

	"gopkg.in/yaml.v3"
)

const (
	skillsDirName         = "skills"
	skillFileName         = "SKILL.md"
	skillsAvailableHeader = "Available skills:"
)

var skillsPrompt = strings.TrimSpace(prompts.SkillsPrompt)
var readSkillsDir = os.ReadDir

// errReadSkillsDirectory wraps failures to read a skills discovery directory.
var errReadSkillsDirectory = errors.New("read skills directory")

type injectedSkill struct {
	Name        string
	Description string
	Path        string
	SourceKind  skillSourceKind
}

type SkillMetadata struct {
	Name        string
	Description string
	Path        string
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type skillDiscoveryIssue struct {
	Name   string
	Path   string
	Reason string
}

type skillSourceKind string

const (
	skillSourceGlobal    skillSourceKind = "global"
	skillSourceWorkspace skillSourceKind = "workspace"
	skillSourceGenerated skillSourceKind = "generated"
)

type skillRoot struct {
	Path string
	Kind skillSourceKind
}

func skillsContextMessageWithDisabled(workspaceRoot string, disabledSkills map[string]bool) (string, bool, error) {
	builder := newMetaContextBuilder(workspaceRoot, "", "", disabledSkills, time.Now())
	metaResult, err := builder.Build(metaContextBuildOptions{IncludeSkills: true})
	if err != nil {
		return "", false, err
	}
	if len(metaResult.Skills) == 0 {
		return "", false, nil
	}
	return metaResult.Skills[0].Content, true, nil
}

func discoverInjectedSkills(workspaceRoot, globalConfigDir string, disabledSkills map[string]bool) ([]injectedSkill, []skillDiscoveryIssue, error) {
	roots, err := skillDiscoveryRoots(workspaceRoot, globalConfigDir)
	if err != nil {
		return nil, nil, err
	}
	candidates := make([]injectedSkill, 0)
	issues := make([]skillDiscoveryIssue, 0)
	userSkillNames := map[string]bool{}
	seenPaths := map[string]bool{}
	for _, root := range roots {
		entries, readErr := readSkillsDir(root.Path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, nil, fmt.Errorf("%w %q: %w", errReadSkillsDirectory, root.Path, readErr)
		}
		for _, entry := range entries {
			resolution := resolveSkillDir(root.Path, entry)
			if resolution.Issue != nil {
				issues = append(issues, *resolution.Issue)
			}
			if !resolution.Discoverable {
				continue
			}
			skillPath := filepath.Join(resolution.SkillDir, skillFileName)
			skill, ok := parseInjectedSkill(skillPath)
			if !ok {
				continue
			}
			if seenPaths[skill.Path] {
				continue
			}
			seenPaths[skill.Path] = true
			skill.SourceKind = root.Kind
			candidates = append(candidates, skill)
			if root.Kind != skillSourceGenerated {
				userSkillNames[strings.ToLower(sanitizeSkillSingleLine(skill.Name))] = true
			}
		}
	}

	out := make([]injectedSkill, 0, len(candidates))
	for _, skill := range candidates {
		nameKey := strings.ToLower(sanitizeSkillSingleLine(skill.Name))
		if disabledSkills[nameKey] {
			continue
		}
		if skill.SourceKind == skillSourceGenerated && userSkillNames[nameKey] {
			continue
		}
		out = append(out, skill)
	}
	return out, issues, nil
}

type skillDirResolution struct {
	SkillDir     string
	Discoverable bool
	Issue        *skillDiscoveryIssue
}

func resolveSkillDir(root string, entry os.DirEntry) skillDirResolution {
	skillDir := filepath.Join(root, entry.Name())
	info, err := os.Lstat(skillDir)
	if err != nil {
		return skillDirResolution{Issue: &skillDiscoveryIssue{
			Name:   sanitizeSkillSingleLine(entry.Name()),
			Path:   filepath.ToSlash(skillDir),
			Reason: formatSkillDirResolutionFailure(err),
		}}
	}
	if info.IsDir() {
		return skillDirResolution{SkillDir: skillDir, Discoverable: true}
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return skillDirResolution{}
	}
	targetInfo, err := os.Stat(skillDir)
	if err != nil {
		return skillDirResolution{Issue: &skillDiscoveryIssue{
			Name:   sanitizeSkillSingleLine(entry.Name()),
			Path:   filepath.ToSlash(skillDir),
			Reason: formatSkillDirResolutionFailure(err),
		}}
	}
	if targetInfo.IsDir() {
		return skillDirResolution{SkillDir: skillDir, Discoverable: true}
	}
	return skillDirResolution{Issue: &skillDiscoveryIssue{
		Name:   sanitizeSkillSingleLine(entry.Name()),
		Path:   filepath.ToSlash(skillDir),
		Reason: "symlink target is not a directory",
	}}
}

func formatSkillDirResolutionFailure(err error) string {
	if os.IsNotExist(err) {
		return "symlink target does not exist"
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return strings.TrimSpace(pathErr.Err.Error())
	}
	return strings.TrimSpace(err.Error())
}

func formatSkillDiscoveryWarning(issue skillDiscoveryIssue) string {
	name := strings.TrimSpace(issue.Name)
	if name == "" {
		name = filepath.Base(strings.TrimSpace(issue.Path))
	}
	if strings.TrimSpace(issue.Path) == "" {
		return fmt.Sprintf("Skipped skill %q: %s", name, issue.Reason)
	}
	return fmt.Sprintf("Skipped skill %q at %s: %s", name, issue.Path, issue.Reason)
}

func skillDiscoveryRoots(workspaceRoot, globalConfigDir string) ([]skillRoot, error) {
	roots := make([]skillRoot, 0, 3)
	seen := map[string]bool{}
	addPath := func(path string, kind skillSourceKind) {
		cleaned := filepath.Clean(path)
		if cleaned == "" || seen[cleaned] {
			return
		}
		seen[cleaned] = true
		roots = append(roots, skillRoot{Path: cleaned, Kind: kind})
	}

	layout, err := generatedassets.GeneratedLayoutFor(globalConfigDir)
	if err != nil {
		return nil, err
	}

	addPath(layout.UserSkillsRoot, skillSourceGlobal)
	if strings.TrimSpace(workspaceRoot) != "" {
		addPath(filepath.Join(workspaceRoot, agentsGlobalDirName, skillsDirName), skillSourceWorkspace)
	}
	addPath(layout.GeneratedSkillsRoot, skillSourceGenerated)
	return roots, nil
}

func parseInjectedSkill(path string) (injectedSkill, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return injectedSkill{}, false
	}
	frontmatter, ok := extractSkillFrontmatter(string(contents))
	if !ok {
		return injectedSkill{}, false
	}
	var parsed skillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return injectedSkill{}, false
	}
	name := sanitizeSkillSingleLine(parsed.Name)
	if name == "" {
		name = sanitizeSkillSingleLine(filepath.Base(filepath.Dir(path)))
	}
	description := sanitizeSkillSingleLine(parsed.Description)
	if name == "" || description == "" {
		return injectedSkill{}, false
	}
	resolvedPath := path
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		resolvedPath = canonical
	}
	return injectedSkill{
		Name:        name,
		Description: description,
		Path:        filepath.ToSlash(resolvedPath),
	}, true
}

func ParseSkillMetadata(path string) (SkillMetadata, bool) {
	skill, ok := parseInjectedSkill(path)
	if !ok {
		return SkillMetadata{}, false
	}
	return SkillMetadata{Name: skill.Name, Description: skill.Description, Path: skill.Path}, true
}

func extractSkillFrontmatter(contents string) (string, bool) {
	lines := strings.Split(contents, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}

	frontmatterLines := make([]string, 0)
	foundClosing := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			foundClosing = true
			break
		}
		frontmatterLines = append(frontmatterLines, line)
	}
	if len(frontmatterLines) == 0 || !foundClosing {
		return "", false
	}
	return strings.Join(frontmatterLines, "\n"), true
}

func sanitizeSkillSingleLine(raw string) string {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func normalizedDisabledSkills(disabledSkills map[string]bool) map[string]bool {
	if len(disabledSkills) == 0 {
		return nil
	}
	normalized := make(map[string]bool, len(disabledSkills))
	for name, disabled := range disabledSkills {
		if !disabled {
			continue
		}
		key := strings.ToLower(sanitizeSkillSingleLine(name))
		if key == "" {
			continue
		}
		normalized[key] = true
	}
	return normalized
}

func renderSkillsContext(skills []injectedSkill) string {
	lines := make([]string, 0, len(skills)+2)
	lines = append(lines, skillsPrompt)
	lines = append(lines, skillsAvailableHeader)
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- %s: %s . %s", skill.Name, skill.Path, skill.Description))
	}
	return strings.Join(lines, "\n")
}
