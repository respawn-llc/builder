package onboardingimports

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/skillcatalog"
	brand "core/shared/config"
)

func TestDiscoverWithoutWorkspaceReturnsGlobalAndGeneratedFactsAndRecomputes(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	first, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	if first.Workspace.Root != nil {
		t.Fatalf("workspace root = %q, want absent", *first.Workspace.Root)
	}
	if len(first.Skills.Items) == 0 || len(first.SkillEnablement) == 0 {
		t.Fatalf("expected generated skill facts without a workspace: %+v", first.Skills)
	}

	writeProviderSkill(t, home, ProviderCodex, filepath.Join("skills", "local"), "review", "Review", "Review code")
	second, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if len(second.Skills.Items) <= len(first.Skills.Items) {
		t.Fatalf("expected recomputed skill item count to grow, first=%d second=%d", len(first.Skills.Items), len(second.Skills.Items))
	}
	for _, item := range second.Skills.Items {
		if item.Ref.SourceKind == SourceKindExternalProvider && item.Ref.ProviderID != nil && *item.Ref.ProviderID != ProviderCodex {
			t.Fatalf("unexpected provider item: %+v", item)
		}
	}
}

func TestDiscoverInvalidWorkspaceReturnsStructuredErrorAndContinues(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	badWorkspace := filepath.Join(t.TempDir(), "missing")
	writeProviderSkill(t, home, ProviderClaudeCode, "skills", "review", "Review", "Review code")

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home, WorkspaceRoot: &badWorkspace})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Workspace.Root != nil {
		t.Fatalf("workspace root = %q, want absent", *result.Workspace.Root)
	}
	if !hasErrorScope(result.Errors, ErrorScopeWorkspace) {
		t.Fatalf("expected workspace error, got %+v", result.Errors)
	}
	if len(result.Skills.Items) == 0 {
		t.Fatal("expected global/provider facts despite invalid workspace")
	}
}

func TestDiscoverProviderFailuresRemainScoped(t *testing.T) {
	tests := []struct {
		name          string
		badPath       string
		source        string
		itemProvider  ProviderID
		errorProvider ProviderID
	}{
		{name: "other providers continue", badPath: filepath.Join(".claude", "skills"), source: filepath.Join("skills", "local"), itemProvider: ProviderCodex, errorProvider: ProviderClaudeCode},
		{name: "alternate source roots continue", badPath: filepath.Join(".codex", "skills", "local"), source: "skills", itemProvider: ProviderCodex, errorProvider: ProviderCodex},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configRoot := t.TempDir()
			home := t.TempDir()
			badPath := filepath.Join(home, tt.badPath)
			if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
				t.Fatalf("mkdir bad provider parent: %v", err)
			}
			if err := os.WriteFile(badPath, []byte("not a directory"), 0o644); err != nil {
				t.Fatalf("write bad provider root: %v", err)
			}
			writeProviderSkill(t, home, tt.itemProvider, tt.source, "available", "Available", "Available skill")

			result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if !hasProviderItem(result.Skills.Items, tt.itemProvider) {
				t.Fatalf("expected available provider item, items=%+v errors=%+v", result.Skills.Items, result.Errors)
			}
			if !hasProviderError(result.Errors, tt.errorProvider) {
				t.Fatalf("expected scoped provider error, got %+v", result.Errors)
			}
		})
	}
}

func TestDiscoverReportsTargetSkipState(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	writeProviderSkill(t, home, ProviderCodex, filepath.Join("skills", "local"), "review", "Review", "Review code")
	if err := os.MkdirAll(filepath.Join(configRoot, "skills", "existing"), 0o755); err != nil {
		t.Fatalf("mkdir existing skills target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configRoot, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "prompts", "review.md"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing prompt: %v", err)
	}

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Skills.Target.Skip || len(result.Skills.Target.Conflicts) == 0 {
		t.Fatalf("expected skill target skip conflict, got %+v", result.Skills.Target)
	}
	if !result.Commands.Target.Skip || len(result.Commands.Target.Conflicts) == 0 {
		t.Fatalf("expected command target skip conflict, got %+v", result.Commands.Target)
	}
	if result.Recommendations.Skills == nil || result.Recommendations.Skills.ChoiceRef.Mode != ChoiceModeNone {
		t.Fatalf("expected skipped skill target to recommend none, got %+v", result.Recommendations.Skills)
	}
}

