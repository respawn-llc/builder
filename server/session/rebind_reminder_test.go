package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestSessionRebindReminderNormalizesAndClonesOnlyReminderFacts(t *testing.T) {
	workingDirectory := " /target/pkg "
	normalized, err := NormalizeSessionRebindReminder(SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: " source-id ", Name: " Source "},
		TargetProject:    serverapi.ProjectReference{ID: " target-id ", Name: " Target "},
		WorkingDirectory: &workingDirectory,
	})
	if err != nil {
		t.Fatalf("NormalizeSessionRebindReminder: %v", err)
	}
	want := SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: "source-id", Name: "Source"},
		TargetProject:    serverapi.ProjectReference{ID: "target-id", Name: "Target"},
		WorkingDirectory: stringPointer("/target/pkg"),
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized reminder = %#v, want %#v", normalized, want)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("reminder fields = %v, want source_project, target_project, working_directory only", fields)
	}
	for _, field := range []string{"source_project", "target_project", "working_directory"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("reminder fields = %v, missing %q", fields, field)
		}
	}
	normalized.WorkingDirectory = nil
	encoded, err = json.Marshal(normalized)
	if err != nil {
		t.Fatalf("json.Marshal without working directory: %v", err)
	}
	fields = nil
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal without working directory: %v", err)
	}
	if len(fields) != 2 || fields["working_directory"] != nil {
		t.Fatalf("reminder without changed working directory fields = %v", fields)
	}
	normalized.WorkingDirectory = stringPointer("/target/pkg")
	clone := CloneSessionRebindReminder(&normalized)
	*clone.WorkingDirectory = "/mutated"
	if *normalized.WorkingDirectory != "/target/pkg" {
		t.Fatal("cloned reminder aliases source working directory")
	}
}

func TestSessionRebindReminderPersistsAcrossReopenButNotIntoForksOrClones(t *testing.T) {
	store, err := Create(
		t.TempDir(),
		"source",
		"/source",
		sessioncontract.SessionCategoryMain,
		sessionTestPersistence.options()...,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	reminder := SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: "source-id", Name: "Source"},
		TargetProject:    serverapi.ProjectReference{ID: "target-id", Name: "Target"},
		WorkingDirectory: stringPointer("/target"),
	}
	if err := store.SetSessionRebindReminder(&reminder); err != nil {
		t.Fatalf("SetSessionRebindReminder: %v", err)
	}

	reopened := mustOpenSessionTestStore(t, store)
	if got := reopened.Meta().RebindReminder; got == nil || !SessionRebindReminderEqual(*got, reminder) {
		t.Fatalf("reopened reminder = %#v, want %#v", got, reminder)
	}

	appendSessionTestRecord(t, store, "step-1", sessionTestMessage(MessageRoleUser, "source message"))
	parentLog := mustMaterializeSessionTestEventLog(t, store)
	forked, _, err := ForkAtUserMessage(
		parentLog,
		userMessageSeqAt(t, store, 1),
		"fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("ForkAtUserMessage: %v", err)
	}
	if forked.Meta().RebindReminder != nil {
		t.Fatalf("fork inherited rebind reminder: %#v", forked.Meta().RebindReminder)
	}
	cloned, err := CloneSession(parentLog, "clone", sessioncontract.SessionCategorySubagent)
	if err != nil {
		t.Fatalf("CloneSession: %v", err)
	}
	if cloned.Meta().RebindReminder != nil {
		t.Fatalf("clone inherited rebind reminder: %#v", cloned.Meta().RebindReminder)
	}
}

func TestSessionReminderStatesMutateIndependently(t *testing.T) {
	store, err := Create(
		t.TempDir(),
		"workspace",
		"/workspace",
		sessioncontract.SessionCategoryMain,
		sessionTestPersistence.options()...,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	worktree := WorktreeReminderState{
		Mode: WorktreeReminderModeEnter,
		WorktreeContext: WorktreeContext{
			WorktreePath:  "/workspace/worktree",
			WorkspaceRoot: "/workspace",
			EffectiveCwd:  "/workspace/worktree",
		},
	}
	if err := store.SetWorktreeReminderState(&worktree); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	rebind := SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: "source", Name: "Source"},
		TargetProject:    serverapi.ProjectReference{ID: "target", Name: "Target"},
		WorkingDirectory: stringPointer("/target"),
	}
	if err := store.SetSessionRebindReminder(&rebind); err != nil {
		t.Fatalf("SetSessionRebindReminder: %v", err)
	}
	if store.Meta().WorktreeReminder == nil {
		t.Fatal("setting rebind reminder cleared worktree reminder")
	}
	if err := store.SetWorktreeReminderState(nil); err != nil {
		t.Fatalf("clear worktree reminder: %v", err)
	}
	if got := store.Meta().RebindReminder; got == nil || !SessionRebindReminderEqual(*got, rebind) {
		t.Fatalf("clearing worktree reminder changed rebind reminder: %+v", got)
	}
	if err := store.SetSessionRebindReminder(nil); err != nil {
		t.Fatalf("clear rebind reminder: %v", err)
	}
	if store.Meta().WorktreeReminder != nil || store.Meta().RebindReminder != nil {
		t.Fatalf("independent clears left reminder state: %+v", store.Meta())
	}
}
