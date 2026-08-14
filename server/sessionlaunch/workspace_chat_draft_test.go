package sessionlaunch

import (
	"context"
	"errors"
	"testing"

	"core/server/auth"
	"core/server/launch"
	"core/server/session"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func draftSettings(model, thinking string) config.Settings {
	s := config.DefaultOnboardingSettings()
	s.Model, s.ThinkingLevel = model, thinking
	s.Reviewer.Frequency, s.Reviewer.Model, s.Reviewer.ThinkingLevel, s.Reviewer.ModelContextWindow = "edits", model, thinking, s.ModelContextWindow
	return s
}
func draftInput(s config.Settings) WorkspaceChatDraftResolverInput {
	return WorkspaceChatDraftResolverInput{Settings: s, AuthState: auth.EmptyState()}
}
func addWorker(s *config.Settings, thinking string) {
	s.Subagents = map[string]config.SubagentRole{"worker": {Settings: config.Settings{Model: "worker-model", ProviderOverride: "anthropic", ThinkingLevel: thinking, Reviewer: config.ReviewerSettings{Frequency: "all"}}, Sources: map[string]string{"model": "file", "provider_override": "file", "thinking_level": "file", "reviewer.frequency": "file"}}}
}

func TestWorkspaceChatDraftResolution(t *testing.T) {
	base := draftSettings("gpt-5.6-sol", "medium")
	base.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	got, err := ResolveWorkspaceChatDraft(draftInput(base), nil)
	if err != nil || got.Draft.Agent != "default" || got.Draft.Supervisor != "edits" || got.Draft.Thinking != "medium" || !got.Draft.Questions || !got.Draft.AutoCompaction {
		t.Fatalf("defaults=%+v err=%v", got.Draft, err)
	}
	addWorker(&base, "high")
	got, err = ResolveWorkspaceChatDraft(draftInput(base), nil)
	if err != nil || len(got.Baselines) < 3 || got.Baselines["worker"].Supervisor != "all" || got.Baselines["worker"].Thinking != "high" || got.Baselines["fast"].Thinking != "low" {
		t.Fatalf("baselines=%+v err=%v", got.Baselines, err)
	}
	stored := &WorkspaceChatDraft{Message: "keep\nverbatim", Agent: "removed", Supervisor: "all", Thinking: "high", Fast: true}
	got, err = ResolveWorkspaceChatDraft(draftInput(draftSettings("gpt-5.6-sol", "medium")), stored)
	def := got.Baselines["default"]
	if err != nil || got.Draft.Message != stored.Message || got.Draft.Agent != "default" || got.Draft.Supervisor != def.Supervisor || got.Draft.Thinking != def.Thinking || got.Draft.Fast != def.Fast || got.Draft.Questions != def.Questions || got.Draft.AutoCompaction != def.AutoCompaction {
		t.Fatalf("unavailable=%+v default=%+v err=%v", got.Draft, def, err)
	}
	stored = &WorkspaceChatDraft{Message: "preserve", Agent: "worker", Supervisor: "off", Thinking: "bad", Fast: true, Questions: true}
	got, err = ResolveWorkspaceChatDraft(draftInput(base), stored)
	if err != nil || got.Draft.Message != "preserve" || got.Draft.Thinking != "bad" || got.Draft.Fast {
		t.Fatalf("custom Thinking=%+v err=%v", got.Draft, err)
	}
}

func TestWorkspaceChatDraftDefaultFastUsesLoadedConfiguration(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "medium")
	settings.PriorityRequestMode = true
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}
	resolved, err := ResolveWorkspaceChatDraft(draftInput(settings), nil)
	if err != nil {
		t.Fatalf("ResolveWorkspaceChatDraft: %v", err)
	}
	if !resolved.Draft.Fast {
		t.Fatal("workspace Chat Fast default = false, want loaded configuration value true")
	}
}

