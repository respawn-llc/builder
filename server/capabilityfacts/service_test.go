package capabilityfacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/auth"
	"core/server/onboardingimports"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestImportErrorFactsPreserveItemKind(t *testing.T) {
	command := onboardingimports.ItemKindCommand
	facts := importErrorFacts([]onboardingimports.Error{{
		Code:     "provider_discovery_failed",
		ItemKind: &command,
	}})
	if len(facts) != 1 || facts[0].ItemKind == nil {
		t.Fatalf("import error facts = %+v, want command item kind", facts)
	}
	if *facts[0].ItemKind != serverapi.ImportErrorItemKindCommand {
		t.Fatalf("item kind = %q, want command", *facts[0].ItemKind)
	}
}

func TestServiceProjectsModelCatalogAndUnknownFallback(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{Model: "gpt-5.6-sol"})})

	resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}

	if len(resp.Models.KnownModels) == 0 {
		t.Fatal("expected known model facts")
	}
	gpt56 := knownModelFact(resp.Models.KnownModels, "gpt-5.6-sol")
	if gpt56 == nil || !gpt56.Known || gpt56.LargeWindow != nil {
		t.Fatalf("gpt-5.6-sol fact = %+v, want known without a redundant large-window choice", gpt56)
	}
	gpt54 := knownModelFact(resp.Models.KnownModels, "gpt-5.4")
	if gpt54 == nil || gpt54.ContextWindowTokens == nil || gpt54.LargeWindow == nil || gpt54.LargeWindow.Tokens <= *gpt54.ContextWindowTokens {
		t.Fatalf("gpt-5.4 fact = %+v, want a strictly larger optional window", gpt54)
	}
	if gpt54.DefaultContextWindowMode == nil || *gpt54.DefaultContextWindowMode != "standard" {
		t.Fatalf("gpt-5.4 default context mode = %#v, want standard", gpt54.DefaultContextWindowMode)
	}

	fallback := resp.Models.UnknownFallback
	if fallback.Known || fallback.ModelID != nil || !fallback.SupportsThinking ||
		fallback.SupportsReasoningSummary || fallback.SupportsVisionInputs ||
		fallback.Verbosity.Source != "provider_default" || !fallback.Verbosity.Supported || len(fallback.Verbosity.Levels) == 0 {
		t.Fatalf("unknown fallback = %+v, want provider-default thinking and verbosity without catalog-only capabilities", fallback)
	}
}

func TestServiceProjectsProviderFactsAndExplicitProviders(t *testing.T) {
	settings := config.Settings{
		Model:            "gpt-5.6-sol",
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

func TestServiceProjectsProviderVerbosityIndependentlyOfFirstPartyClassification(t *testing.T) {
	tests := []struct {
		name                      string
		isOpenAIFirstParty        bool
		supportsProviderVerbosity bool
	}{
		{name: "enabled for non-first-party provider", isOpenAIFirstParty: false, supportsProviderVerbosity: true},
		{name: "disabled for first-party provider", isOpenAIFirstParty: true, supportsProviderVerbosity: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewService(Options{Config: testConfig(t, config.Settings{
				Model: "operator-alias",
				ProviderCapabilities: config.ProviderCapabilitiesOverride{
					ProviderID:                "custom-provider",
					IsOpenAIFirstParty:        tt.isOpenAIFirstParty,
					SupportsProviderVerbosity: tt.supportsProviderVerbosity,
				},
			})})

			resp, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
			if err != nil {
				t.Fatalf("GetCapabilityFacts: %v", err)
			}
			if got := resp.Providers.CurrentEffective.SupportsProviderVerbosity; got != tt.supportsProviderVerbosity {
				t.Fatalf("provider verbosity = %v, want %v, provider=%+v", got, tt.supportsProviderVerbosity, resp.Providers.CurrentEffective)
			}
		})
	}
}

func TestServiceRejectsUnsupportedExplicitProvider(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{Model: "gpt-5.6-sol"})})

	_, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{ExplicitLLMProviderIDs: []string{"missing-provider"}})
	if !errors.Is(err, serverapi.ErrUnsupportedProvider) {
		t.Fatalf("GetCapabilityFacts error = %v, want ErrUnsupportedProvider", err)
	}
}

