package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/app/commands"
	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type promptCatalogTestService struct {
	response serverapi.PromptCommandCatalogResponse
	err      error
	calls    int
}

func (s *promptCatalogTestService) GetPromptCommandCatalog(context.Context, serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error) {
	s.calls++
	return s.response, s.err
}

func TestPromptCatalogRefreshRemovesStaleEntryAndAtomicallyReplacesSnapshot(t *testing.T) {
	service := &promptCatalogTestService{response: serverapi.PromptCommandCatalogResponse{
		Commands: []serverapi.PromptCommandCatalogEntry{{Name: "prompt:new", Preview: "new"}},
	}}
	model := newProjectedStaticUIModel(
		WithUIPromptCommandCatalog(service),
		WithUIPromptCommandCatalogEntries([]commands.PromptCommandCatalogEntry{{Name: "prompt:old", Preview: "old"}}),
	)
	model.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(model.promptCatalogEntries)

	refresh := model.startPromptCatalogRefresh("prompt:old")
	if _, ok := model.commandRegistry.Command("/prompt:old"); ok {
		t.Fatal("stale command remained registered while refresh was pending")
	}
	msg := refresh()
	model.handlePromptCatalogRefreshDone(msg.(promptCatalogRefreshDoneMsg))
	if _, ok := model.commandRegistry.Command("/prompt:new"); !ok {
		t.Fatal("refreshed command was not registered")
	}
	if _, ok := model.commandRegistry.Command("/prompt:old"); ok {
		t.Fatal("stale command was restored after refresh")
	}
	if service.calls != 1 {
		t.Fatalf("catalog calls = %d, want one", service.calls)
	}
}

func TestPromptCatalogRefreshIgnoresStaleCompletion(t *testing.T) {
	service := &promptCatalogTestService{response: serverapi.PromptCommandCatalogResponse{
		Commands: []serverapi.PromptCommandCatalogEntry{{Name: "prompt:new", Preview: "new"}},
	}}
	model := newProjectedStaticUIModel(
		WithUIPromptCommandCatalog(service),
		WithUIPromptCommandCatalogEntries([]commands.PromptCommandCatalogEntry{{Name: "prompt:old", Preview: "old"}}),
	)
	first := model.startPromptCatalogRefresh("prompt:old")
	second := model.startPromptCatalogRefresh("prompt:old")
	_ = second
	model.handlePromptCatalogRefreshDone(first().(promptCatalogRefreshDoneMsg))
	if _, ok := model.commandRegistry.Command("/prompt:new"); ok {
		t.Fatal("stale refresh completion replaced the current snapshot")
	}
}

func TestPromptCatalogRefreshFailureKeepsFilteredSnapshot(t *testing.T) {
	service := &promptCatalogTestService{err: errors.New("offline")}
	model := newProjectedStaticUIModel(
		WithUIPromptCommandCatalog(service),
		WithUIPromptCommandCatalogEntries([]commands.PromptCommandCatalogEntry{{Name: "prompt:old", Preview: "old"}}),
	)
	model.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(model.promptCatalogEntries)
	msg := model.startPromptCatalogRefresh("prompt:old")()
	model.handlePromptCatalogRefreshDone(msg.(promptCatalogRefreshDoneMsg))
	if _, ok := model.commandRegistry.Command("/prompt:old"); ok {
		t.Fatal("failed refresh restored removed command")
	}
}

func TestMissingPromptCommandSubmissionRefreshesCatalog(t *testing.T) {
	disableTransientStatusClearForTest(t)
	command := "prompt:old"
	service := &promptCatalogTestService{response: serverapi.PromptCommandCatalogResponse{
		Commands: []serverapi.PromptCommandCatalogEntry{{Name: "prompt:new", Preview: "new"}},
	}}
	model := newProjectedStaticUIModel(
		WithUIPromptCommandCatalog(service),
		WithUIPromptCommandCatalogEntries([]commands.PromptCommandCatalogEntry{{Name: command, Preview: "old"}}),
	)
	model.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(model.promptCatalogEntries)
	model.activeSubmit = activeSubmitState{token: 1}

	commandError := &serverapi.PromptCommandError{
		Kind:    serverapi.PromptCommandErrorKindCommandNotFound,
		Command: &command,
	}
	next, cmd := model.inputController().handleSubmitDone(submitDoneMsg{
		token:         1,
		submittedText: "/prompt:old",
		err:           commandError,
	})
	updated := next.(*uiModel)
	for _, msg := range collectCmdMessages(t, cmd) {
		updated = updateUIModel(t, updated, msg)
	}

	if service.calls != 1 {
		t.Fatalf("catalog calls = %d, want one", service.calls)
	}
	if updated.transientStatus != runtimeattach.FormatSubmissionError(commandError) {
		t.Fatalf("missing-command status = %q, want %q", updated.transientStatus, runtimeattach.FormatSubmissionError(commandError))
	}
	if _, ok := updated.commandRegistry.Command("/prompt:new"); !ok {
		t.Fatal("missing-command submission did not install refreshed command")
	}
	if _, ok := updated.commandRegistry.Command("/prompt:old"); ok {
		t.Fatal("missing-command submission restored stale command")
	}
}

func TestDeferredMissingPromptCommandRefreshesCatalogWithoutRestoringQueue(t *testing.T) {
	service := &promptCatalogTestService{response: serverapi.PromptCommandCatalogResponse{
		Commands: []serverapi.PromptCommandCatalogEntry{{Name: "prompt:new", Preview: "new"}},
	}}
	model := newProjectedStaticUIModel(
		WithUIPromptCommandCatalog(service),
		WithUIPromptCommandCatalogEntries([]commands.PromptCommandCatalogEntry{{Name: "prompt:old", Preview: "old"}}),
	)
	model.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(model.promptCatalogEntries)
	command := "prompt:old"
	reason := clientui.QueuedUserMessageFailurePromptCommandNotFound
	text := "/prompt:old src"
	cmd := model.applyTranscriptQueuedMessageState(clientui.TranscriptQueuedMessageState{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		QueueItemID:     runtimeids.NewQueueItemID(),
		Status:          clientui.QueuedUserMessageFailed,
		FailureReason:   &reason,
		Text:            &text,
		PromptCommand:   &command,
	})
	for _, msg := range collectCmdMessages(t, cmd) {
		model = updateUIModel(t, model, msg)
	}

	if service.calls != 1 {
		t.Fatalf("catalog calls = %d, want one", service.calls)
	}
	if _, ok := model.commandRegistry.Command("/prompt:new"); !ok {
		t.Fatal("deferred missing-command failure did not install refreshed command")
	}
	if _, ok := model.commandRegistry.Command("/prompt:old"); ok {
		t.Fatal("deferred missing-command failure restored stale command")
	}
	if got := testMainInput(model); got != "" {
		t.Fatalf("deferred missing-command failure restored queued input %q", got)
	}
}