func TestDiscoverWorkspaceLocalTargetsDoNotSkipGlobalImports(t *testing.T) {
	configRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	writeProviderSkill(t, home, ProviderCodex, filepath.Join("skills", "local"), "review", "Review", "Review code")
	writeProviderCommand(t, home, ProviderCodex, "prompts", "review.md")
	if err := os.MkdirAll(filepath.Join(workspaceRoot, brand.ConfigDirName, "skills", "existing"), 0o755); err != nil {
		t.Fatalf("mkdir workspace skill target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, brand.ConfigDirName, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir workspace prompt target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, brand.ConfigDirName, "prompts", "review.md"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write workspace prompt target: %v", err)
	}

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home, WorkspaceRoot: &workspaceRoot})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Skills.Target.Skip || len(result.Skills.Target.Conflicts) != 0 {
		t.Fatalf("workspace skill target blocked global import: %+v", result.Skills.Target)
	}
	if result.Commands.Target.Skip || len(result.Commands.Target.Conflicts) != 0 {
		t.Fatalf("workspace command target blocked global import: %+v", result.Commands.Target)
	}
	if result.Recommendations.Skills == nil || result.Recommendations.Skills.ChoiceRef.Mode == ChoiceModeNone {
		t.Fatalf("expected skill import recommendation, got %+v", result.Recommendations.Skills)
	}
	if result.Recommendations.Commands == nil || result.Recommendations.Commands.ChoiceRef.Mode == ChoiceModeNone {
		t.Fatalf("expected command import recommendation, got %+v", result.Recommendations.Commands)
	}
}

