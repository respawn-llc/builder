package workflowview

import (
	"context"
	"errors"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflow/label"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

type taskListLabelFilterFixture struct {
	ctx        context.Context
	projectID  string
	workflowID workflow.WorkflowID
	metadata   *metadata.Store
	store      *workflowstore.Store
	view       *workflowViewTestFixture
	alpha      label.ID
	beta       label.ID
	gamma      label.ID
	taskIDs    map[string]string
}

func newTaskListLabelFilterFixture(t *testing.T) taskListLabelFilterFixture {
	t.Helper()
	ctx, metadataStore, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	alpha, err := workflowStore.CreateProjectLabel(ctx, binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel alpha: %v", err)
	}
	beta, err := workflowStore.CreateProjectLabel(ctx, binding.ProjectID, "beta")
	if err != nil {
		t.Fatalf("CreateProjectLabel beta: %v", err)
	}
	gamma, err := workflowStore.CreateProjectLabel(ctx, binding.ProjectID, "gamma")
	if err != nil {
		t.Fatalf("CreateProjectLabel gamma: %v", err)
	}
	createTask := func(title string, labelIDs ...string) string {
		t.Helper()
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID: binding.ProjectID,
			Title:     title,
			LabelIDs:  labelIDs,
		})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return string(task.ID)
	}
	return taskListLabelFilterFixture{
		ctx:        ctx,
		projectID:  binding.ProjectID,
		workflowID: workflowID,
		metadata:   metadataStore,
		store:      workflowStore,
		view:       view,
		alpha:      alpha.ID,
		beta:       beta.ID,
		gamma:      gamma.ID,
		taskIDs: map[string]string{
			"alpha":     createTask("alpha", alpha.ID.String()),
			"beta":      createTask("beta", beta.ID.String()),
			"both":      createTask("both", alpha.ID.String(), beta.ID.String()),
			"gamma":     createTask("gamma", gamma.ID.String()),
			"unlabeled": createTask("unlabeled"),
		},
	}
}

func (f taskListLabelFilterFixture) list(t *testing.T, filter serverapi.WorkflowTaskLabelFilter) serverapi.WorkflowTaskListResponse {
	t.Helper()
	response, err := f.request(t, filter)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return response
}

func (f taskListLabelFilterFixture) request(t *testing.T, filter serverapi.WorkflowTaskLabelFilter) (serverapi.WorkflowTaskListResponse, error) {
	t.Helper()
	return f.view.tasks(t).List(f.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &f.projectID,
		LabelFilter: filter,
	})
}

func requireTaskListIDs(t *testing.T, response serverapi.WorkflowTaskListResponse, want ...string) {
	t.Helper()
	got := make(map[string]bool, len(response.Tasks))
	for _, task := range response.Tasks {
		got[task.TaskID] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, taskID := range want {
		wantSet[taskID] = true
	}
	if len(got) != len(wantSet) {
		t.Fatalf("matching task ids = %v, want %v", got, wantSet)
	}
	for taskID := range wantSet {
		if !got[taskID] {
			t.Fatalf("matching task ids = %v, missing %s", got, taskID)
		}
	}
}

func namedTaskLabelFilter(mode serverapi.WorkflowTaskNamedLabelFilterMode, labelIDs ...string) serverapi.WorkflowTaskLabelFilter {
	return serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:     mode,
			LabelIDs: append([]string(nil), labelIDs...),
		},
	}
}

func namedTaskLabelFilterWithExclusions(
	mode serverapi.WorkflowTaskNamedLabelFilterMode,
	labelIDs []string,
	excludedLabelIDs []string,
) serverapi.WorkflowTaskLabelFilter {
	return serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:             mode,
			LabelIDs:         append([]string(nil), labelIDs...),
			ExcludedLabelIDs: append([]string(nil), excludedLabelIDs...),
		},
	}
}

func TestTaskListNamedAnyLabelFilterMatchesAnySelectedAssignment(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	response := fixture.list(t, namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		fixture.beta.String(),
		fixture.alpha.String(),
	))
	requireTaskListIDs(
		t,
		response,
		fixture.taskIDs["alpha"],
		fixture.taskIDs["beta"],
		fixture.taskIDs["both"],
	)
}

func TestTaskListNamedAllLabelFilterMatchesEverySelectedAssignment(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	response := fixture.list(t, namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAll,
		fixture.beta.String(),
		fixture.alpha.String(),
	))
	requireTaskListIDs(t, response, fixture.taskIDs["both"])
}

