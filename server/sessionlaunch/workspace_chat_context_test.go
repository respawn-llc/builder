package sessionlaunch

import (
	"context"
	"errors"
	"testing"

	"core/server/auth"
	"core/server/launch"
	"core/shared/config"
	"core/shared/serverapi"
)

type workspaceChatContextAuthReader struct {
	loaded      auth.State
	stored      auth.State
	loadErr     error
	loadCalls   int
	storedCalls int
}

func (r *workspaceChatContextAuthReader) Load(context.Context) (auth.State, error) {
	r.loadCalls++
	if r.loadErr != nil {
		return auth.State{}, r.loadErr
	}
	return r.loaded, nil
}

func (r *workspaceChatContextAuthReader) CurrentState(context.Context) (auth.State, error) {
	return auth.State{}, errors.New("workspace Chat Context must not refresh auth")
}

func (r *workspaceChatContextAuthReader) StoredState(context.Context) (auth.State, error) {
	r.storedCalls++
	return r.stored, nil
}

func TestReadWorkspaceChatContextProjectsSelectedDraftPolicyWithoutMaterializingSession(t *testing.T) {
	tests := []struct {
		name     string
		settings func() config.Settings
		draft    *WorkspaceChatDraft
		auth     auth.State
		want     serverapi.ChatContext
	}{
		{
			name: "untouched default Agent",
			settings: func() config.Settings {
				settings := draftSettings("gpt-5.6-sol", "medium")
				settings.ModelContextWindow = 120_000
				settings.ContextCompactionThresholdTokens = 90_000
				settings.CompactionMode = config.CompactionModeLocal
				return settings
			},
			want: serverapi.ChatContext{
				ContextWindowTokens:      120_000,
				RemainingTokens:          120_000,
				AutomaticThresholdTokens: 90_000,
				AutoCompactionEnabled:    true,
				CompactionMode:           serverapi.ChatContextCompactionModeLocal,
			},
		},
		{
			name: "configured disabled draft",
			settings: func() config.Settings {
				settings := draftSettings("gpt-5.6-sol", "medium")
				settings.ModelContextWindow = 140_000
				settings.ContextCompactionThresholdTokens = 100_000
				settings.CompactionMode = config.CompactionModeNone
				return settings
			},
			draft: &WorkspaceChatDraft{
				Agent:          config.DefaultSubagentRole,
				Supervisor:     "edits",
				Thinking:       "medium",
				AutoCompaction: false,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens:      140_000,
				RemainingTokens:          140_000,
				AutomaticThresholdTokens: 100_000,
				CompactionMode:           serverapi.ChatContextCompactionModeDisabled,
			},
		},
		{
			name: "native mode with OAuth capability",
			settings: func() config.Settings {
				settings := draftSettings("gpt-5.6-sol", "medium")
				settings.ModelContextWindow = 150_000
				settings.ContextCompactionThresholdTokens = 120_000
				settings.CompactionMode = config.CompactionModeNative
				return settings
			},
			auth: auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
			want: serverapi.ChatContext{
				ContextWindowTokens:      150_000,
				RemainingTokens:          150_000,
				AutomaticThresholdTokens: 120_000,
				AutoCompactionEnabled:    true,
				CompactionMode:           serverapi.ChatContextCompactionModeProviderNative,
			},
		},
		{
			name: "named Agent final settings",
			settings: func() config.Settings {
				settings := draftSettings("gpt-5.6-sol", "medium")
				settings.ModelContextWindow = 160_000
				settings.ContextCompactionThresholdTokens = 130_000
				settings.CompactionMode = config.CompactionModeNative
				roleSettings := settings
				roleSettings.Model = "claude-sonnet-4-5"
				roleSettings.ProviderOverride = "anthropic"
				roleSettings.ModelContextWindow = 80_000
				roleSettings.ContextCompactionThresholdTokens = 60_000
				roleSettings.PreSubmitCompactionLeadTokens = 20_000
				roleSettings.Subagents = nil
				settings.Subagents = map[string]config.SubagentRole{
					"worker": {
						Settings: roleSettings,
						Sources: map[string]string{
							"model":                               "file",
							"provider_override":                   "file",
							"model_context_window":                "file",
							"context_compaction_threshold_tokens": "file",
							"pre_submit_compaction_lead_tokens":   "file",
						},
					},
				}
				return settings
			},
			draft: &WorkspaceChatDraft{
				Agent:          "worker",
				Supervisor:     "edits",
				Thinking:       "medium",
				AutoCompaction: false,
			},
			want: serverapi.ChatContext{
				ContextWindowTokens:      80_000,
				RemainingTokens:          80_000,
				AutomaticThresholdTokens: 60_000,
				CompactionMode:           serverapi.ChatContextCompactionModeLocal,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persistence := &draftPersistence{draft: test.draft}
			authReader := &workspaceChatContextAuthReader{loaded: test.auth}
			service := NewService(launchPlannerForWorkspaceChatContext(test.settings())).
				WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(persistence), "workspace-1").
				WithAuthStateReader(authReader)

			got, err := service.ReadWorkspaceChatContext(t.Context())
			if err != nil {
				t.Fatalf("ReadWorkspaceChatContext: %v", err)
			}
			if got != test.want {
				t.Fatalf("ReadWorkspaceChatContext() = %+v, want %+v", got, test.want)
			}
			if authReader.loadCalls == 0 {
				t.Fatal("workspace Chat Context did not read non-refreshing effective auth")
			}
			if test.draft != nil && (persistence.draft == nil || *persistence.draft != *test.draft) {
				t.Fatalf("workspace Chat Context changed persisted draft: got %+v, want %+v", persistence.draft, test.draft)
			}
			if service.runtime != nil {
				t.Fatal("lazy workspace Context created or required runtime authority")
			}
		})
	}
}

