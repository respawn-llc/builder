package skillcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brand "core/shared/config"
)

func TestDiscoverOrdersRootsAndReadsEmbeddedGeneratedWhenUnseeded(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	globalSkillPath := writeSkill(t, filepath.Join(root, SkillsDirName, "global-skill"), "global-skill", "global")
	workspaceSkillPath := writeSkill(t, filepath.Join(workspace, brand.ConfigDirName, SkillsDirName, "workspace-skill"), "workspace-skill", "workspace")

	result, err := Discover(Options{WorkspaceRoot: workspace, ConfigRoot: root, IncludeEmbeddedGenerated: true})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.Roots) != 3 {
		t.Fatalf("roots = %+v, want global/workspace/generated", result.Roots)
	}
	if result.Roots[0].Kind != SourceKindGlobal || result.Roots[1].Kind != SourceKindWorkspace || result.Roots[2].Kind != SourceKindGenerated {
		t.Fatalf("root order = %+v", result.Roots)
	}
	requireSkillPath(t, result.Skills, canonicalSlashPath(t, globalSkillPath))
	requireSkillPath(t, result.Skills, canonicalSlashPath(t, workspaceSkillPath))
	generatedRoot := filepath.Join(root, ".generated", SkillsDirName)
	foundGenerated := false
	for _, skill := range result.Skills {
		if skill.SourceKind == SourceKindGenerated {
			foundGenerated = true
			if !strings.HasPrefix(skill.Path, filepath.ToSlash(generatedRoot)+"/") {
				t.Fatalf("generated skill path = %q, want projected under %q", skill.Path, generatedRoot)
			}
		}
	}
	if !foundGenerated {
		t.Fatalf("expected embedded generated skills, got %+v", result.Skills)
	}
}

func TestDiscoverGeneratedShadowedByUserSkillAndDisabled(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	writeSkill(t, filepath.Join(workspace, brand.ConfigDirName, SkillsDirName, "skill-creator"), "skill-creator", "workspace")
	writeSkill(t, filepath.Join(root, ".generated", SkillsDirName, "skill-creator"), "skill-creator", "generated")

	result, err := Discover(Options{WorkspaceRoot: workspace, ConfigRoot: root, DisabledSkills: map[string]bool{"skill-creator": true}})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, skill := range result.Skills {
		if strings.EqualFold(skill.Name, "skill-creator") {
			t.Fatalf("disabled/shadowed skill loaded: %+v", skill)
		}
	}
	var generated Inspection
	for _, inspection := range result.Inspections {
		if inspection.SourceKind == SourceKindGenerated && inspection.Name == "skill-creator" {
			generated = inspection
			break
		}
	}
	if !generated.Loaded || !generated.Shadowed || !generated.Disabled {
		t.Fatalf("generated inspection = %+v, want loaded shadowed disabled", generated)
	}
}

func TestDiscoverDeduplicatesResolvedSkillPaths(t *testing.T) {
	root := t.TempDir()
	targetSkillPath := writeSkill(t, filepath.Join(t.TempDir(), "shared", "linked"), "linked", "shared")
	firstLink := filepath.Join(root, SkillsDirName, "first")
	secondLink := filepath.Join(root, SkillsDirName, "second")
	if err := os.MkdirAll(filepath.Dir(firstLink), 0o755); err != nil {
		t.Fatalf("mkdir links: %v", err)
	}
	if err := os.Symlink(filepath.Dir(targetSkillPath), firstLink); err != nil {
		t.Fatalf("symlink first: %v", err)
	}
	if err := os.Symlink(filepath.Dir(targetSkillPath), secondLink); err != nil {
		t.Fatalf("symlink second: %v", err)
	}

	result, err := Discover(Options{ConfigRoot: root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.Skills) != 1 {
		t.Fatalf("loaded skills = %+v, want exactly one resolved path", result.Skills)
	}
	duplicates := 0
	for _, inspection := range result.Inspections {
		if inspection.Reason == "duplicate resolved SKILL.md path" {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate inspections = %d, inspections=%+v", duplicates, result.Inspections)
	}
}

func TestDiscoverReportsBrokenSymlinkedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, SkillsDirName)
	brokenLinkPath := filepath.Join(skillsRoot, "broken-skill")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatalf("mkdir skills root: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-skill-dir"), brokenLinkPath); err != nil {
		t.Fatalf("symlink broken skill dir: %v", err)
	}

	result, err := Discover(Options{ConfigRoot: root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.Issues) != 1 || result.Issues[0].Path != filepath.ToSlash(brokenLinkPath) || strings.TrimSpace(result.Issues[0].Reason) == "" {
		t.Fatalf("broken symlink issues = %+v, want one structured issue", result.Issues)
	}
}

func canonicalSlashPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks for %q: %v", path, err)
	}
	return filepath.ToSlash(resolved)
}

func TestDiscoverReportsUnreadableRootAsStructuredError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, SkillsDirName), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write non-directory skills root: %v", err)
	}
	_, err := Discover(Options{ConfigRoot: root})
	if !errors.Is(err, ErrReadSkillsDirectory) {
		t.Fatalf("Discover error = %v, want ErrReadSkillsDirectory", err)
	}
}

func requireSkillPath(t *testing.T, skills []Skill, path string) {
	t.Helper()
	for _, skill := range skills {
		if skill.Path == path {
			return
		}
	}
	t.Fatalf("skill path %q not found in %+v", path, skills)
}

func writeSkill(t *testing.T, dir, name, description string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	path := filepath.Join(dir, SkillFileName)
	contents := "---\nname: " + name + "\ndescription: " + description + "\n---\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return path
}
