package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflow/label"
	"core/shared/config"
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

func TestTaskLabelsReadEmptyAndAddOne(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	projectLabel, err := store.CreateProjectLabel(ctx, binding.ProjectID, "area/runtime")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}

	assigned, err := store.GetTaskLabelIDs(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskLabelIDs empty: %v", err)
	}
	if len(assigned) != 0 {
		t.Fatalf("empty task labels = %+v", assigned)
	}
	before := taskUpdatedAtUnixMs(t, ctx, store, task.ID)
	assigned, err = store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      task.ID,
		AddLabelIDs: []string{projectLabel.ID.String()},
	})
	if err != nil {
		t.Fatalf("UpdateTaskLabels add: %v", err)
	}
	if len(assigned) != 1 || assigned[0] != projectLabel.ID {
		t.Fatalf("assigned labels = %+v, want %s", assigned, projectLabel.ID.String())
	}
	if got := taskUpdatedAtUnixMs(t, ctx, store, task.ID); got != before {
		t.Fatalf("task updated_at after assignment = %d, want %d", got, before)
	}
}

func TestTaskLabelUpdateIsIdempotentAtomicAndAlphabeticallyOrdered(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	zulu, err := store.CreateProjectLabel(ctx, binding.ProjectID, "Zulu")
	if err != nil {
		t.Fatalf("CreateProjectLabel Zulu: %v", err)
	}
	alpha, err := store.CreateProjectLabel(ctx, binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel alpha: %v", err)
	}
	absent, err := store.CreateProjectLabel(ctx, binding.ProjectID, "absent")
	if err != nil {
		t.Fatalf("CreateProjectLabel absent: %v", err)
	}

	assigned, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      task.ID,
		AddLabelIDs: []string{zulu.ID.String(), alpha.ID.String()},
	})
	if err != nil {
		t.Fatalf("UpdateTaskLabels add batch: %v", err)
	}
	if len(assigned) != 2 || assigned[0] != alpha.ID || assigned[1] != zulu.ID {
		t.Fatalf("ordered assigned labels = %+v, want alpha then Zulu", assigned)
	}
	assigned, err = store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:         task.ID,
		AddLabelIDs:    []string{alpha.ID.String()},
		RemoveLabelIDs: []string{absent.ID.String()},
	})
	if err != nil {
		t.Fatalf("UpdateTaskLabels idempotent add/remove: %v", err)
	}
	if len(assigned) != 2 || assigned[0] != alpha.ID || assigned[1] != zulu.ID {
		t.Fatalf("idempotent assigned labels = %+v", assigned)
	}
	assigned, err = store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:         task.ID,
		RemoveLabelIDs: []string{alpha.ID.String()},
	})
	if err != nil {
		t.Fatalf("UpdateTaskLabels remove: %v", err)
	}
	if len(assigned) != 1 || assigned[0] != zulu.ID {
		t.Fatalf("assigned labels after remove = %+v, want Zulu", assigned)
	}
}