func TestWorkspaceChatContextKeepsDraftAndEffectiveAuthAuthoritiesSeparate(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "medium")
	settings.ModelContextWindow = 100_000
	settings.ContextCompactionThresholdTokens = 75_000
	settings.CompactionMode = config.CompactionModeNative
	settings.OpenAIBaseURL = "https://compatible.example/v1"
	authReader := &workspaceChatContextAuthReader{
		stored: auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
		loaded: auth.State{Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "environment-key"},
		}},
	}
	service := NewService(launchPlannerForWorkspaceChatContext(settings)).
		WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(&draftPersistence{}), "workspace-1").
		WithAuthStateReader(authReader)

	got, err := service.ReadWorkspaceChatContext(t.Context())
	if err != nil {
		t.Fatalf("ReadWorkspaceChatContext: %v", err)
	}
	if got.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("CompactionMode = %q, want local fallback from effective environment API-key authority", got.CompactionMode)
	}
	if authReader.storedCalls == 0 || authReader.loadCalls == 0 {
		t.Fatalf("draft/effective auth reads = stored:%d load:%d, want both separate authorities", authReader.storedCalls, authReader.loadCalls)
	}
}

func TestOrdinaryWorkspaceChatDraftOperationsDoNotResolveContextPolicy(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "medium")
	persistence := &draftPersistence{}
	authReader := &workspaceChatContextAuthReader{}
	service := NewService(launchPlannerForWorkspaceChatContext(settings)).
		WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(persistence), "workspace-1").
		WithAuthStateReader(authReader)

	if _, err := service.ResolveWorkspaceChatDraftAggregate(t.Context()); err != nil {
		t.Fatalf("ResolveWorkspaceChatDraftAggregate: %v", err)
	}
	if _, err := service.TransformWorkspaceChatDraftAggregate(t.Context(), func(current WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
		next := current.Draft
		next.Message = "updated"
		return next, nil
	}); err != nil {
		t.Fatalf("TransformWorkspaceChatDraftAggregate: %v", err)
	}
	if _, err := service.WorkspaceChatDraft(t.Context(), serverapi.WorkspaceChatDraftRequest{
		Operation: serverapi.WorkspaceChatDraftOperation{Kind: serverapi.WorkspaceChatDraftClear},
	}); err != nil {
		t.Fatalf("clear WorkspaceChatDraft: %v", err)
	}
	if authReader.loadCalls != 0 {
		t.Fatalf("ordinary draft operations made %d Context-policy auth loads, want 0", authReader.loadCalls)
	}
}

func TestWorkspaceChatMaterializationDoesNotResolveContextPolicy(t *testing.T) {
	service, _, _, _ := newWorkspaceChatMaterializationService(t)
	authReader := &workspaceChatContextAuthReader{}
	service.WithAuthStateReader(authReader)

	if _, err := service.materializeWorkspaceChatSession(t.Context()); err != nil {
		t.Fatalf("materializeWorkspaceChatSession: %v", err)
	}
	if authReader.loadCalls != 0 {
		t.Fatalf("workspace Chat materialization made %d Context-policy auth loads, want 0", authReader.loadCalls)
	}
}

func TestReadWorkspaceChatContextPropagatesEffectiveAuthFailure(t *testing.T) {
	wantErr := errors.New("effective auth unavailable")
	service := NewService(launchPlannerForWorkspaceChatContext(draftSettings("gpt-5.6-sol", "medium"))).
		WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(&draftPersistence{}), "workspace-1").
		WithAuthStateReader(&workspaceChatContextAuthReader{loadErr: wantErr})

	if _, err := service.ReadWorkspaceChatContext(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("ReadWorkspaceChatContext error = %v, want %v", err, wantErr)
	}
}

func TestReadWorkspaceChatContextSkipsEffectiveAuthForExplicitProviderCapabilities(t *testing.T) {
	settings := draftSettings("custom-model", "medium")
	settings.CompactionMode = config.CompactionModeNative
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:               "custom",
		SupportsResponsesCompact: true,
	}
	authReader := &workspaceChatContextAuthReader{loadErr: errors.New("auth must not be loaded")}
	service := NewService(launchPlannerForWorkspaceChatContext(settings)).
		WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(&draftPersistence{}), "workspace-1").
		WithAuthStateReader(authReader)

	got, err := service.ReadWorkspaceChatContext(t.Context())
	if err != nil {
		t.Fatalf("ReadWorkspaceChatContext: %v", err)
	}
	if got.CompactionMode != serverapi.ChatContextCompactionModeProviderNative {
		t.Fatalf("CompactionMode = %q, want explicit provider-native mode", got.CompactionMode)
	}
	if authReader.loadCalls != 0 {
		t.Fatalf("effective auth Load calls = %d, want 0", authReader.loadCalls)
	}
}

func launchPlannerForWorkspaceChatContext(settings config.Settings) launch.Planner {
	return launch.Planner{Config: config.App{Settings: settings}}
}