func TestWorkspaceChatDraftResolutionRetainsQuestionsPolicyForSettingsRead(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "medium")
	settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	stored := &WorkspaceChatDraft{
		Agent:          "default",
		Supervisor:     "edits",
		Thinking:       "medium",
		Questions:      true,
		AutoCompaction: true,
	}
	resolved, err := ResolveWorkspaceChatDraft(draftInput(settings), stored)
	if err != nil {
		t.Fatalf("ResolveWorkspaceChatDraft: %v", err)
	}
	if resolved.Draft.Questions {
		t.Fatal("existing effective draft capability repair changed")
	}
	if !resolved.PersistedQuestionsPolicy {
		t.Fatal("persisted Questions policy was not retained")
	}
	projected, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog: resolved.Catalog,
		Agent:   resolved.Draft.Agent,
		Settings: session.ChatSettings{
			Supervisor:     resolved.Draft.Supervisor,
			Thinking:       resolved.Draft.Thinking,
			Fast:           resolved.Draft.Fast,
			Questions:      resolved.Draft.Questions,
			AutoCompaction: resolved.Draft.AutoCompaction,
		},
		PersistedQuestionsPolicy: resolved.PersistedQuestionsPolicy,
		CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
	})
	if err != nil {
		t.Fatalf("ProjectChatSettings: %v", err)
	}
	if projected.Questions.Capable || !projected.Questions.Enabled ||
		projected.Questions.Editability != serverapi.ChatSettingsEditable {
		t.Fatalf("Questions projection = %+v", projected.Questions)
	}
}

func TestWorkspaceChatDraftMessageUpdatePreservesCustomThinking(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "medium")
	stored := &WorkspaceChatDraft{
		Agent:          "default",
		Supervisor:     "edits",
		Thinking:       "provider-specific-depth",
		AutoCompaction: true,
	}
	persistence := &draftPersistence{draft: stored}
	service := NewService(launch.Planner{Config: config.App{Settings: settings}}).
		WithWorkspaceChatDraft(NewWorkspaceChatDraftOwner(persistence), "workspace-1")
	message := "updated without touching settings"

	if _, err := service.WorkspaceChatDraft(t.Context(), serverapi.WorkspaceChatDraftRequest{
		Operation: serverapi.WorkspaceChatDraftOperation{
			Kind:    serverapi.WorkspaceChatDraftUpdateMessage,
			Message: &message,
		},
	}); err != nil {
		t.Fatalf("WorkspaceChatDraft update message: %v", err)
	}
	if persistence.draft == nil ||
		persistence.draft.Message != message ||
		persistence.draft.Thinking != stored.Thinking {
		t.Fatalf("persisted draft = %+v", persistence.draft)
	}
}

type draftPersistence struct {
	draft  *WorkspaceChatDraft
	reads  int
	writes int
}

func (f *draftPersistence) ReadWorkspaceChatDraft(context.Context, string) (*WorkspaceChatDraft, error) {
	f.reads++
	if f.draft == nil {
		return nil, nil
	}
	d := *f.draft
	return &d, nil
}

func TestWorkspaceChatDraftOwnerMaterializesOneCanonicalResolutionUnderItsLane(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "  custom-depth  ")
	stored := WorkspaceChatDraft{
		Message:        "unsent",
		Agent:          "default",
		Supervisor:     "all",
		Thinking:       "custom-depth",
		Fast:           false,
		Questions:      false,
		AutoCompaction: false,
	}
	persistence := &draftPersistence{draft: &stored}
	owner := NewWorkspaceChatDraftOwner(persistence)
	resolverCalls := 0
	materializerCalls := 0
	wantID := runtimeids.NewSessionID()

	got, err := owner.MaterializeWorkspaceChat(
		context.Background(),
		"workspace-1",
		func(context.Context) (WorkspaceChatDraftResolverInput, error) {
			resolverCalls++
			return draftInput(settings), nil
		},
		func(_ context.Context, resolution WorkspaceChatDraftResolution) (runtimeids.SessionID, error) {
			materializerCalls++
			if resolution.Draft != stored {
				t.Fatalf("materializer draft = %+v, want %+v", resolution.Draft, stored)
			}
			return wantID, nil
		},
	)
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	if got != wantID || resolverCalls != 1 || persistence.reads != 1 || materializerCalls != 1 {
		t.Fatalf("result=%q resolver=%d reads=%d materializer=%d", got.String(), resolverCalls, persistence.reads, materializerCalls)
	}
}

