package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"core/server/workflow"
	"core/server/workflow/label"
	"core/shared/serverapi"
)

func TestProjectLabelsCreateAndList(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)

	beta, err := store.CreateProjectLabel(ctx, binding.ProjectID, "  Beta  ")
	if err != nil {
		t.Fatalf("CreateProjectLabel Beta: %v", err)
	}
	alpha, err := store.CreateProjectLabel(ctx, binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel alpha: %v", err)
	}

	for _, record := range []ProjectLabelRecord{beta, alpha} {
		if record.ProjectID != binding.ProjectID {
			t.Fatalf("created label project = %q, want %q", record.ProjectID, binding.ProjectID)
		}
		if _, err := label.ParseID(record.ID.String()); err != nil {
			t.Fatalf("created label ID %q: %v", record.ID.String(), err)
		}
	}
	if beta.Name.String() != "Beta" {
		t.Fatalf("created Beta name = %q", beta.Name.String())
	}

	labels, err := store.ListProjectLabels(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("label count = %d, want 2: %+v", len(labels), labels)
	}
	if labels[0].ID != alpha.ID || labels[1].ID != beta.ID {
		t.Fatalf("label order = %+v, want alpha then Beta", labels)
	}
}

func TestProjectLabelRenamePreservesIdentityAndAllowsCapitalizationOnlyChange(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, err := store.CreateProjectLabel(ctx, binding.ProjectID, "Zulu")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}

	renamed, err := store.RenameProjectLabel(ctx, binding.ProjectID, created.ID, " alpha ")
	if err != nil {
		t.Fatalf("RenameProjectLabel alpha: %v", err)
	}
	if renamed.ID != created.ID || renamed.ProjectID != binding.ProjectID || renamed.Name.String() != "alpha" {
		t.Fatalf("renamed label = %+v, want stable identity and prepared alpha", renamed)
	}

	capitalized, err := store.RenameProjectLabel(ctx, binding.ProjectID, created.ID, "ALPHA")
	if err != nil {
		t.Fatalf("RenameProjectLabel capitalization only: %v", err)
	}
	if capitalized.ID != created.ID || capitalized.Name.String() != "ALPHA" {
		t.Fatalf("capitalized label = %+v", capitalized)
	}
}

