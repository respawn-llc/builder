package metadata

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestSessionPageSeparatesCategoriesAndMapsLegacyToMain(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	updatedAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	importSessionPageFixture(t, store, cfg, binding, "legacy-session", nil, updatedAt.Add(2*time.Minute))
	importSessionPageFixture(t, store, cfg, binding, "main-session", sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), updatedAt.Add(time.Minute))
	importSessionPageFixture(t, store, cfg, binding, "subagent-session", sessionCategoryForPageTest(sessioncontract.SessionCategorySubagent), updatedAt)

	mainPage, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  10,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage main: %v", err)
	}
	requireSessionPageIDs(t, mainPage, "legacy-session", "main-session")

	subagentPage, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategorySubagent,
		PageSize:  10,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage subagent: %v", err)
	}
	requireSessionPageIDs(t, subagentPage, "subagent-session")
}

func TestSessionPageNavigatesOlderAndNewerWithStableTieBreaks(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	updatedAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"session-d", "session-c", "session-b", "session-a"} {
		importSessionPageFixture(t, store, cfg, binding, id, sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), updatedAt)
	}

	newest, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  2,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage newest: %v", err)
	}
	requireSessionPageIDs(t, newest, "session-d", "session-c")
	if newest.Older == nil || newest.Newer != nil {
		t.Fatalf("newest continuations older=%v newer=%v", newest.Older, newest.Newer)
	}

	older, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  2,
		Position:  serverapi.OlderSessionPagePosition(*newest.Older),
	})
	if err != nil {
		t.Fatalf("ListSessionPage older: %v", err)
	}
	requireSessionPageIDs(t, older, "session-b", "session-a")
	if older.Older != nil || older.Newer == nil {
		t.Fatalf("older continuations older=%v newer=%v", older.Older, older.Newer)
	}

	newer, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  2,
		Position:  serverapi.NewerSessionPagePosition(*older.Newer),
	})
	if err != nil {
		t.Fatalf("ListSessionPage newer: %v", err)
	}
	requireSessionPageIDs(t, newer, "session-d", "session-c")
}

func TestSessionPageRejectsInvalidAndCrossScopeTokens(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	other, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	importSessionPageFixture(t, store, cfg, binding, "session-main", sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), time.Now().UTC())
	first, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  1,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if first.Older == nil {
		importSessionPageFixture(t, store, cfg, binding, "session-main-older", sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), time.Now().UTC().Add(-time.Hour))
		first, err = store.ListSessionPage(ctx, serverapi.SessionPageRequest{
			ProjectID: binding.ProjectID,
			Category:  sessioncontract.SessionCategoryMain,
			PageSize:  1,
			Position:  serverapi.NewestSessionPagePosition(),
		})
		if err != nil {
			t.Fatalf("ListSessionPage after older seed: %v", err)
		}
	}
	if first.Older == nil {
		t.Fatal("expected older continuation")
	}
	tokenJSON, err := base64.RawURLEncoding.DecodeString(first.Older.String())
	if err != nil {
		t.Fatalf("decode valid continuation: %v", err)
	}
	trailingJSON, err := serverapi.ParseSessionPageContinuation(
		base64.RawURLEncoding.EncodeToString(append(tokenJSON, []byte("{}")...)),
	)
	if err != nil {
		t.Fatalf("parse trailing-json continuation: %v", err)
	}

	invalid, err := serverapi.ParseSessionPageContinuation("not-base64-json")
	if err != nil {
		t.Fatalf("parse opaque invalid continuation: %v", err)
	}
	for _, request := range []serverapi.SessionPageRequest{
		{ProjectID: binding.ProjectID, Category: sessioncontract.SessionCategoryMain, PageSize: 1, Position: serverapi.OlderSessionPagePosition(invalid)},
		{ProjectID: binding.ProjectID, Category: sessioncontract.SessionCategoryMain, PageSize: 1, Position: serverapi.OlderSessionPagePosition(trailingJSON)},
		{ProjectID: binding.ProjectID, Category: sessioncontract.SessionCategorySubagent, PageSize: 1, Position: serverapi.OlderSessionPagePosition(*first.Older)},
		{ProjectID: other.ProjectID, Category: sessioncontract.SessionCategoryMain, PageSize: 1, Position: serverapi.OlderSessionPagePosition(*first.Older)},
	} {
		if _, err := store.ListSessionPage(ctx, request); !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("ListSessionPage error = %v, want ErrInvalidPageToken for %+v", err, request)
		}
	}
}

func TestSessionPageUsesLiveRecencyWithoutOffsetDrift(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	base := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"session-c", "session-b", "session-a"} {
		importSessionPageFixture(t, store, cfg, binding, id, sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), base.Add(-time.Duration(index)*time.Minute))
	}
	first, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  2,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage first: %v", err)
	}
	requireSessionPageIDs(t, first, "session-c", "session-b")
	if first.Older == nil {
		t.Fatal("expected older continuation")
	}

	importSessionPageFixture(t, store, cfg, binding, "session-a", sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), base.Add(time.Minute))
	older, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  2,
		Position:  serverapi.OlderSessionPagePosition(*first.Older),
	})
	if err != nil {
		t.Fatalf("ListSessionPage older: %v", err)
	}
	requireSessionPageIDs(t, older)

	refreshed, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		PageSize:  2,
		Position:  serverapi.NewestSessionPagePosition(),
	})
	if err != nil {
		t.Fatalf("ListSessionPage refreshed: %v", err)
	}
	requireSessionPageIDs(t, refreshed, "session-a", "session-c")
}

func importSessionPageFixture(
	t *testing.T,
	store *Store,
	cfg config.App,
	binding Binding,
	id string,
	category *sessioncontract.SessionCategory,
	updatedAt time.Time,
) {
	t.Helper()
	err := store.ImportSessionSnapshot(t.Context(), session.PersistedStoreSnapshot{
		SessionDir: filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions", id),
		Meta: session.Meta{
			SessionID:          id,
			Category:           category,
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

func sessionCategoryForPageTest(category sessioncontract.SessionCategory) *sessioncontract.SessionCategory {
	return &category
}

func requireSessionPageIDs(t *testing.T, page serverapi.SessionPageResponse, want ...string) {
	t.Helper()
	if len(page.Sessions) != len(want) {
		t.Fatalf("session count = %d, want %d: %+v", len(page.Sessions), len(want), page.Sessions)
	}
	for index := range want {
		if got := page.Sessions[index].SessionID.String(); got != want[index] {
			t.Fatalf("session %d = %q, want %q", index, got, want[index])
		}
	}
}
