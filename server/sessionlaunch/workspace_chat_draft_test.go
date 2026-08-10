package sessionlaunch

import (
	"context"
	"testing"

	"core/server/auth"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/toolspec"
)

func draftSettings(model, thinking string) config.Settings {
	s := config.DefaultOnboardingSettings()
	s.Model, s.ThinkingLevel = model, thinking
	s.Reviewer.Frequency, s.Reviewer.Model, s.Reviewer.ThinkingLevel, s.Reviewer.ModelContextWindow = "edits", model, thinking, s.ModelContextWindow
	return s
}
func draftInput(s config.Settings) WorkspaceChatDraftResolverInput {
	return WorkspaceChatDraftResolverInput{Settings: s, AuthState: auth.EmptyState(), FastModeState: runtime.NewFastModeState(false)}
}
func addWorker(s *config.Settings, thinking string) {
	s.Subagents = map[string]config.SubagentRole{"worker": {Settings: config.Settings{Model: "worker-model", ProviderOverride: "anthropic", ThinkingLevel: thinking, Reviewer: config.ReviewerSettings{Frequency: "all"}}, Sources: map[string]string{"model": "file", "provider_override": "file", "thinking_level": "file", "reviewer.frequency": "file"}}}
}

func TestWorkspaceChatDraftResolution(t *testing.T) {
	base := draftSettings("gpt-5.6-sol", "medium")
	base.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	got, err := ResolveWorkspaceChatDraft(draftInput(base), nil)
	if err != nil || got.Draft.Agent != "default" || got.Draft.Supervisor != "edits" || got.Draft.Thinking != "medium" || !got.Draft.Questions || !got.Draft.AutoCompaction || got.GoalAvailability != clientui.GoalAvailabilityAvailable {
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
	if err != nil || got.Draft.Message != "preserve" || got.Draft.Thinking != "high" || got.Draft.Fast {
		t.Fatalf("repair=%+v err=%v", got.Draft, err)
	}
	worker := base.Subagents["worker"]
	worker.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	worker.Sources["tools.ask_question"] = "file"
	worker.Sources["tools.exec_command"] = "file"
	base.Subagents["worker"] = worker
	workerDraft, err := ResolveWorkspaceChatDraft(draftInput(base), &WorkspaceChatDraft{
		Message: "keep", Agent: "worker", Supervisor: "all", Thinking: "high", Questions: false,
	})
	if err != nil || workerDraft.GoalAvailability != clientui.GoalAvailabilityAgentCapabilityMissing {
		t.Fatalf("worker availability=%q err=%v", workerDraft.GoalAvailability, err)
	}
}

type draftPersistence struct {
	draft *WorkspaceChatDraft
}

func (f *draftPersistence) ReadWorkspaceChatDraft(context.Context, string) (*WorkspaceChatDraft, error) {
	if f.draft == nil {
		return nil, nil
	}
	d := *f.draft
	return &d, nil
}
func (f *draftPersistence) ReplaceWorkspaceChatDraft(_ context.Context, _ string, d *WorkspaceChatDraft) error {
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
	if err != nil || got.Draft.Agent != "worker" || got.Draft.Message != "latest" || got.Draft.Supervisor != "all" || got.Draft.Thinking != "low" || got.Draft.Fast || !got.Draft.Questions || !got.Draft.AutoCompaction || got.GoalAvailability != clientui.GoalAvailabilityAvailable {
		t.Fatalf("transform=%+v err=%v", got.Draft, err)
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