func TestServiceRawAndTrustedCapabilityFactsUseTheSameOwner(t *testing.T) {
	service := NewService(Options{Config: testConfig(t, config.Settings{Model: "gpt-5.6-sol"})})
	request := serverapi.CapabilityFactsRequest{ExplicitLLMProviderIDs: []string{" openai ", "OPENAI"}}

	raw, err := service.GetCapabilityFacts(context.Background(), request)
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}
	trusted, err := apicontract.WithValidated(
		request,
		apicontract.SemanticValidationRequired,
		func(validated apicontract.Validated[serverapi.CapabilityFactsRequest]) (serverapi.CapabilityFactsResponse, error) {
			return service.GetCapabilityFactsValidated(context.Background(), validated)
		},
	)
	if err != nil {
		t.Fatalf("GetCapabilityFactsValidated: %v", err)
	}
	if len(raw.Providers.Explicit) != 1 || len(trusted.Providers.Explicit) != 1 ||
		raw.Providers.Explicit[0] != trusted.Providers.Explicit[0] {
		t.Fatalf("raw/trusted explicit provider facts differ: raw=%+v trusted=%+v", raw.Providers.Explicit, trusted.Providers.Explicit)
	}
}

func TestServiceConstructionRejectsInvalidStaticFacts(t *testing.T) {
	for name, settings := range map[string]config.Settings{
		"blank primary model": {},
	} {
		t.Run(name, func(t *testing.T) {
			service := NewService(Options{Config: testConfig(t, settings)})
			if _, err := service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{}); err == nil {
				t.Fatal("capability operation accepted invalid construction facts")
			}
		})
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
	if resp.Defaults.Thinking.Mode != "level" || resp.Defaults.Thinking.Level == nil || *resp.Defaults.Thinking.Level != "ultra" {
		t.Fatalf("thinking default = %+v, want level ultra", resp.Defaults.Thinking)
	}
	if resp.Defaults.Verbosity == nil || resp.Defaults.Verbosity.Level != string(config.ModelVerbosityHigh) {
		t.Fatalf("verbosity default = %+v", resp.Defaults.Verbosity)
	}
	if resp.Defaults.CompactionMode != string(config.CompactionModeNative) {
		t.Fatalf("compaction default = %q", resp.Defaults.CompactionMode)
	}
	service = NewService(Options{Config: testConfig(t, config.Settings{
		Model:            "custom-model",
		ProviderOverride: "openai",
	})})
	resp, err = service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
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
		Config:  testConfigAt(configRoot, config.Settings{Model: "gpt-5.6-sol"}),
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
	if err := os.MkdirAll(filepath.Join(configRoot, "skills", "existing"), 0o755); err != nil {
		t.Fatalf("mkdir existing skills target: %v", err)
	}
	resp, err = service.GetCapabilityFacts(context.Background(), serverapi.CapabilityFactsRequest{})
	if err != nil {
		t.Fatalf("GetCapabilityFacts: %v", err)
	}
	if !resp.Imports.Skills.Target.Skip || len(resp.Imports.Skills.Target.Conflicts) == 0 {
		t.Fatalf("expected skill target skip facts, got %+v", resp.Imports.Skills.Target)
	}
}

func TestServiceProjectsHomeResolutionFailureAsImportErrorFact(t *testing.T) {
	t.Setenv("HOME", "")
	service := NewService(Options{Config: testConfigAt(t.TempDir(), config.Settings{Model: "gpt-5.6-sol"})})

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

func TestServiceUsesSavedAuthWithoutRefreshingPreAuthFacts(t *testing.T) {
	state := auth.EmptyState()
	state.Method.Type = auth.MethodOAuth
	state.Method.OAuth = &auth.OAuthMethod{
		AccessToken:  "stale-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
		AccountID:    "account",
	}
	refresher := auth.NewOAuthRefresher(
		time.Now,
		time.Second,
		func(context.Context, auth.Method) (auth.Method, error) {
			return auth.Method{}, errors.New("refresh must not run for capability facts")
		},
	)
	service := NewService(Options{
		Config:      testConfig(t, config.Settings{Model: "gpt-5.6-sol"}),
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

func knownModelFact(facts []serverapi.ModelCapabilityFact, modelID string) *serverapi.ModelCapabilityFact {
	for idx := range facts {
		if facts[idx].ModelID != nil && *facts[idx].ModelID == modelID {
			return &facts[idx]
		}
	}
	return nil
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
