package capabilityfacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestServiceProjectsModelCatalogAndUnknownFallback(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{Model: "gpt-5.5"})})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if len(resp.Models.KnownModels) == 0 {
		t.Fatal("expected known model facts")
	}
	seen := map[string]bool{}
	var modelWithLargeWindow *serverapi.ModelCapabilityFact
	for idx, fact := range resp.Models.KnownModels {
		if fact.ModelID == nil || *fact.ModelID == "" {
			t.Fatalf("known model %d has no model id: %+v", idx, fact)
		}
		if seen[*fact.ModelID] {
			t.Fatalf("duplicate model id %q", *fact.ModelID)
		}
		seen[*fact.ModelID] = true
		if !fact.Known {
			t.Fatalf("known model %q marked unknown", *fact.ModelID)
		}
		if fact.ContextWindowTokens != nil && *fact.ContextWindowTokens <= 0 {
			t.Fatalf("model %q has non-positive context window fact: %d", *fact.ModelID, *fact.ContextWindowTokens)
		}
		if fact.LargeWindow != nil {
			if fact.LargeWindow.Tokens <= 0 {
				t.Fatalf("model %q has non-positive large window fact: %d", *fact.ModelID, fact.LargeWindow.Tokens)
			}
			modelWithLargeWindow = &resp.Models.KnownModels[idx]
		}
	}
	if !seen["gpt-5.5"] {
		t.Fatalf("known model catalog missing gpt-5.5: %#v", seen)
	}
	if modelWithLargeWindow == nil {
		t.Fatal("expected at least one model with large-window facts")
	}
	if modelWithLargeWindow.DefaultContextWindowMode == nil || *modelWithLargeWindow.DefaultContextWindowMode != "standard" {
		t.Fatalf("large-window model default context mode = %#v, want standard", modelWithLargeWindow.DefaultContextWindowMode)
	}

	fallback := resp.Models.UnknownFallback
	if fallback.Known {
		t.Fatalf("unknown fallback marked known: %+v", fallback)
	}
	if fallback.ModelID != nil {
		t.Fatalf("unknown fallback model id = %#v, want nil", fallback.ModelID)
	}
	if !fallback.SupportsThinking {
		t.Fatalf("unknown fallback should support thinking: %+v", fallback)
	}
	if fallback.SupportsReasoningSummary || fallback.SupportsVisionInputs {
		t.Fatalf("unknown fallback exposes catalog-only capabilities: %+v", fallback)
	}
	if fallback.Verbosity.Source != "provider_default" {
		t.Fatalf("unknown fallback verbosity source = %q", fallback.Verbosity.Source)
	}
	if !fallback.Verbosity.Supported || len(fallback.Verbosity.Levels) == 0 {
		t.Fatalf("unknown fallback should use first-party provider verbosity defaults: %+v", fallback.Verbosity)
	}
}

func TestServiceProjectsProviderFactsAndExplicitProviders(t *testing.T) {
	settings := config.Settings{
		Model:            "gpt-5.5",
		ProviderOverride: "openai",
		OpenAIBaseURL:    "https://api.compatible.example/v1",
	}
	service := NewService(Options{Config: testConfig(t, settings)})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{ExplicitLLMProviderIDs: []string{"openai", "OPENAI"}})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if resp.Providers.CurrentEffective == nil {
		t.Fatal("expected current effective provider facts")
	}
	if got := resp.Providers.CurrentEffective.LLMProviderID; got != "openai-compatible" {
		t.Fatalf("current provider id = %q, want openai-compatible", got)
	}
	if resp.Providers.CurrentEffective.Role != "current_effective" {
		t.Fatalf("current provider role = %q", resp.Providers.CurrentEffective.Role)
	}
	if resp.Providers.CurrentEffective.SupportsProviderVerbosity {
		t.Fatal("remote compatible provider must not expose provider-default verbosity")
	}
	if len(resp.Providers.Explicit) != 1 {
		t.Fatalf("explicit providers = %d, want deduplicated one", len(resp.Providers.Explicit))
	}
	if got := resp.Providers.Explicit[0].LLMProviderID; got != "openai" {
		t.Fatalf("explicit provider id = %q, want openai", got)
	}
	if resp.Providers.Explicit[0].Role != "explicit_catalog" {
		t.Fatalf("explicit provider role = %q", resp.Providers.Explicit[0].Role)
	}
	if !resp.Providers.Explicit[0].SupportsProviderVerbosity {
		t.Fatal("first-party explicit provider should expose provider-default verbosity")
	}
}

func TestServiceRejectsUnsupportedExplicitProvider(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{Model: "gpt-5.5"})})

	_, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{ExplicitLLMProviderIDs: []string{"missing-provider"}})
	if !errors.Is(err, serverapi.ErrUnsupportedProvider) {
		t.Fatalf("GetCapabilityFacts error = %v, want ErrUnsupportedProvider", err)
	}
}

func TestServiceProjectsDefaults(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{
		Model:            "custom-model",
		ProviderOverride: "openai",
		ThinkingLevel:    "ultra",
		ModelVerbosity:   config.ModelVerbosityHigh,
		CompactionMode:   config.CompactionModeNative,
	})})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if resp.Defaults.PrimaryModelID != "custom-model" {
		t.Fatalf("primary model = %q", resp.Defaults.PrimaryModelID)
	}
	if resp.Defaults.Thinking.Mode != "custom" || resp.Defaults.Thinking.Value == nil || *resp.Defaults.Thinking.Value != "ultra" {
		t.Fatalf("thinking default = %+v, want custom ultra", resp.Defaults.Thinking)
	}
	if resp.Defaults.Verbosity == nil || resp.Defaults.Verbosity.Level != string(config.ModelVerbosityHigh) {
		t.Fatalf("verbosity default = %+v", resp.Defaults.Verbosity)
	}
	if resp.Defaults.CompactionMode != string(config.CompactionModeNative) {
		t.Fatalf("compaction default = %q", resp.Defaults.CompactionMode)
	}
}