func TestDiscoverCommandsCountsFollowedRegularMarkdownFiles(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	writeProviderCommand(t, home, ProviderCodex, "prompts", "regular.md")
	root := filepath.Join(home, ".codex", "prompts")
	if err := os.WriteFile(filepath.Join(root, "blank.md"), []byte(" \n\t"), 0o644); err != nil {
		t.Fatalf("write blank command: %v", err)
	}
	targetDir := filepath.Join(t.TempDir(), "command-directory")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir command target directory: %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(root, "directory.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	targetFile := filepath.Join(t.TempDir(), "linked.md")
	if err := os.WriteFile(targetFile, []byte("linked command"), 0o644); err != nil {
		t.Fatalf("write linked command: %v", err)
	}
	if err := os.Symlink(targetFile, filepath.Join(root, "linked.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.Commands.Items) != 3 {
		t.Fatalf("command items = %+v, want regular files and regular-file symlinks regardless of content", result.Commands.Items)
	}
	foundBlank := false
	for _, item := range result.Commands.Items {
		if item.Ref.TargetName == "directory.md" {
			t.Fatalf("directory symlink counted as command: %+v", item)
		}
		foundBlank = foundBlank || item.Ref.TargetName == "blank.md"
	}
	if !foundBlank {
		t.Fatalf("blank regular Markdown file was not counted: %+v", result.Commands.Items)
	}
}

func TestDiscoverReportsTargetProbeErrorsAndKeepsGeneratedSkills(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	target := filepath.Join(configRoot, "skills")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Chmod(target, 0); err != nil {
		t.Fatalf("chmod target: %v", err)
	}
	defer func() {
		_ = os.Chmod(target, 0o755)
	}()

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !result.Skills.Target.Skip {
		t.Fatalf("expected conservative target skip on read failure, got %+v", result.Skills.Target)
	}
	if !hasErrorScope(result.Errors, ErrorScopeTarget) {
		t.Fatalf("expected structured target error, got %+v", result.Errors)
	}
	if !hasSourceKindItem(result.Skills.Items, SourceKindGenerated) {
		t.Fatalf("expected generated skill facts despite target failure, items=%+v errors=%+v", result.Skills.Items, result.Errors)
	}
}

func TestValidateChoiceUsesStructuredRefs(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	writeProviderSkill(t, home, ProviderAgents, "skills", "review", "Review", "Review code")
	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.Skills.Choices) < 2 {
		t.Fatalf("expected symlink choice, got %+v", result.Skills.Choices)
	}
	if err := ValidateChoice(result, result.Skills.Choices[1].Ref, ItemKindSkill); err != nil {
		t.Fatalf("valid choice rejected: %v", err)
	}
	missing := ChoiceRef{Mode: ChoiceModeSymlinkSource}
	if err := ValidateChoice(result, missing, ItemKindSkill); !errors.Is(err, ErrInvalidChoice) {
		t.Fatalf("invalid choice err = %v, want ErrInvalidChoice", err)
	}
}

func TestSkillEnablementProjectionsShadowGeneratedAndMarkDefaults(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	writeProviderSkill(t, home, ProviderAgents, "skills", "creating-skills", "creating-skills", "External override")

	result, err := Discover(Options{
		ConfigRoot:  configRoot,
		HomeDir:     home,
		SkillPolicy: brand.ResolveSkillPolicy(brand.Settings{SkillToggles: map[string]bool{"kent-dogfooding": false}}),
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	disabledGeneratedFound := false
	shadowedGeneratedFound := false
	for _, projection := range result.SkillEnablement {
		creatingSkills := 0
		for _, item := range projection.Candidates {
			if item.Ref.Name == nil {
				continue
			}
			if projection.ChoiceRef.Mode == ChoiceModeNone && *item.Ref.Name == "kent-dogfooding" {
				disabledGeneratedFound = true
				if item.DefaultEnabled == nil || *item.DefaultEnabled {
					t.Fatalf("disabled generated skill default enabled: %+v", item)
				}
			}
			if projection.ChoiceRef.Mode == ChoiceModeSymlinkSource && *item.Ref.Name == "creating-skills" {
				creatingSkills++
			}
		}
		if creatingSkills > 0 {
			if creatingSkills != 1 {
				t.Fatalf("expected imported creating-skills to shadow its generated candidate, projection=%+v", projection)
			}
			shadowedGeneratedFound = true
		}
	}
	if !disabledGeneratedFound || !shadowedGeneratedFound {
		t.Fatalf("skill enablement projections did not preserve disabled or shadowed generated candidates: %+v", result.SkillEnablement)
	}
}

func writeProviderSkill(t *testing.T, home string, provider ProviderID, sourceCandidate, dirName, name, description string) {
	t.Helper()
	providerHome := map[ProviderID]string{ProviderClaudeCode: ".claude", ProviderCodex: ".codex", ProviderAgents: ".agents"}[provider]
	dir := filepath.Join(home, providerHome, sourceCandidate, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir provider skill: %v", err)
	}
	path := filepath.Join(dir, skillcatalog.SkillFileName)
	contents := "---\nname: " + name + "\ndescription: " + description + "\n---\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write provider skill: %v", err)
	}
}

func writeProviderCommand(t *testing.T, home string, provider ProviderID, sourceCandidate, fileName string) {
	t.Helper()
	providerHome := map[ProviderID]string{ProviderClaudeCode: ".claude", ProviderCodex: ".codex", ProviderAgents: ".agents"}[provider]
	dir := filepath.Join(home, providerHome, sourceCandidate)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir provider command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("command"), 0o644); err != nil {
		t.Fatalf("write provider command: %v", err)
	}
}

func hasErrorScope(errors []Error, scope ErrorScope) bool {
	for _, err := range errors {
		if err.Scope == scope {
			return true
		}
	}
	return false
}

func hasProviderItem(items []Item, provider ProviderID) bool {
	for _, item := range items {
		if item.Ref.ProviderID != nil && *item.Ref.ProviderID == provider {
			return true
		}
	}
	return false
}

func hasProviderError(errors []Error, provider ProviderID) bool {
	for _, err := range errors {
		if err.ProviderID != nil && *err.ProviderID == provider {
			return true
		}
	}
	return false
}

func hasSourceKindItem(items []Item, sourceKind SourceKind) bool {
	for _, item := range items {
		if item.Ref.SourceKind == sourceKind {
			return true
		}
	}
	return false
}