func TestTaskLabelUpdateRejectsMalformedCollectionsBeforeMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	projectLabel, err := store.CreateProjectLabel(ctx, binding.ProjectID, "valid")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}

	for _, test := range []struct {
		name   string
		req    TaskLabelUpdateRequest
		reason TaskLabelMutationErrorReason
	}{
		{
			name: "duplicate add",
			req: TaskLabelUpdateRequest{
				TaskID:      task.ID,
				AddLabelIDs: []string{projectLabel.ID.String(), projectLabel.ID.String()},
			},
			reason: TaskLabelMutationDuplicateAdd,
		},
		{
			name: "duplicate remove",
			req: TaskLabelUpdateRequest{
				TaskID:         task.ID,
				RemoveLabelIDs: []string{projectLabel.ID.String(), projectLabel.ID.String()},
			},
			reason: TaskLabelMutationDuplicateRemove,
		},
		{
			name: "overlap",
			req: TaskLabelUpdateRequest{
				TaskID:         task.ID,
				AddLabelIDs:    []string{projectLabel.ID.String()},
				RemoveLabelIDs: []string{projectLabel.ID.String()},
			},
			reason: TaskLabelMutationOverlap,
		},
		{
			name: "invalid canonical ID",
			req: TaskLabelUpdateRequest{
				TaskID:      task.ID,
				AddLabelIDs: []string{"not-a-label-id"},
			},
			reason: TaskLabelMutationInvalidID,
		},
		{
			name: "raw 101 IDs precede ID parsing",
			req: TaskLabelUpdateRequest{
				TaskID:      task.ID,
				AddLabelIDs: make([]string, label.MaxProjectLabels+1),
			},
			reason: TaskLabelMutationTooManyAdd,
		},
		{
			name: "raw 101 remove IDs precede ID parsing",
			req: TaskLabelUpdateRequest{
				TaskID:         task.ID,
				RemoveLabelIDs: make([]string, label.MaxProjectLabels+1),
			},
			reason: TaskLabelMutationTooManyRemove,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.UpdateTaskLabels(ctx, test.req)
			var mutationErr TaskLabelMutationError
			if !errors.As(err, &mutationErr) || mutationErr.Reason != test.reason {
				t.Fatalf("UpdateTaskLabels error = %T %v, want reason %q", err, err, test.reason)
			}
			assigned, err := store.GetTaskLabelIDs(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetTaskLabelIDs after rejection: %v", err)
			}
			if len(assigned) != 0 {
				t.Fatalf("assignments after rejected mutation = %+v", assigned)
			}
		})
	}
}

func TestTaskLabelUpdateRejectsMissingAndWrongProjectReferencesAtomically(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	own, err := store.CreateProjectLabel(ctx, binding.ProjectID, "own")
	if err != nil {
		t.Fatalf("CreateProjectLabel own: %v", err)
	}
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	foreign, err := store.CreateProjectLabel(ctx, other.ProjectID, "foreign")
	if err != nil {
		t.Fatalf("CreateProjectLabel foreign: %v", err)
	}
	missing := label.NewID()

	if _, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      task.ID,
		AddLabelIDs: []string{own.ID.String(), foreign.ID.String()},
	}); !errors.Is(err, ErrTaskLabelWrongProject) {
		t.Fatalf("UpdateTaskLabels wrong project = %v, want ErrTaskLabelWrongProject", err)
	}
	if _, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      task.ID,
		AddLabelIDs: []string{own.ID.String(), missing.String()},
	}); !errors.Is(err, ErrTaskLabelNotFound) {
		t.Fatalf("UpdateTaskLabels missing label = %v, want ErrTaskLabelNotFound", err)
	}
	if _, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      workflow.TaskID("missing-task"),
		AddLabelIDs: []string{own.ID.String()},
	}); !errors.Is(err, ErrTaskLabelTaskNotFound) {
		t.Fatalf("UpdateTaskLabels missing task = %v, want ErrTaskLabelTaskNotFound", err)
	}
	assigned, err := store.GetTaskLabelIDs(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskLabelIDs after atomic failures: %v", err)
	}
	if len(assigned) != 0 {
		t.Fatalf("assignments after atomic failures = %+v", assigned)
	}
	if _, err := store.GetTaskLabelIDs(ctx, workflow.TaskID("missing-task")); !errors.Is(err, ErrTaskLabelTaskNotFound) {
		t.Fatalf("GetTaskLabelIDs missing task = %v, want ErrTaskLabelTaskNotFound", err)
	}
}

