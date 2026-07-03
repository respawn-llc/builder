package onboardingimports

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/skillcatalog"
	brand "core/shared/config"
)

func TestDiscoverWithoutWorkspaceReturnsGlobalAndGeneratedFacts(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	writeProviderSkill(t, home, ProviderCodex, filepath.Join("skills", "local"), "review", "Review", "Review code")

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if result.Workspace.Root != nil {
		t.Fatalf("workspace root = %q, want absent", *result.Workspace.Root)
	}
	if len(result.Skills.Items) == 0 {
		t.Fatal("expected skill facts")
	}
	if len(result.SkillEnablement) == 0 {
		t.Fatal("expected skill enablement projections")
	}
	for _, item := range result.Skills.Items {
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

func TestDiscoverProviderFailureDoesNotSuppressOtherProviders(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	writeProviderSkill(t, home, ProviderCodex, filepath.Join("skills", "local"), "review", "Review", "Review code")
	claudeSkills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(filepath.Dir(claudeSkills), 0o755); err != nil {
		t.Fatalf("mkdir bad claude parent: %v", err)
	}
	if err := os.WriteFile(claudeSkills, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write bad claude root: %v", err)
	}

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !hasProviderItem(result.Skills.Items, ProviderCodex) {
		t.Fatalf("expected codex item despite claude failure, items=%+v errors=%+v", result.Skills.Items, result.Errors)
	}
	if !hasProviderError(result.Errors, ProviderClaudeCode) {
		t.Fatalf("expected claude provider error, got %+v", result.Errors)
	}
}

func TestDiscoverProviderSourceFailureDoesNotSuppressAlternateSourceRoot(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	badCodexLocal := filepath.Join(home, ".codex", "skills", "local")
	if err := os.MkdirAll(filepath.Dir(badCodexLocal), 0o755); err != nil {
		t.Fatalf("mkdir bad codex parent: %v", err)
	}
	if err := os.WriteFile(badCodexLocal, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write bad codex local root: %v", err)
	}
	writeProviderSkill(t, home, ProviderCodex, "skills", "fallback", "Fallback", "Fallback skill")

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !hasProviderItem(result.Skills.Items, ProviderCodex) {
		t.Fatalf("expected fallback codex skill despite local root failure, items=%+v errors=%+v", result.Skills.Items, result.Errors)
	}
	if !hasProviderError(result.Errors, ProviderCodex) {
		t.Fatalf("expected codex source error, got %+v", result.Errors)
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

func TestDiscoverReportsTargetProbeErrors(t *testing.T) {
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

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home, DisabledSkills: map[string]bool{"kent-dogfooding": true}})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, projection := range result.SkillEnablement {
		seen := map[string]bool{}
		for _, item := range projection.Candidates {
			if item.Ref.Name != nil {
				seen[*item.Ref.Name] = true
			}
			if item.Ref.Name != nil && *item.Ref.Name == "kent-dogfooding" && (item.DefaultEnabled == nil || *item.DefaultEnabled) {
				t.Fatalf("disabled generated skill default enabled: %+v", item)
			}
		}
		if projection.ChoiceRef.Mode == ChoiceModeSymlinkSource && seen["creating-skills"] {
			count := 0
			for _, item := range projection.Candidates {
				if item.Ref.Name != nil && *item.Ref.Name == "creating-skills" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("expected generated creating-skills shadowed by import choice, projection=%+v", projection)
			}
		}
	}
}

func TestGeneratedDisabledSkillRemainsCandidateWithDisabledDefault(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()

	result, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home, DisabledSkills: map[string]bool{"kent-dogfooding": true}})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	found := false
	for _, projection := range result.SkillEnablement {
		if projection.ChoiceRef.Mode != ChoiceModeNone {
			continue
		}
		for _, item := range projection.Candidates {
			if item.Ref.Name == nil || *item.Ref.Name != "kent-dogfooding" {
				continue
			}
			found = true
			if item.DefaultEnabled == nil || *item.DefaultEnabled {
				t.Fatalf("expected disabled generated skill to be present with default disabled, got %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("expected disabled generated skill candidate, projections=%+v", result.SkillEnablement)
	}
}

func TestDiscoverRecomputesFilesystemState(t *testing.T) {
	configRoot := t.TempDir()
	home := t.TempDir()
	first, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	writeProviderSkill(t, home, ProviderCodex, filepath.Join("skills", "local"), "review", "Review", "Review code")
	second, err := Discover(Options{ConfigRoot: configRoot, HomeDir: home})
	if err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if len(second.Skills.Items) <= len(first.Skills.Items) {
		t.Fatalf("expected recomputed skill item count to grow, first=%d second=%d", len(first.Skills.Items), len(second.Skills.Items))
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

var _ = brand.ConfigDirName
