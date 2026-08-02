package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/app/commands"
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