func TestTaskLabelAssignmentSupportsEveryTaskLifecycleState(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	projectLabel, err := store.CreateProjectLabel(ctx, binding.ProjectID, "lifecycle")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}

	backlog := createDefaultTask(t, ctx, store, binding.ProjectID)

	active := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, active.ID)

	admitted := createDefaultTask(t, ctx, store, binding.ProjectID)
	admittedStart := startTask(t, ctx, store, admitted.ID)
	if err := store.AdmitCurrentNode(ctx, admittedStart.Mutation.Created[0].Reference); err != nil {
		t.Fatalf("AdmitCurrentNode: %v", err)
	}

	interrupted := createDefaultTask(t, ctx, store, binding.ProjectID)
	interruptedStart := startTask(t, ctx, store, interrupted.ID)
	if err := store.InterruptCurrentNode(ctx, interruptedStart.Mutation.Created[0].Reference, "test", workflow.CurrentNodeInterruptionDetail{Code: "test"}); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}

	done := createDefaultTask(t, ctx, store, binding.ProjectID)
	doneStart := startTask(t, ctx, store, done.ID)
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       doneStart.Mutation.Created[0].Reference,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}

	for name, task := range map[string]TaskRecord{
		"backlog":     backlog,
		"active":      active,
		"admitted":    admitted,
		"interrupted": interrupted,
		"done":        done,
	} {
		t.Run(name, func(t *testing.T) {
			assigned, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
				TaskID:      task.ID,
				AddLabelIDs: []string{projectLabel.ID.String()},
			})
			if err != nil {
				t.Fatalf("UpdateTaskLabels: %v", err)
			}
			if len(assigned) != 1 || assigned[0] != projectLabel.ID {
				t.Fatalf("assigned labels = %+v", assigned)
			}
			assigned, err = store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
				TaskID:         task.ID,
				RemoveLabelIDs: []string{projectLabel.ID.String()},
			})
			if err != nil {
				t.Fatalf("UpdateTaskLabels remove: %v", err)
			}
			if len(assigned) != 0 {
				t.Fatalf("assigned labels after remove = %+v", assigned)
			}
		})
	}
}

func openConcurrentWorkflowStores(t *testing.T, cfg config.App) (*Store, *Store) {
	t.Helper()
	open := func() *Store {
		metadataStore, err := metadata.Open(cfg.PersistenceRoot)
		if err != nil {
			t.Fatalf("metadata.Open concurrent store: %v", err)
		}
		t.Cleanup(func() {
			if err := metadataStore.Close(); err != nil {
				t.Errorf("close concurrent metadata store: %v", err)
			}
		})
		store, err := New(metadataStore)
		if err != nil {
			t.Fatalf("workflowstore.New concurrent store: %v", err)
		}
		return store
	}
	return open(), open()
}

func TestTaskLabelUpdateAcceptsTheFullProjectCatalog(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	rawIDs := make([]string, 0, label.MaxProjectLabels)
	for index := label.MaxProjectLabels - 1; index >= 0; index-- {
		projectLabel, err := store.CreateProjectLabel(ctx, binding.ProjectID, fmt.Sprintf("label-%03d", index))
		if err != nil {
			t.Fatalf("CreateProjectLabel %d: %v", index, err)
		}
		rawIDs = append(rawIDs, projectLabel.ID.String())
	}

	assigned, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      task.ID,
		AddLabelIDs: rawIDs,
	})
	if err != nil {
		t.Fatalf("UpdateTaskLabels full catalog: %v", err)
	}
	if len(assigned) != label.MaxProjectLabels {
		t.Fatalf("assigned label count = %d, want %d", len(assigned), label.MaxProjectLabels)
	}
	catalog, err := store.ListProjectLabels(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels: %v", err)
	}
	for index := range catalog {
		if assigned[index] != catalog[index].ID {
			t.Fatalf("assigned order at %d = %s, want %s", index, assigned[index].String(), catalog[index].ID.String())
		}
	}
}

