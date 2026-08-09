package status

import (
	"context"
	"core/shared/apicontract"
	"core/shared/authstatus"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type statusSessionViewStub struct {
	apicontract.SessionViewService
	mainViewCalls int
	view          clientui.RuntimeMainView
}

type statusAuthStatusStub struct {
	response serverapi.AuthStatusResponse
	err      error
}

func (s statusAuthStatusStub) GetAuthStatus(
	context.Context,
	serverapi.AuthStatusRequest,
) (serverapi.AuthStatusResponse, error) {
	return s.response, s.err
}

type recordingStatusAuthStatusStub struct {
	request  serverapi.AuthStatusRequest
	response serverapi.AuthStatusResponse
}

func (s *recordingStatusAuthStatusStub) GetAuthStatus(
	_ context.Context,
	request serverapi.AuthStatusRequest,
) (serverapi.AuthStatusResponse, error) {
	s.request = request
	return s.response, nil
}

func (s *statusSessionViewStub) GetSessionMainView(_ context.Context, _ serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	s.mainViewCalls++
	return serverapi.SessionMainViewResponse{MainView: s.view}, nil
}

func TestCollectEnvironmentDisabledSkillRemainsVisibleAndStillCollectsAgents(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace guidance"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	skillDir := filepath.Join(workspace, config.ConfigDirName, "skills", "hidden-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: hidden-skill\ndescription: hidden\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	result := (Collector{}).CollectEnvironment(context.Background(), Request{
		WorkspaceRoot:   workspace,
		PersistenceRoot: persistenceRoot,
		Settings: config.Settings{
			SkillToggles: map[string]bool{"hidden-skill": false},
		},
	}, Snapshot{})

	if len(result.Skills) != 1 || result.Skills[0].Name != "hidden-skill" || !result.Skills[0].Loaded || !result.Skills[0].Disabled {
		t.Fatalf("disabled skill inspection = %+v, want visible loaded disabled skill", result.Skills)
	}
	if len(result.SkillTokenCounts) != 0 {
		t.Fatalf("disabled skill must not be tokenized for model visibility: %+v", result.SkillTokenCounts)
	}
	if result.CollectorWarning != "" {
		t.Fatalf("disabled skills emitted a collector warning: %q", result.CollectorWarning)
	}
	if len(result.AgentsPaths) == 0 || len(result.AgentTokenCounts) == 0 {
		t.Fatalf("AGENTS.md collection did not run: %+v", result)
	}
}

func TestCollectorUsesTypedAuthStatusService(t *testing.T) {
	email := "user@example.com"
	plan := "pro"
	collector := Collector{}
	snapshot, err := collector.Collect(context.Background(), Request{
		WorkspaceRoot: t.TempDir(),
		AuthStatus: statusAuthStatusStub{response: serverapi.AuthStatusResponse{
			Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
				Method:        serverapi.AuthStatusMethodOAuth,
				Provider:      serverapi.OpenAIAuthProviderFacts(),
				EnvPreference: serverapi.AuthStatusEnvPreferencePreferSaved,
				OAuth:         &serverapi.AuthOAuthFacts{Email: &email},
			}, nil),
			Subscription: serverapi.AuthSubscriptionFacts{Applicable: true, Plan: &plan},
		}},
	})
	if err != nil {
		t.Fatalf("collect status: %v", err)
	}
	if snapshot.Auth.Summary != email {
		t.Fatalf("auth summary = %q", snapshot.Auth.Summary)
	}
	if snapshot.Subscription.Summary != "Pro subscription" {
		t.Fatalf("subscription summary = %q", snapshot.Subscription.Summary)
	}
}

func TestCollectorRequestsEffectiveSessionAuthProvider(t *testing.T) {
	provider := serverapi.AuthProviderFacts{
		Kind:       serverapi.AuthProviderKindOpenAICompatible,
		Identifier: "openai-compatible",
		DisplayOrigin: &serverapi.AuthProviderDisplayOrigin{
			Scheme:   "https",
			Hostname: "session.example",
		},
	}
	authStatus := &recordingStatusAuthStatusStub{
		response: serverapi.AuthStatusResponse{
			Resolution: serverapi.KnownAuthStatusResolution(serverapi.AuthStatusFacts{
				Method:        serverapi.AuthStatusMethodNone,
				Provider:      provider,
				EnvPreference: serverapi.AuthStatusEnvPreferenceUnspecified,
			}, nil),
		},
	}
	selection := authstatus.ProviderSelection(config.Settings{OpenAIBaseURL: "https://session.example/v1"})

	result := (Collector{}).CollectAuth(context.Background(), Request{
		AuthStatus:    authStatus,
		AuthSelection: &selection,
	}, Snapshot{})

	if authStatus.request.Provider == nil ||
		!reflect.DeepEqual(*authStatus.request.Provider, selection) ||
		result.Auth.Provider != "https://session.example" {
		t.Fatalf("effective provider request/result = %+v / %+v", authStatus.request, result)
	}
}

func TestCollectBasePreservesOptionalAgentRole(t *testing.T) {
	role := "worker"
	for name, agentRole := range map[string]*string{
		"named agent":   &role,
		"default agent": nil,
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := (Collector{}).CollectBase(Request{AgentRole: agentRole})
			if agentRole == nil {
				if snapshot.AgentRole != nil {
					t.Fatalf("snapshot agent role = %v, want nil", snapshot.AgentRole)
				}
				return
			}
			if snapshot.AgentRole == nil || *snapshot.AgentRole != *agentRole {
				t.Fatalf("snapshot agent role = %v, want %q", snapshot.AgentRole, *agentRole)
			}
			if snapshot.AgentRole == agentRole {
				t.Fatal("snapshot agent role aliases request")
			}
		})
	}
}

func TestEnrichBaseUsesCurrentSessionRoleAsAuthoritative(t *testing.T) {
	cachedRole := "stale"
	storedRole := "qa_tester"
	for name, role := range map[string]*string{
		"named agent":   &storedRole,
		"default agent": nil,
	} {
		t.Run(name, func(t *testing.T) {
			sessionViews := &statusSessionViewStub{
				view: clientui.RuntimeMainView{
					Session: clientui.RuntimeSessionView{SessionID: "session-1", AgentRole: role},
				},
			}
			snapshot := (Collector{}).EnrichBase(context.Background(), Request{
				SessionID:    "session-1",
				SessionViews: sessionViews,
			}, Snapshot{
				SessionID: "session-1",
				AgentRole: &cachedRole,
			})

			if sessionViews.mainViewCalls != 1 {
				t.Fatalf("current session view calls = %d, want 1", sessionViews.mainViewCalls)
			}
			if role == nil {
				if snapshot.AgentRole != nil {
					t.Fatalf("snapshot agent role = %v, want nil", snapshot.AgentRole)
				}
				return
			}
			if snapshot.AgentRole == nil || *snapshot.AgentRole != *role {
				t.Fatalf("snapshot agent role = %v, want %q", snapshot.AgentRole, *role)
			}
			if snapshot.AgentRole == role {
				t.Fatal("snapshot agent role aliases current session view")
			}
		})
	}
}