func TestWorkspaceChatDraftOwnerPreservesDraftAcrossMaterializerFailures(t *testing.T) {
	settings := draftSettings("gpt-5.6-sol", "medium")
	for _, failure := range []string{"initialization", "filesystem", "metadata commit"} {
		t.Run(failure, func(t *testing.T) {
			stored := WorkspaceChatDraft{
				Message:        "preserve",
				Agent:          "default",
				Supervisor:     "edits",
				Thinking:       "medium",
				AutoCompaction: true,
			}
			persistence := &draftPersistence{draft: &stored}
			owner := NewWorkspaceChatDraftOwner(persistence)
			wantErr := errors.New(failure)
			_, err := owner.MaterializeWorkspaceChat(
				context.Background(),
				"workspace-1",
				func(context.Context) (WorkspaceChatDraftResolverInput, error) {
					return draftInput(settings), nil
				},
				func(context.Context, WorkspaceChatDraftResolution) (runtimeids.SessionID, error) {
					return runtimeids.SessionID{}, wantErr
				},
			)
			if !errors.Is(err, wantErr) {
				t.Fatalf("MaterializeWorkspaceChat error = %v, want %v", err, wantErr)
			}
			if persistence.draft == nil || *persistence.draft != stored {
				t.Fatalf("workspace draft after %s failure = %+v, want %+v", failure, persistence.draft, stored)
			}
		})
	}
}
func (f *draftPersistence) ReplaceWorkspaceChatDraft(_ context.Context, _ string, d *WorkspaceChatDraft) error {
	f.writes++
	f.draft = d
	return nil
}

func TestWorkspaceChatDraftOwnerTransformsClearAndSerializes(t *testing.T) {
	s := draftSettings("gpt-5.6-sol", "medium")
	s.ProviderOverride = "openai"
	addWorker(&s, "low")
	p := &draftPersistence{draft: &WorkspaceChatDraft{Message: "latest", Agent: "default", Supervisor: "off", Thinking: "high", Fast: true}}
	resolve := func(context.Context) (WorkspaceChatDraftResolverInput, error) { return draftInput(s), nil }
	o := NewWorkspaceChatDraftOwner(p)
	o2 := o
	got, err := o.TransformWorkspaceChatDraft(context.Background(), "w", resolve, func(r WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
		d := r.Baselines["worker"]
		d.Message = r.Draft.Message
		return d, nil
	})
	if err != nil || got.Agent != "worker" || got.Message != "latest" || got.Supervisor != "all" || got.Thinking != "low" || got.Fast || !got.Questions || !got.AutoCompaction {
		t.Fatalf("transform=%+v err=%v", got, err)
	}
	if err := o.ClearWorkspaceChatDraft(context.Background(), "w"); err != nil || p.draft != nil {
		t.Fatalf("clear=%+v err=%v", p.draft, err)
	}
	p.draft = &WorkspaceChatDraft{Message: "old", Agent: "default", Supervisor: "edits", Thinking: "medium"}
	entered, release := make(chan struct{}), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		_, e := o.TransformWorkspaceChatDraft(context.Background(), "w", resolve, func(r WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
			close(entered)
			<-release
			r.Draft.Message = "new"
			return r.Draft, nil
		})
		first <- e
	}()
	<-entered
	go func() {
		_, e := o2.TransformWorkspaceChatDraft(context.Background(), "w", resolve, func(r WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
			r.Draft.Fast = true
			return r.Draft, nil
		})
		second <- e
	}()
	select {
	case e := <-second:
		t.Fatalf("ran early: %v", e)
	default:
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil || p.draft.Message != "new" || !p.draft.Fast {
		t.Fatalf("serialized=%+v err=%v", p.draft, err)
	}
}
