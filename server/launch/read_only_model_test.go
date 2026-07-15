package launch

import (
	"errors"
	"reflect"
	"testing"

	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestReadOnlyModelUsesLockedModelWithoutBackfill(t *testing.T) {
	meta := session.Meta{
		Locked: &session.LockedContract{
			Model: "gpt-locked-model",
		},
	}
	before := meta
	resolved, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{Model: "configured-model"},
	}, meta)
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Name != "gpt-locked-model" || !resolved.Locked {
		t.Fatalf("resolved model = %+v, want locked model", resolved)
	}
	if resolved.Provider.ID() != "openai" {
		t.Fatalf("legacy locked provider = %q, want inferred openai", resolved.Provider.ID())
	}
	if meta.Locked != before.Locked || meta.Locked.Model != before.Locked.Model {
		t.Fatal("read-only model resolution mutated locked metadata")
	}
}

func TestReadOnlyModelUsesLockedProviderContract(t *testing.T) {
	meta := session.Meta{
		Locked: &session.LockedContract{
			Model: "provider-owned-model",
			ProviderContract: session.LockedProviderCapabilities{
				ProviderID: "anthropic",
			},
		},
	}
	resolved, err := ResolveReadOnlySessionModel(config.App{}, meta)
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Provider.ID() != "anthropic" {
		t.Fatalf("resolved provider = %q, want anthropic", resolved.Provider.ID())
	}
}

func TestReadOnlyModelUsesCurrentConfigAndContinuationRole(t *testing.T) {
	app := config.App{
		Settings: config.Settings{
			Model: "gpt-configured-model",
			Subagents: map[string]config.SubagentRole{
				"worker": {
					Settings: config.Settings{Model: "gpt-worker-model"},
					Sources:  map[string]string{"model": "workspace"},
				},
			},
		},
	}
	role := "worker"
	meta := session.Meta{
		Continuation: &session.ContinuationContext{AgentRole: &role},
	}
	resolved, err := ResolveReadOnlySessionModel(app, meta)
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Name != "gpt-worker-model" || resolved.Locked {
		t.Fatalf("resolved model = %+v, want unlocked worker model", resolved)
	}
	if resolved.Provider.ID() != "openai" {
		t.Fatalf("resolved worker provider = %q, want inferred openai", resolved.Provider.ID())
	}
}

func TestReadOnlyModelContinuationRoleInheritsBaseModel(t *testing.T) {
	role := "worker"
	resolved, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{
			Model: "gpt-base-model",
			Subagents: map[string]config.SubagentRole{
				role: {
					Settings: config.Settings{ThinkingLevel: "high"},
					Sources:  map[string]string{"thinking_level": "workspace"},
				},
			},
		},
	}, session.Meta{
		Continuation: &session.ContinuationContext{AgentRole: &role},
	})
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Name != "gpt-base-model" || resolved.Provider.ID() != "openai" {
		t.Fatalf("inherited role model = %+v provider=%q", resolved, resolved.Provider.ID())
	}
}

func TestReadOnlyModelAppliesFastRoleHeuristics(t *testing.T) {
	role := config.BuiltInSubagentRoleFast
	resolved, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{Model: "gpt-base-model"},
	}, session.Meta{
		Continuation: &session.ContinuationContext{AgentRole: &role},
	})
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Name != "gpt-5.6-terra" || resolved.Provider.ID() != "openai" {
		t.Fatalf("fast role model = %+v provider=%q", resolved, resolved.Provider.ID())
	}
}

func TestReadOnlyModelRejectsLegacyPartialContractWithoutRepair(t *testing.T) {
	meta := session.Meta{
		Locked: &session.LockedContract{},
	}
	before := *meta.Locked
	if _, err := ResolveReadOnlySessionModel(config.App{}, meta); err == nil {
		t.Fatal("ResolveReadOnlySessionModel accepted a locked contract without a model")
	}
	if !reflect.DeepEqual(*meta.Locked, before) {
		t.Fatalf("read-only model resolver repaired metadata: before=%+v after=%+v", before, *meta.Locked)
	}
}

func TestReadOnlyModelReportsMissingCurrentModelAsUnavailable(t *testing.T) {
	_, err := ResolveReadOnlySessionModel(config.App{}, session.Meta{})
	if err == nil {
		t.Fatal("ResolveReadOnlySessionModel accepted missing current model")
	}
	var unavailable *ReadOnlySessionModelUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing current model error = %T, want typed unavailable error", err)
	}
	if unavailable.Reason != serverapi.SessionExecutionModelUnavailableNotConfigured {
		t.Fatalf("unavailable reason = %q", unavailable.Reason)
	}
}

func TestReadOnlyModelReportsProviderInferenceFailureAsInvalid(t *testing.T) {
	_, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{Model: "provider-unknown-model"},
	}, session.Meta{})
	if err == nil {
		t.Fatal("ResolveReadOnlySessionModel accepted a model without a provider contract")
	}
	var invalid *ReadOnlySessionModelInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("provider inference error = %T, want typed invalid error", err)
	}
}

func TestReadOnlyModelPreservesExplicitProviderWithoutInference(t *testing.T) {
	resolved, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{
			Model:            "provider-owned-model",
			ProviderOverride: "custom-provider",
		},
	}, session.Meta{})
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Provider.ID() != "custom-provider" {
		t.Fatalf("explicit provider = %q, want custom-provider", resolved.Provider.ID())
	}
}

func TestReadOnlyModelUsesConfiguredProviderCapabilitiesID(t *testing.T) {
	resolved, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{
			Model: "provider-owned-model",
			ProviderCapabilities: config.ProviderCapabilitiesOverride{
				ProviderID: "openai-compatible",
			},
		},
	}, session.Meta{})
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Provider.ID() != "openai-compatible" {
		t.Fatalf("provider capabilities ID = %q, want openai-compatible", resolved.Provider.ID())
	}
}

func TestReadOnlyModelUsesConfiguredOpenAICompatibleBaseURL(t *testing.T) {
	resolved, err := ResolveReadOnlySessionModel(config.App{
		Settings: config.Settings{
			Model:         "provider-owned-model",
			OpenAIBaseURL: "https://example.test/v1",
		},
	}, session.Meta{})
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionModel: %v", err)
	}
	if resolved.Provider.ID() != "openai-compatible" {
		t.Fatalf("base URL provider = %q, want openai-compatible", resolved.Provider.ID())
	}
}
