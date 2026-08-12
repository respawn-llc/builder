package metadata

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestSessionPageSeparatesCategoriesAndMapsLegacyToMain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	updatedAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	importSessionPageFixture(t, store, cfg, binding, "legacy-session", nil, updatedAt.Add(2*time.Minute))
	importSessionPageFixture(t, store, cfg, binding, "main-session", sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), updatedAt.Add(time.Minute))
	importSessionPageFixture(t, store, cfg, binding, "subagent-session", sessionCategoryForPageTest(sessioncontract.SessionCategorySubagent), updatedAt)

	mainPage, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Limit:     sessionPageInt(10),
	})
	if err != nil {
		t.Fatalf("ListSessionPage main: %v", err)
	}
	requireSessionPageIDs(t, mainPage, "legacy-session", "main-session")

	subagentPage, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategorySubagent,
		Limit:     sessionPageInt(10),
	})
	if err != nil {
		t.Fatalf("ListSessionPage subagent: %v", err)
	}
	requireSessionPageIDs(t, subagentPage, "subagent-session")
}

func TestSessionPageReflectsPersistedCategoryPromotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	updatedAt := time.Now().UTC()
	category := sessioncontract.SessionCategorySubagent
	importSessionPageFixture(t, store, cfg, binding, "promoted-session", &category, updatedAt)

	snapshot, err := store.ResolvePersistedSession(ctx, "promoted-session")
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	promoted := sessioncontract.SessionCategoryMain
	snapshot.Meta.Category = &promoted
	snapshot.Meta.UpdatedAt = updatedAt.Add(time.Second)
	if err := store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: snapshot.SessionDir,
		Meta:       *snapshot.Meta,
	}); err != nil {
		t.Fatalf("ImportSessionSnapshot promotion: %v", err)
	}

	mainPage, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Limit:     sessionPageInt(10),
	})
	if err != nil {
		t.Fatalf("ListSessionPage main: %v", err)
	}
	requireSessionPageIDs(t, mainPage, "promoted-session")

	subagentPage, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategorySubagent,
		Limit:     sessionPageInt(10),
	})
	if err != nil {
		t.Fatalf("ListSessionPage subagent: %v", err)
	}
	requireSessionPageIDs(t, subagentPage)
}

func TestSessionPageUsesStableRecencyOrderAcrossAdjacentOffsets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	updatedAt := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"session-d", "session-c", "session-b", "session-a"} {
		importSessionPageFixture(t, store, cfg, binding, id, sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), updatedAt)
	}

	first, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    sessionPageInt(0),
		Limit:     sessionPageInt(2),
	})
	if err != nil {
		t.Fatalf("ListSessionPage first: %v", err)
	}
	requireSessionPageIDs(t, first, "session-d", "session-c")
	if first.NextOffset == nil || *first.NextOffset != 2 {
		t.Fatalf("first next offset = %v, want 2", first.NextOffset)
	}

	second, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    first.NextOffset,
		Limit:     sessionPageInt(2),
	})
	if err != nil {
		t.Fatalf("ListSessionPage second: %v", err)
	}
	requireSessionPageIDs(t, second, "session-b", "session-a")
	if second.NextOffset != nil {
		t.Fatalf("second next offset = %v, want nil", second.NextOffset)
	}

	beyond, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    sessionPageInt(10),
		Limit:     sessionPageInt(2),
	})
	if err != nil {
		t.Fatalf("ListSessionPage beyond end: %v", err)
	}
	requireSessionPageIDs(t, beyond)
	if beyond.NextOffset != nil {
		t.Fatalf("beyond next offset = %v, want nil", beyond.NextOffset)
	}
}

func TestSessionPageAcceptsLiveRepeatAndSkipAfterRecencyChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	base := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"session-c", "session-b", "session-a"} {
		importSessionPageFixture(t, store, cfg, binding, id, sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), base.Add(-time.Duration(index)*time.Minute))
	}
	first, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    sessionPageInt(0),
		Limit:     sessionPageInt(2),
	})
	if err != nil {
		t.Fatalf("ListSessionPage first: %v", err)
	}
	requireSessionPageIDs(t, first, "session-c", "session-b")
	if first.NextOffset == nil {
		t.Fatal("expected next offset")
	}

	importSessionPageFixture(t, store, cfg, binding, "session-a", sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), base.Add(time.Minute))
	second, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    first.NextOffset,
		Limit:     sessionPageInt(2),
	})
	if err != nil {
		t.Fatalf("ListSessionPage second: %v", err)
	}
	requireSessionPageIDs(t, second, "session-b")

	refreshed, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
		Offset:    sessionPageInt(0),
		Limit:     sessionPageInt(2),
	})
	if err != nil {
		t.Fatalf("ListSessionPage refreshed: %v", err)
	}
	requireSessionPageIDs(t, refreshed, "session-a", "session-c")
}

func TestSessionPageDefaultWindowTrimsOneHundredAndOneRowLookahead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	base := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	for index := range serverapi.MaxSessionPageSize + 1 {
		id := fmt.Sprintf("session-%03d", index)
		importSessionPageFixture(t, store, cfg, binding, id, sessionCategoryForPageTest(sessioncontract.SessionCategoryMain), base.Add(time.Duration(index)*time.Second))
	}

	page, err := store.ListSessionPage(ctx, serverapi.SessionPageRequest{
		ProjectID: binding.ProjectID,
		Category:  sessioncontract.SessionCategoryMain,
	})
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != serverapi.MaxSessionPageSize {
		t.Fatalf("session count = %d, want %d", len(page.Sessions), serverapi.MaxSessionPageSize)
	}
	if page.NextOffset == nil || *page.NextOffset != serverapi.MaxSessionPageSize {
		t.Fatalf("next offset = %v, want %d", page.NextOffset, serverapi.MaxSessionPageSize)
	}
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

func sessionPageInt(value int) *int {
	return &value
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
