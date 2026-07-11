package prompts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type generatedSkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func TestGeneratedSkillsAreValid(t *testing.T) {
	entries, err := fs.ReadDir(GeneratedSkillsFS, "skills")
	if err != nil {
		t.Fatalf("read generated skills: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one generated skill")
	}

	seenNames := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("generated skills root contains non-directory %q", entry.Name())
		}
		skillPath := filepath.ToSlash(filepath.Join("skills", entry.Name(), "SKILL.md"))
		contents, err := fs.ReadFile(GeneratedSkillsFS, skillPath)
		if err != nil {
			t.Fatalf("read %s: %v", skillPath, err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			t.Fatalf("%s must not be empty", skillPath)
		}
		frontmatter, body, ok := splitGeneratedSkillFrontmatter(string(contents))
		if !ok {
			t.Fatalf("%s must contain YAML frontmatter", skillPath)
		}
		var parsed generatedSkillFrontmatter
		if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
			t.Fatalf("%s frontmatter YAML: %v", skillPath, err)
		}
		name := strings.Join(strings.Fields(parsed.Name), " ")
		if name == "" {
			t.Fatalf("%s frontmatter name must not be empty", skillPath)
		}
		if strings.Join(strings.Fields(parsed.Description), " ") == "" {
			t.Fatalf("%s frontmatter description must not be empty", skillPath)
		}
		if strings.Join(strings.Fields(body), " ") == "" {
			t.Fatalf("%s body must not be empty", skillPath)
		}
		if previous, ok := seenNames[strings.ToLower(name)]; ok {
			t.Fatalf("generated skill name %q is duplicated by %s and %s", name, previous, skillPath)
		}
		seenNames[strings.ToLower(name)] = skillPath
	}
}

func TestGeneratedSkillsContainOnlyRegularFilesAndDirectories(t *testing.T) {
	if err := fs.WalkDir(GeneratedSkillsFS, "skills", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode.IsDir() || mode.IsRegular() {
			return nil
		}
		t.Fatalf("generated skills contain unsupported entry %s mode %s", path, mode)
		return nil
	}); err != nil {
		t.Fatalf("walk generated skills: %v", err)
	}
}

func TestGeneratedSkillsEmbedCommittedSources(t *testing.T) {
	entries, err := fs.ReadDir(GeneratedSkillsFS, "skills")
	if err != nil {
		t.Fatalf("read generated skills: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.ToSlash(filepath.Join("skills", entry.Name(), "SKILL.md"))
		embedded, err := fs.ReadFile(GeneratedSkillsFS, path)
		if err != nil {
			t.Fatalf("read embedded %s: %v", path, err)
		}
		source, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			t.Fatalf("read committed %s: %v", path, err)
		}
		if string(embedded) != string(source) {
			t.Fatalf("embedded skill %s does not match committed source", path)
		}
	}
}

func splitGeneratedSkillFrontmatter(contents string) (string, string, bool) {
	lines := strings.Split(contents, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}
	for idx, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			frontmatter := strings.Join(lines[1:idx+1], "\n")
			body := strings.Join(lines[idx+2:], "\n")
			return frontmatter, body, strings.TrimSpace(frontmatter) != ""
		}
	}
	return "", "", false
}
