package skillcatalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"core/prompts"
	brand "core/shared/config"

	"gopkg.in/yaml.v3"
)

const (
	SkillsDirName = "skills"
	SkillFileName = "SKILL.md"
)

var ErrReadSkillsDirectory = errors.New("read skills directory")

type SourceKind string

const (
	SourceKindGlobal    SourceKind = "global"
	SourceKindWorkspace SourceKind = "workspace"
	SourceKindGenerated SourceKind = "generated"
)

type Options struct {
	WorkspaceRoot            string
	ConfigRoot               string
	DisabledSkills           map[string]bool
	IncludeEmbeddedGenerated bool
	ReadDir                  func(string) ([]os.DirEntry, error)
}

type Skill struct {
	Name        string
	Description string
	Path        string
	SourceKind  SourceKind
	Disabled    bool
	Shadowed    bool
}

type Issue struct {
	Name       string
	Path       string
	SourceKind SourceKind
	Reason     string
}

type Inspection struct {
	Name        string
	Description string
	Path        string
	SourceKind  SourceKind
	Loaded      bool
	Disabled    bool
	Shadowed    bool
	Reason      string
}

type Result struct {
	Roots       []Root
	Skills      []Skill
	Issues      []Issue
	Inspections []Inspection
}

type Root struct {
	Path string
	Kind SourceKind
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func Discover(opts Options) (Result, error) {
	roots, err := Roots(opts.WorkspaceRoot, opts.ConfigRoot)
	if err != nil {
		return Result{}, err
	}
	readDir := opts.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	disabled := normalizedDisabledSkills(opts.DisabledSkills)
	candidates := make([]Skill, 0)
	issues := make([]Issue, 0)
	inspections := make([]Inspection, 0)
	seenLoadedPaths := map[string]bool{}
	userSkillNames := map[string]bool{}
	for _, root := range roots {
		rootInspections, rootSkills, rootIssues, err := discoverRoot(root, readDir, opts.IncludeEmbeddedGenerated)
		if err != nil {
			return Result{}, err
		}
		for _, inspection := range rootInspections {
			if inspection.Loaded {
				nameKey := normalizedSkillName(inspection.Name)
				if disabled[nameKey] {
					inspection.Disabled = true
				}
				if seenLoadedPaths[inspection.Path] {
					inspection.Loaded = false
					inspection.Disabled = false
					inspection.Reason = "duplicate resolved SKILL.md path"
				} else {
					seenLoadedPaths[inspection.Path] = true
				}
				if inspection.Loaded && root.Kind != SourceKindGenerated {
					userSkillNames[nameKey] = true
				}
			}
			inspections = append(inspections, inspection)
		}
		candidates = append(candidates, rootSkills...)
		issues = append(issues, rootIssues...)
	}
	for idx := range inspections {
		if inspections[idx].Loaded && inspections[idx].SourceKind == SourceKindGenerated && userSkillNames[normalizedSkillName(inspections[idx].Name)] {
			inspections[idx].Shadowed = true
		}
	}
	loaded := make([]Skill, 0, len(candidates))
	seenPaths := map[string]bool{}
	for _, skill := range candidates {
		nameKey := normalizedSkillName(skill.Name)
		if disabled[nameKey] {
			continue
		}
		if skill.SourceKind == SourceKindGenerated && userSkillNames[nameKey] {
			continue
		}
		if seenPaths[skill.Path] {
			continue
		}
		seenPaths[skill.Path] = true
		loaded = append(loaded, skill)
	}
	sort.Slice(inspections, func(i, j int) bool {
		if inspections[i].Shadowed != inspections[j].Shadowed {
			return !inspections[i].Shadowed && inspections[j].Shadowed
		}
		if inspections[i].Disabled != inspections[j].Disabled {
			return !inspections[i].Disabled && inspections[j].Disabled
		}
		if inspections[i].Loaded != inspections[j].Loaded {
			return inspections[i].Loaded && !inspections[j].Loaded
		}
		return inspections[i].Path < inspections[j].Path
	})
	return Result{Roots: roots, Skills: loaded, Issues: issues, Inspections: inspections}, nil
}

func Roots(workspaceRoot, configRoot string) ([]Root, error) {
	layout, err := prompts.GeneratedLayoutFor(configRoot)
	if err != nil {
		return nil, err
	}
	roots := make([]Root, 0, 3)
	seen := map[string]bool{}
	add := func(path string, kind SourceKind) {
		cleaned := filepath.Clean(path)
		if cleaned == "." || cleaned == "" || seen[cleaned] {
			return
		}
		seen[cleaned] = true
		roots = append(roots, Root{Path: cleaned, Kind: kind})
	}
	add(layout.UserSkillsRoot, SourceKindGlobal)
	if strings.TrimSpace(workspaceRoot) != "" {
		add(filepath.Join(workspaceRoot, brand.ConfigDirName, SkillsDirName), SourceKindWorkspace)
	}
	add(layout.GeneratedSkillsRoot, SourceKindGenerated)
	return roots, nil
}

func discoverRoot(root Root, readDir func(string) ([]os.DirEntry, error), includeEmbeddedGenerated bool) ([]Inspection, []Skill, []Issue, error) {
	if root.Kind == SourceKindGenerated && includeEmbeddedGenerated {
		return discoverEmbeddedGenerated(root.Path)
	}
	entries, err := readDir(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("%w %q: %w", ErrReadSkillsDirectory, root.Path, err)
	}
	inspections := make([]Inspection, 0)
	skills := make([]Skill, 0)
	issues := make([]Issue, 0)
	for _, entry := range entries {
		resolution := resolveSkillDir(root.Path, entry)
		if resolution.Issue != nil {
			issue := Issue{Name: resolution.Issue.Name, Path: resolution.Issue.Path, SourceKind: root.Kind, Reason: resolution.Issue.Reason}
			issues = append(issues, issue)
			inspections = append(inspections, Inspection{Name: issue.Name, Path: filepath.ToSlash(filepath.Join(issue.Path, SkillFileName)), SourceKind: root.Kind, Reason: issue.Reason})
		}
		if !resolution.Discoverable {
			continue
		}
		skillPath := filepath.Join(resolution.SkillDir, SkillFileName)
		inspection := InspectSkillAtPath(entry.Name(), skillPath)
		inspection.SourceKind = root.Kind
		inspections = append(inspections, inspection)
		if inspection.Loaded {
			skills = append(skills, Skill{Name: inspection.Name, Description: inspection.Description, Path: inspection.Path, SourceKind: root.Kind})
		}
	}
	return inspections, skills, issues, nil
}

func discoverEmbeddedGenerated(projectedRoot string) ([]Inspection, []Skill, []Issue, error) {
	entries, err := fs.ReadDir(prompts.GeneratedSkillsFS, SkillsDirName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w embedded generated skills: %w", ErrReadSkillsDirectory, err)
	}
	inspections := make([]Inspection, 0, len(entries))
	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		embeddedPath := filepath.ToSlash(filepath.Join(SkillsDirName, entry.Name(), SkillFileName))
		contents, err := fs.ReadFile(prompts.GeneratedSkillsFS, embeddedPath)
		if err != nil {
			continue
		}
		projectedPath := filepath.ToSlash(filepath.Join(projectedRoot, entry.Name(), SkillFileName))
		inspection := inspectSkillContents(entry.Name(), projectedPath, string(contents))
		inspection.SourceKind = SourceKindGenerated
		inspections = append(inspections, inspection)
		if inspection.Loaded {
			skills = append(skills, Skill{Name: inspection.Name, Description: inspection.Description, Path: inspection.Path, SourceKind: SourceKindGenerated})
		}
	}
	return inspections, skills, nil, nil
}