func TestTaskListNamedFilterCombinesIncludedAndExcludedConditions(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	delta, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.projectID, "delta")
	if err != nil {
		t.Fatalf("CreateProjectLabel delta: %v", err)
	}
	createTask := func(title string, labelIDs ...string) string {
		t.Helper()
		task, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
			ProjectID: fixture.projectID,
			Title:     title,
			LabelIDs:  labelIDs,
		})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return string(task.ID)
	}
	featureWithUnrelatedLabel := createTask("feature with unrelated label", fixture.gamma.String(), delta.ID.String())
	allConditions := createTask("all conditions", fixture.alpha.String(), fixture.beta.String(), fixture.gamma.String())

	for _, tt := range []struct {
		name   string
		filter serverapi.WorkflowTaskLabelFilter
		want   []string
	}{
		{
			name: "mixed OR",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				[]string{fixture.gamma.String()},
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["alpha"],
				fixture.taskIDs["beta"],
				fixture.taskIDs["gamma"],
				fixture.taskIDs["unlabeled"],
				featureWithUnrelatedLabel,
				allConditions,
			},
		},
		{
			name: "mixed AND",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAll,
				[]string{fixture.gamma.String()},
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["gamma"],
				featureWithUnrelatedLabel,
			},
		},
		{
			name: "excluded-only OR",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				nil,
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["alpha"],
				fixture.taskIDs["beta"],
				fixture.taskIDs["gamma"],
				fixture.taskIDs["unlabeled"],
				featureWithUnrelatedLabel,
			},
		},
		{
			name: "excluded-only AND",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAll,
				nil,
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["gamma"],
				fixture.taskIDs["unlabeled"],
				featureWithUnrelatedLabel,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireTaskListIDs(t, fixture.list(t, tt.filter), tt.want...)
		})
	}
}

func TestTaskListOneNamedLabelMatchesIdenticallyInAnyAndAllModes(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	list := func(mode serverapi.WorkflowTaskNamedLabelFilterMode) serverapi.WorkflowTaskListResponse {
		t.Helper()
		return fixture.list(t, namedTaskLabelFilter(mode, fixture.alpha.String()))
	}

	anyResponse := list(serverapi.WorkflowTaskNamedLabelFilterModeAny)
	allResponse := list(serverapi.WorkflowTaskNamedLabelFilterModeAll)
	requireTaskListIDs(t, anyResponse, fixture.taskIDs["alpha"], fixture.taskIDs["both"])
	requireTaskListIDs(t, allResponse, fixture.taskIDs["alpha"], fixture.taskIDs["both"])
}

func TestTaskListUnlabeledFilterMatchesOnlyTasksWithoutAssignments(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	response := fixture.list(t, serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindUnlabeled,
	})
	requireTaskListIDs(t, response, fixture.taskIDs["unlabeled"])
}

func TestTaskListNamedLabelFilterRejectsMissingLabel(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	missing := label.NewID()
	_, err := fixture.request(t, namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		missing.String(),
	))
	var labelErr *serverapi.WorkflowLabelError
	if !errors.As(err, &labelErr) ||
		labelErr.Reason != serverapi.WorkflowLabelErrorReasonLabelNotFound ||
		labelErr.LabelID == nil ||
		*labelErr.LabelID != missing.String() {
		t.Fatalf("missing label error = %+v", err)
	}
}

func TestTaskListNamedLabelFilterRejectsLabelFromAnotherProject(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	other, err := fixture.metadata.RegisterWorkspaceBinding(fixture.ctx, t.TempDir())
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding other project: %v", err)
	}
	foreign, err := fixture.store.CreateProjectLabel(fixture.ctx, other.ProjectID, "foreign")
	if err != nil {
		t.Fatalf("CreateProjectLabel foreign: %v", err)
	}

	_, err = fixture.request(t, namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		foreign.ID.String(),
	))
	var labelErr *serverapi.WorkflowLabelError
	if !errors.As(err, &labelErr) ||
		labelErr.Reason != serverapi.WorkflowLabelErrorReasonWrongProject ||
		labelErr.ProjectID == nil ||
		*labelErr.ProjectID != fixture.projectID ||
		labelErr.LabelID == nil ||
		*labelErr.LabelID != foreign.ID.String() {
		t.Fatalf("wrong-project label error = %+v", err)
	}
}

func TestTaskListPageTokenRejectsAnotherLabelExpression(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstPage, err := fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: &fixture.projectID,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilter(
			serverapi.WorkflowTaskNamedLabelFilterModeAny,
			fixture.beta.String(),
			fixture.alpha.String(),
		),
	})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page did not produce a continuation token")
	}

	_, err = fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		PageToken: *firstPage.NextPageToken,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilter(
			serverapi.WorkflowTaskNamedLabelFilterModeAny,
			fixture.gamma.String(),
		),
	})
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("changed label expression error = %v, want ErrInvalidPageToken", err)
	}
}

func TestTaskListPageTokenRejectsAnotherNamedLabelMode(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstPage, err := fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: &fixture.projectID,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilter(
			serverapi.WorkflowTaskNamedLabelFilterModeAny,
			fixture.alpha.String(),
			fixture.beta.String(),
		),
	})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page did not produce a continuation token")
	}

	_, err = fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		PageToken: *firstPage.NextPageToken,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilter(
			serverapi.WorkflowTaskNamedLabelFilterModeAll,
			fixture.alpha.String(),
			fixture.beta.String(),
		),
	})
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("changed named label mode error = %v, want ErrInvalidPageToken", err)
	}
}