func TestProjectLabelDeleteIsProjectScopedAndCascadesAssignments(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	created, err := store.CreateProjectLabel(ctx, binding.ProjectID, "obsolete")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`INSERT INTO task_label_assignments (task_id, label_id) VALUES (?, ?)`,
		task.ID,
		created.ID.String(),
	); err != nil {
		t.Fatalf("seed task label assignment: %v", err)
	}
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}

	if _, err := store.DeleteProjectLabel(ctx, other.ProjectID, created.ID); err == nil {
		t.Fatal("DeleteProjectLabel succeeded through another project")
	}
	deleted, err := store.DeleteProjectLabel(ctx, binding.ProjectID, created.ID)
	if err != nil {
		t.Fatalf("DeleteProjectLabel: %v", err)
	}
	if deleted.ID != created.ID || deleted.ProjectID != binding.ProjectID || deleted.Name != created.Name {
		t.Fatalf("deleted label = %+v, want %+v", deleted, created)
	}
	var assignmentCount int
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM task_label_assignments WHERE task_id = ?`,
		task.ID,
	).Scan(&assignmentCount); err != nil {
		t.Fatalf("count task label assignments: %v", err)
	}
	if assignmentCount != 0 {
		t.Fatalf("assignment count after delete = %d, want 0", assignmentCount)
	}
}

func TestProjectLabelCatalogIsolationAndTypedMissingEntities(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	first, err := store.CreateProjectLabel(ctx, binding.ProjectID, "shared name")
	if err != nil {
		t.Fatalf("CreateProjectLabel first project: %v", err)
	}
	second, err := store.CreateProjectLabel(ctx, other.ProjectID, "SHARED NAME")
	if err != nil {
		t.Fatalf("CreateProjectLabel other project: %v", err)
	}

	firstLabels, err := store.ListProjectLabels(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels first project: %v", err)
	}
	otherLabels, err := store.ListProjectLabels(ctx, other.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels other project: %v", err)
	}
	if len(firstLabels) != 1 || firstLabels[0].ID != first.ID {
		t.Fatalf("first project labels = %+v", firstLabels)
	}
	if len(otherLabels) != 1 || otherLabels[0].ID != second.ID {
		t.Fatalf("other project labels = %+v", otherLabels)
	}

	if _, err := store.ListProjectLabels(ctx, "missing-project"); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("ListProjectLabels missing project = %v, want ErrProjectNotFound", err)
	}
	if _, err := store.CreateProjectLabel(ctx, "missing-project", "name"); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("CreateProjectLabel missing project = %v, want ErrProjectNotFound", err)
	}
	if _, err := store.RenameProjectLabel(ctx, binding.ProjectID, second.ID, "renamed"); !errors.Is(err, ErrProjectLabelNotFound) {
		t.Fatalf("RenameProjectLabel wrong project = %v, want ErrProjectLabelNotFound", err)
	} else {
		var notFound ProjectLabelNotFoundError
		if !errors.As(err, &notFound) || notFound.ProjectID != binding.ProjectID || notFound.LabelID != second.ID.String() {
			t.Fatalf("RenameProjectLabel not-found detail = %+v, error=%v", notFound, err)
		}
	}
	if _, err := store.DeleteProjectLabel(ctx, binding.ProjectID, second.ID); !errors.Is(err, ErrProjectLabelNotFound) {
		t.Fatalf("DeleteProjectLabel wrong project = %v, want ErrProjectLabelNotFound", err)
	}
	if _, err := store.RenameProjectLabel(ctx, "missing-project", second.ID, "renamed"); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("RenameProjectLabel missing project = %v, want ErrProjectNotFound", err)
	}
	if _, err := store.DeleteProjectLabel(ctx, "missing-project", second.ID); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("DeleteProjectLabel missing project = %v, want ErrProjectNotFound", err)
	}
}

func TestProjectLabelCatalogEnforcesUnicodeNameUniquenessAndTheHundredLabelLimit(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	first, err := store.CreateProjectLabel(ctx, binding.ProjectID, "Straße")
	if err != nil {
		t.Fatalf("CreateProjectLabel Straße: %v", err)
	}
	if _, err := store.CreateProjectLabel(ctx, binding.ProjectID, "STRASSE"); !errors.Is(err, ErrProjectLabelNameConflict) {
		t.Fatalf("CreateProjectLabel folded duplicate = %v, want ErrProjectLabelNameConflict", err)
	}

	other, err := store.CreateProjectLabel(ctx, binding.ProjectID, "other")
	if err != nil {
		t.Fatalf("CreateProjectLabel other: %v", err)
	}
	if _, err := store.RenameProjectLabel(ctx, binding.ProjectID, other.ID, "strasse"); !errors.Is(err, ErrProjectLabelNameConflict) {
		t.Fatalf("RenameProjectLabel folded duplicate = %v, want ErrProjectLabelNameConflict", err)
	}

	for index := 2; index < label.MaxProjectLabels; index++ {
		if _, err := store.CreateProjectLabel(ctx, binding.ProjectID, fmt.Sprintf("label-%03d", index)); err != nil {
			t.Fatalf("CreateProjectLabel %d: %v", index, err)
		}
	}
	labels, err := store.ListProjectLabels(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels at limit: %v", err)
	}
	if len(labels) != label.MaxProjectLabels {
		t.Fatalf("label count = %d, want %d", len(labels), label.MaxProjectLabels)
	}
	if _, err := store.CreateProjectLabel(ctx, binding.ProjectID, "one-too-many"); !errors.Is(err, ErrProjectLabelLimitReached) {
		t.Fatalf("CreateProjectLabel 101 = %v, want ErrProjectLabelLimitReached", err)
	}

	if _, err := store.DeleteProjectLabel(ctx, binding.ProjectID, first.ID); err != nil {
		t.Fatalf("DeleteProjectLabel at limit: %v", err)
	}
	if _, err := store.CreateProjectLabel(ctx, binding.ProjectID, "replacement"); err != nil {
		t.Fatalf("CreateProjectLabel after delete: %v", err)
	}
}

func TestConcurrentProjectLabelCreatesResolveToTypedConflictAndAtomicLimit(t *testing.T) {
	ctx, fixtureStore, binding, cfg := newTestStoreWithConfigContext(t)
	createStore, competingStore := openConcurrentWorkflowStores(t, cfg)

	results := raceProjectLabelCreates(
		func() (ProjectLabelRecord, error) {
			return createStore.CreateProjectLabel(ctx, binding.ProjectID, "Concurrent")
		},
		func() (ProjectLabelRecord, error) {
			return competingStore.CreateProjectLabel(ctx, binding.ProjectID, "concurrent")
		},
	)
	assertOneProjectLabelCreateError(t, results, ErrProjectLabelNameConflict)

	for index := 1; index < label.MaxProjectLabels-1; index++ {
		if _, err := fixtureStore.CreateProjectLabel(ctx, binding.ProjectID, fmt.Sprintf("capacity-%03d", index)); err != nil {
			t.Fatalf("fill label capacity %d: %v", index, err)
		}
	}
	results = raceProjectLabelCreates(
		func() (ProjectLabelRecord, error) {
			return createStore.CreateProjectLabel(ctx, binding.ProjectID, "final-a")
		},
		func() (ProjectLabelRecord, error) {
			return competingStore.CreateProjectLabel(ctx, binding.ProjectID, "final-b")
		},
	)
	assertOneProjectLabelCreateError(t, results, ErrProjectLabelLimitReached)

	labels, err := fixtureStore.ListProjectLabels(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels after concurrent limit race: %v", err)
	}
	if len(labels) != label.MaxProjectLabels {
		t.Fatalf("label count after concurrent limit race = %d, want %d", len(labels), label.MaxProjectLabels)
	}
}

type projectLabelCreateResult struct {
	record ProjectLabelRecord
	err    error
}

func raceProjectLabelCreates(
	first func() (ProjectLabelRecord, error),
	second func() (ProjectLabelRecord, error),
) [2]projectLabelCreateResult {
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	results := make(chan projectLabelCreateResult, 2)
	run := func(create func() (ProjectLabelRecord, error)) {
		ready.Done()
		<-start
		record, err := create()
		results <- projectLabelCreateResult{record: record, err: err}
	}
	go run(first)
	go run(second)
	ready.Wait()
	close(start)
	return [2]projectLabelCreateResult{<-results, <-results}
}

func assertOneProjectLabelCreateError(t *testing.T, results [2]projectLabelCreateResult, target error) {
	t.Helper()
	successes := 0
	failures := 0
	for _, result := range results {
		switch {
		case result.err == nil:
			successes++
			if result.record.ID.String() == "" {
				t.Fatal("successful label create returned no ID")
			}
		case errors.Is(result.err, target):
			failures++
		default:
			t.Fatalf("concurrent label create error = %T %v, want %v", result.err, result.err, target)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent label create outcomes = %+v, want one success and one %v", results, target)
	}
}

func TestProjectLabelCatalogMutationsDoNotTouchTaskOrderingTimestamp(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	assigned, err := store.CreateProjectLabel(ctx, binding.ProjectID, "assigned")
	if err != nil {
		t.Fatalf("CreateProjectLabel assigned: %v", err)
	}
	unassigned, err := store.CreateProjectLabel(ctx, binding.ProjectID, "unassigned")
	if err != nil {
		t.Fatalf("CreateProjectLabel unassigned: %v", err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`INSERT INTO task_label_assignments (task_id, label_id) VALUES (?, ?)`,
		task.ID,
		assigned.ID.String(),
	); err != nil {
		t.Fatalf("seed task label assignment: %v", err)
	}
	before := taskUpdatedAtUnixMs(t, ctx, store, task.ID)

	if _, err := store.CreateProjectLabel(ctx, binding.ProjectID, "created later"); err != nil {
		t.Fatalf("CreateProjectLabel later: %v", err)
	}
	if _, err := store.RenameProjectLabel(ctx, binding.ProjectID, assigned.ID, "renamed assigned"); err != nil {
		t.Fatalf("RenameProjectLabel assigned: %v", err)
	}
	if _, err := store.DeleteProjectLabel(ctx, binding.ProjectID, unassigned.ID); err != nil {
		t.Fatalf("DeleteProjectLabel unassigned: %v", err)
	}
	if got := taskUpdatedAtUnixMs(t, ctx, store, task.ID); got != before {
		t.Fatalf("task updated_at after catalog mutations = %d, want unchanged %d", got, before)
	}
	var assignedCount int
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM task_label_assignments WHERE task_id = ? AND label_id = ?`,
		task.ID,
		assigned.ID.String(),
	).Scan(&assignedCount); err != nil {
		t.Fatalf("count retained task label assignment: %v", err)
	}
	if assignedCount != 1 {
		t.Fatalf("retained assignment count = %d, want 1", assignedCount)
	}

	if _, err := store.DeleteProjectLabel(ctx, binding.ProjectID, assigned.ID); err != nil {
		t.Fatalf("DeleteProjectLabel assigned: %v", err)
	}
	if got := taskUpdatedAtUnixMs(t, ctx, store, task.ID); got != before {
		t.Fatalf("task updated_at after assigned label delete = %d, want unchanged %d", got, before)
	}
}

func taskUpdatedAtUnixMs(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID) int64 {
	t.Helper()
	var updatedAt int64
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT updated_at_unix_ms FROM tasks WHERE id = ?`,
		taskID,
	).Scan(&updatedAt); err != nil {
		t.Fatalf("query task updated_at: %v", err)
	}
	return updatedAt
}
