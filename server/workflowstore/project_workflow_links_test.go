package workflowstore

import (
	"errors"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
	sqlitedriver "modernc.org/sqlite"
)

func TestProjectWorkflowLinkMutationsTranslateSQLiteRelations(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)

	t.Run("idempotent existing relation", func(t *testing.T) {
		first, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowID, WorkflowLinkDefaultNever)
		if err != nil {
			t.Fatalf("first LinkWorkflowWithDefaultPolicy: %v", err)
		}
		second, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowID, WorkflowLinkDefaultNever)
		if err != nil {
			t.Fatalf("second LinkWorkflowWithDefaultPolicy: %v", err)
		}
		if second.ID != first.ID {
			t.Fatalf("idempotent link ID = %q, want %q", second.ID, first.ID)
		}
	})

	t.Run("missing project", func(t *testing.T) {
		_, err := store.LinkWorkflowWithDefaultPolicy(ctx, "project-missing", workflowID, WorkflowLinkDefaultNever)
		assertProjectWorkflowLinkError(t, err, serverapi.ErrProjectNotFound)
	})

	t.Run("missing workflow", func(t *testing.T) {
		_, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, runtimeids.NewWorkflowID(), WorkflowLinkDefaultNever)
		assertProjectWorkflowLinkError(t, err, ErrWorkflowNotFound)
	})

	t.Run("missing default link", func(t *testing.T) {
		_, err := store.SetDefaultProjectWorkflowLink(ctx, binding.ProjectID, runtimeids.NewWorkflowID())
		assertProjectWorkflowLinkError(t, err, ErrProjectWorkflowLinkNotFound)
	})
}

func TestUnlinkProjectWorkflowTranslatesInvalidReplacementRelation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	first := linkWorkflow(t, ctx, store, binding.ProjectID, createValidWorkflow(t, ctx, store), true)
	second := linkWorkflow(t, ctx, store, binding.ProjectID, createValidWorkflow(t, ctx, store), false)
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	foreign := linkWorkflow(t, ctx, store, other.ProjectID, createValidWorkflow(t, ctx, store), true)

	for name, replacementID := range map[string]string{
		"missing":         "workflow-link-missing",
		"another project": foreign.ID,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.UnlinkProjectWorkflow(ctx, first.ID, replacementID)
			assertProjectWorkflowLinkError(t, err, ErrReplacementDefaultInvalid)
			if _, getErr := store.GetProjectWorkflowLink(ctx, first.ID); getErr != nil {
				t.Fatalf("failed unlink removed source link: %v", getErr)
			}
			if _, getErr := store.GetProjectWorkflowLink(ctx, second.ID); getErr != nil {
				t.Fatalf("failed unlink removed sibling link: %v", getErr)
			}
		})
	}
}

func assertProjectWorkflowLinkError(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) {
		t.Fatalf("operation exposed SQLite error: %v", err)
	}
}