func TestTaskListPageTokenRejectsChangedExcludedConditions(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstPage, err := fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: &fixture.projectID,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilterWithExclusions(
			serverapi.WorkflowTaskNamedLabelFilterModeAny,
			nil,
			[]string{fixture.alpha.String()},
		),
	})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page did not produce a continuation token")
	}

	_, err = fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		PageToken: *firstPage.NextPageToken,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilterWithExclusions(
			serverapi.WorkflowTaskNamedLabelFilterModeAny,
			nil,
			[]string{fixture.beta.String()},
		),
	})
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("changed excluded conditions error = %v, want ErrInvalidPageToken", err)
	}
}

func TestTaskListNamedLabelFilterKeepsCanonicalIdentityAcrossPages(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstPage, err := fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: &fixture.projectID,
		PageSize:  1,
		LabelFilter: namedTaskLabelFilter(
			serverapi.WorkflowTaskNamedLabelFilterModeAny,
			fixture.beta.String(),
			fixture.alpha.String(),
		),
	})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page did not produce a continuation token")
	}
	seen := map[string]bool{firstPage.Tasks[0].TaskID: true}
	pageToken := firstPage.NextPageToken
	for pageToken != nil {
		page, err := fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
			PageToken: *pageToken,
			PageSize:  1,
			LabelFilter: namedTaskLabelFilter(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				fixture.alpha.String(),
				fixture.beta.String(),
			),
		})
		if err != nil {
			t.Fatalf("List continuation with reordered IDs: %v", err)
		}
		if len(page.Tasks) != 1 {
			t.Fatalf("continuation tasks = %+v, want one matching task", page.Tasks)
		}
		taskID := page.Tasks[0].TaskID
		if seen[taskID] {
			t.Fatalf("continuation duplicated task %s", taskID)
		}
		seen[taskID] = true
		pageToken = page.NextPageToken
	}
	want := map[string]bool{
		fixture.taskIDs["alpha"]: true,
		fixture.taskIDs["beta"]:  true,
		fixture.taskIDs["both"]:  true,
	}
	if len(seen) != len(want) {
		t.Fatalf("paged task ids = %v, want %v", seen, want)
	}
	for taskID := range want {
		if !seen[taskID] {
			t.Fatalf("paged task ids = %v, missing %s", seen, taskID)
		}
	}
}

func TestTaskListLabelFilterComposesWithScopeStatusAttentionColumnAndSort(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	createWaitingQuestion := func(taskID workflow.TaskID) {
		t.Helper()
		started, err := fixture.store.StartTask(fixture.ctx, taskID)
		if err != nil {
			t.Fatalf("StartTask %s: %v", taskID, err)
		}
		claimed, err := fixture.store.ClaimRun(fixture.ctx, started.RunID, 0)
		if err != nil {
			t.Fatalf("ClaimRun %s: %v", taskID, err)
		}
		if err := fixture.store.SetRunWaitingAsk(
			fixture.ctx,
			started.RunID,
			claimed.Generation,
			"ask-"+string(taskID),
		); err != nil {
			t.Fatalf("SetRunWaitingAsk %s: %v", taskID, err)
		}
	}
	existingTaskID := workflow.TaskID(fixture.taskIDs["both"])
	createWaitingQuestion(existingTaskID)
	first, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID: fixture.projectID,
		Title:     "aardvark",
		LabelIDs:  []string{fixture.beta.String(), fixture.alpha.String()},
	})
	if err != nil {
		t.Fatalf("CreateTask aardvark: %v", err)
	}
	createWaitingQuestion(first.ID)

	workflowID := string(fixture.workflowID)
	response, err := fixture.view.tasks(t).List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &fixture.projectID,
		WorkflowID:  &workflowID,
		ColumnKeys:  []string{"agent"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{
			serverapi.WorkflowTaskAttentionKindQuestion,
		},
		Sort: []serverapi.WorkflowTaskListSort{{
			Field:     serverapi.WorkflowTaskListSortFieldTitle,
			Direction: serverapi.WorkflowTaskListSortDirectionAsc,
		}},
		LabelFilter: namedTaskLabelFilter(
			serverapi.WorkflowTaskNamedLabelFilterModeAll,
			fixture.alpha.String(),
			fixture.beta.String(),
		),
	})
	if err != nil {
		t.Fatalf("List composed filters: %v", err)
	}
	if len(response.Tasks) != 2 ||
		response.Tasks[0].TaskID != string(first.ID) ||
		response.Tasks[1].TaskID != string(existingTaskID) {
		t.Fatalf(
			"composed task order = %+v, want %s then %s",
			response.Tasks,
			first.ID,
			existingTaskID,
		)
	}
}
