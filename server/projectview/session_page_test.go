package projectview

import (
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestProjectViewServiceListsBoundedCategorySessionPages(t *testing.T) {
	store, cfg, binding := newProjectViewMetadataStore(t)
	base := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	importProjectViewSessionPageFixture(t, store, cfg, binding, "main-newer", sessioncontract.SessionCategoryMain, base)
	importProjectViewSessionPageFixture(t, store, cfg, binding, "main-older", sessioncontract.SessionCategoryMain, base.Add(-time.Minute))
	importProjectViewSessionPageFixture(t, store, cfg, binding, "subagent", sessioncontract.SessionCategorySubagent, base.Add(time.Minute))
	service := newProjectViewMetadataService(t, store, binding.ProjectID)

	page, err := service.ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  1,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID.String() != "main-newer" {
		t.Fatalf("main page = %+v", page)
	}
	if page.Sessions[0].Name != "main-newer" {
		t.Fatalf("renamed session name = %q, want %q", page.Sessions[0].Name, "main-newer")
	}
	if page.Older == nil {
		t.Fatal("expected older continuation")
	}

	older, err := service.ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  1,
		Position:  serverapi.OlderSessionPagePosition(*page.Older),
	})
	if err != nil {
		t.Fatalf("ListSessionPage older: %v", err)
	}
	if len(older.Sessions) != 1 || older.Sessions[0].SessionID.String() != "main-older" {
		t.Fatalf("older page = %+v", older)
	}
	if older.Newer == nil {
		t.Fatal("expected newer continuation")
	}
}

func TestProjectViewServiceSessionPageEnforcesAttachedProjectScope(t *testing.T) {
	store, _, binding := newProjectViewMetadataStore(t)
	service := newProjectViewMetadataService(t, store, binding.ProjectID)
	if _, err := service.ListSessionPage(t.Context(), serverapi.SessionPageRequest{
		ProjectID: "project-other",
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  20,
		Position:  serverapi.NewestSessionPagePosition(),
	}); err == nil {
		t.Fatal("ListSessionPage accepted an unavailable project")
	}
}

func importProjectViewSessionPageFixture(
	t *testing.T,
	store *metadata.Store,
	cfg config.App,
	binding metadata.Binding,
	id string,
	category sessioncontract.SessionCategory,
	updatedAt time.Time,
) {
	t.Helper()
	err := store.ImportSessionSnapshot(t.Context(), session.PersistedStoreSnapshot{
		SessionDir: filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions", id),
		Meta: session.Meta{
			SessionID:          id,
			Category:           &category,
			Name:               id,
			WorkspaceRoot:      binding.CanonicalRoot,
			WorkspaceContainer: filepath.Base(binding.CanonicalRoot),
			CreatedAt:          updatedAt.Add(-time.Hour),
			UpdatedAt:          updatedAt,
		},
	})
	if err != nil {
		t.Fatalf("ImportSessionSnapshot(%s): %v", id, err)
	}
}