type skillDirResolution struct {
	SkillDir     string
	Discoverable bool
	Issue        *Issue
}

func resolveSkillDir(root string, entry os.DirEntry) skillDirResolution {
	skillDir := filepath.Join(root, entry.Name())
	info, err := os.Lstat(skillDir)
	if err != nil {
		return skillDirResolution{Issue: &Issue{Name: sanitizeSkillSingleLine(entry.Name()), Path: filepath.ToSlash(skillDir), Reason: formatSkillDirResolutionFailure(err)}}
	}
	if info.IsDir() {
		return skillDirResolution{SkillDir: skillDir, Discoverable: true}
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return skillDirResolution{}
	}
	targetInfo, err := os.Stat(skillDir)
	if err != nil {
		return skillDirResolution{Issue: &Issue{Name: sanitizeSkillSingleLine(entry.Name()), Path: filepath.ToSlash(skillDir), Reason: formatSkillDirResolutionFailure(err)}}
	}
	if targetInfo.IsDir() {
		return skillDirResolution{SkillDir: skillDir, Discoverable: true}
	}
	return skillDirResolution{Issue: &Issue{Name: sanitizeSkillSingleLine(entry.Name()), Path: filepath.ToSlash(skillDir), Reason: "symlink target is not a directory"}}
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

func InspectSkillAtPath(fallbackName, skillPath string) Inspection {
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
		return Inspection{Name: sanitizeSkillSingleLine(fallbackName), Path: resolvedPath, Reason: reason}
	}
	return inspectSkillContents(fallbackName, resolvedPath, string(contents))
}

func ParseSkillMetadata(path string) (Skill, bool) {
	inspection := InspectSkillAtPath(filepath.Base(filepath.Dir(path)), path)
	if !inspection.Loaded {
		return Skill{}, false
	}
	return Skill{Name: inspection.Name, Description: inspection.Description, Path: inspection.Path}, true
}

func inspectSkillContents(fallbackName, resolvedPath, contents string) Inspection {
	frontmatter, ok := extractSkillFrontmatter(contents)
	if !ok {
		return Inspection{Name: sanitizeSkillSingleLine(fallbackName), Path: resolvedPath, Reason: "missing or invalid frontmatter"}
	}
	var parsed skillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return Inspection{Name: sanitizeSkillSingleLine(fallbackName), Path: resolvedPath, Reason: "invalid frontmatter YAML"}
	}
	name := sanitizeSkillSingleLine(parsed.Name)
	if name == "" {
		name = sanitizeSkillSingleLine(fallbackName)
	}
	description := sanitizeSkillSingleLine(parsed.Description)
	if name == "" || description == "" {
		return Inspection{Name: name, Path: resolvedPath, Reason: "missing name or description"}
	}
	return Inspection{Name: name, Description: description, Path: resolvedPath, Loaded: true}
}

func extractSkillFrontmatter(contents string) (string, bool) {
	lines := strings.Split(contents, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}
	frontmatterLines := make([]string, 0)
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return strings.Join(frontmatterLines, "\n"), len(frontmatterLines) > 0
		}
		frontmatterLines = append(frontmatterLines, line)
	}
	return "", false
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
		key := normalizedSkillName(name)
		if key != "" {
			normalized[key] = true
		}
	}
	return normalized
}

func normalizedSkillName(raw string) string {
	return strings.ToLower(sanitizeSkillSingleLine(raw))
}

func sanitizeSkillSingleLine(raw string) string {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}