func TestTaskCreateCommitsLabelsAtomically(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	zulu, err := store.CreateProjectLabel(ctx, binding.ProjectID, "Zulu")
	if err != nil {
		t.Fatalf("CreateProjectLabel Zulu: %v", err)
	}
	alpha, err := store.CreateProjectLabel(ctx, binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel alpha: %v", err)
	}

	task, err := store.CreateTask(ctx, CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Labeled task",
		Body:      "Body",
		LabelIDs:  []string{zulu.ID.String(), alpha.ID.String()},
	})
	if err != nil {
		t.Fatalf("CreateTask labeled: %v", err)
	}
	assigned, err := store.GetTaskLabelIDs(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskLabelIDs: %v", err)
	}
	if len(assigned) != 2 || assigned[0] != alpha.ID || assigned[1] != zulu.ID {
		t.Fatalf("created task labels = %+v, want alpha then Zulu", assigned)
	}
}

func TestTaskCreateInvalidLabelsRollBackCurrentNodeAndAssignments(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	own, err := store.CreateProjectLabel(ctx, binding.ProjectID, "own")
	if err != nil {
		t.Fatalf("CreateProjectLabel own: %v", err)
	}
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other Project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	foreign, err := store.CreateProjectLabel(ctx, other.ProjectID, "foreign")
	if err != nil {
		t.Fatalf("CreateProjectLabel foreign: %v", err)
	}
	missing := label.NewID()

	for _, test := range []struct {
		name     string
		labelIDs []string
		target   error
	}{
		{
			name:     "missing label",
			labelIDs: []string{own.ID.String(), missing.String()},
			target:   ErrTaskLabelNotFound,
		},
		{
			name:     "wrong project label",
			labelIDs: []string{own.ID.String(), foreign.ID.String()},
			target:   ErrTaskLabelWrongProject,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeSequence := projectNextTaskSequence(t, ctx, store, binding.ProjectID)
			if _, err := store.CreateTask(ctx, CreateTaskRequest{
				ProjectID: binding.ProjectID,
				Title:     "Invalid labeled task",
				Body:      "Body",
				LabelIDs:  test.labelIDs,
			}); !errors.Is(err, test.target) {
				t.Fatalf("CreateTask error = %v, want %v", err, test.target)
			}
			assertTaskCreationUnchanged(t, ctx, store, binding.ProjectID, beforeSequence)
			var assignmentCount int
			if err := store.db.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM task_label_assignments`,
			).Scan(&assignmentCount); err != nil {
				t.Fatalf("count task label assignments: %v", err)
			}
			if assignmentCount != 0 {
				t.Fatalf("assignment count after rollback = %d, want 0", assignmentCount)
			}
		})
	}
}

func TestLabelDeleteRacingWithAssignmentConvergesWithoutOrphans(t *testing.T) {
	ctx, fixtureStore, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, fixtureStore, binding.ProjectID)
	task := createDefaultTask(t, ctx, fixtureStore, binding.ProjectID)
	projectLabel, err := fixtureStore.CreateProjectLabel(ctx, binding.ProjectID, "race")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	assignStore, deleteStore := openConcurrentWorkflowStores(t, cfg)

	start := make(chan struct{})
	assignmentResult := make(chan error, 1)
	deleteResult := make(chan error, 1)
	go func() {
		<-start
		_, err := assignStore.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
			TaskID:      task.ID,
			AddLabelIDs: []string{projectLabel.ID.String()},
		})
		assignmentResult <- err
	}()
	go func() {
		<-start
		_, err := deleteStore.DeleteProjectLabel(ctx, binding.ProjectID, projectLabel.ID)
		deleteResult <- err
	}()
	close(start)

	if err := <-deleteResult; err != nil {
		t.Fatalf("DeleteProjectLabel race: %v", err)
	}
	if err := <-assignmentResult; err != nil && !errors.Is(err, ErrTaskLabelNotFound) {
		t.Fatalf("UpdateTaskLabels race = %v, want success or ErrTaskLabelNotFound", err)
	}
	labels, err := fixtureStore.ListProjectLabels(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectLabels after race: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("catalog after delete race = %+v, want empty", labels)
	}
	assigned, err := fixtureStore.GetTaskLabelIDs(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskLabelIDs after race: %v", err)
	}
	if len(assigned) != 0 {
		t.Fatalf("assignments after delete race = %+v, want empty", assigned)
	}
}