func TestServiceProjectsAbsentVerbosityDefaultAsNull(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{
		Model:            "custom-model",
		ProviderOverride: "openai",
	})})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if resp.Defaults.Verbosity != nil {
		t.Fatalf("verbosity default = %+v, want nil", resp.Defaults.Verbosity)
	}
}

func TestServiceProjectsImportDomainFacts(t *testing.T) {
	home := t.TempDir()
	configRoot := t.TempDir()
	writeProviderSkill(t, home, ".claude", "skills", "helper", "Helper")
	service := NewService(Options{
		Config:  testConfigAt(configRoot, config.Settings{Model: "gpt-5.5"}),
		HomeDir: home,
	})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if len(resp.Imports.Skills.Choices) < 2 {
		t.Fatalf("expected none plus provider skill import choice, got %+v", resp.Imports.Skills.Choices)
	}
	if resp.Imports.Recommendations.Skills == nil || resp.Imports.Recommendations.Skills.ItemCount != 1 {
		t.Fatalf("skill recommendation = %+v", resp.Imports.Recommendations.Skills)
	}
	var found bool
	for _, item := range resp.Imports.Skills.Items {
		if item.Ref.ImportProviderID != nil && *item.Ref.ImportProviderID == "claude_code" && item.Ref.TargetName == "helper" {
			found = true
		}
	}
	if !found {
		t.Fatalf("projected skill items missing claude_code helper: %+v", resp.Imports.Skills.Items)
	}
	if len(resp.Imports.SkillEnablement) == 0 {
		t.Fatal("expected skill enablement projections")
	}
}

func TestServiceProjectsImportTargetSkipFacts(t *testing.T) {
	home := t.TempDir()
	configRoot := t.TempDir()
	writeProviderSkill(t, home, ".claude", "skills", "helper", "Helper")
	if err := os.MkdirAll(filepath.Join(configRoot, "skills", "existing"), 0o755); err != nil {
		t.Fatalf("mkdir existing skills target: %v", err)
	}
	service := NewService(Options{
		Config:  testConfigAt(configRoot, config.Settings{Model: "gpt-5.5"}),
		HomeDir: home,
	})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}
	if !resp.Imports.Skills.Target.Skip || len(resp.Imports.Skills.Target.Conflicts) == 0 {
		t.Fatalf("expected skill target skip facts, got %+v", resp.Imports.Skills.Target)
	}
}

func TestServiceProjectsHomeResolutionFailureAsImportErrorFact(t *testing.T) {
	t.Setenv("HOME", "")
	service := NewService(Options{Config: testConfigAt(t.TempDir(), config.Settings{Model: "gpt-5.5"})})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}
	if len(resp.Models.KnownModels) == 0 {
		t.Fatal("expected non-import fact groups to still be projected")
	}
	var found bool
	for _, importErr := range resp.Imports.Errors {
		if importErr.Code == "home_dir_resolution_failed" && importErr.Operation == "resolve_home_dir" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected home-dir import error fact, got %+v", resp.Imports.Errors)
	}
}

func TestServiceUsesSavedAuthWhenAvailable(t *testing.T) {
	state := auth.EmptyState()
	state.Method.Type = auth.MethodOAuth
	state.Method.OAuth = &auth.OAuthMethod{AccessToken: "token", AccountID: "account"}
	service := NewService(Options{
		Config:      testConfig(t, config.Settings{Model: "gpt-5.5"}),
		AuthManager: auth.NewManager(auth.NewMemoryStore(state), nil, nil),
	})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if resp.Providers.CurrentEffective == nil || resp.Providers.CurrentEffective.LLMProviderID != "chatgpt-codex" {
		t.Fatalf("current provider = %+v, want chatgpt-codex", resp.Providers.CurrentEffective)
	}
}

func TestServiceDoesNotRefreshAuthForPreAuthFacts(t *testing.T) {
	state := auth.EmptyState()
	state.Method.Type = auth.MethodOAuth
	state.Method.OAuth = &auth.OAuthMethod{
		AccessToken:  "stale-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
		AccountID:    "account",
	}
	refresher := auth.NewOAuthRefresher(nil, time.Now, time.Second)
	refresher.Refresh = func(context.Context, auth.Method) (auth.Method, error) {
		return auth.Method{}, errors.New("refresh must not run for capability facts")
	}
	service := NewService(Options{
		Config:      testConfig(t, config.Settings{Model: "gpt-5.5"}),
		AuthManager: auth.NewManager(auth.NewMemoryStore(state), refresher, time.Now),
	})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if resp.Providers.CurrentEffective == nil || resp.Providers.CurrentEffective.LLMProviderID != "chatgpt-codex" {
		t.Fatalf("current provider = %+v, want chatgpt-codex", resp.Providers.CurrentEffective)
	}
}

func testConfig(t *testing.T, settings config.Settings) config.App {
	t.Helper()
	return testConfigAt(t.TempDir(), settings)
}

func testConfigAt(root string, settings config.Settings) config.App {
	return config.App{PersistenceRoot: root, Settings: settings}
}

func writeProviderSkill(t *testing.T, home string, providerHome string, sourceDir string, dirName string, name string) {
	t.Helper()
	path := filepath.Join(home, providerHome, sourceDir, dirName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: helper\n---\n"
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}
