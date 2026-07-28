package projectview

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

//go:embed testdata/home_sort_task_comment.sql
var homeSortTaskCommentSQL string

func TestMetadataServiceSortsProjectHomeByLatestTaskActivityOrEdit(t *testing.T) {
	ctx := context.Background()
	store, cfg, older := newProjectViewMetadataStore(t)
	svc, err := NewMetadataService(store, "")
	if err != nil {
		t.Fatalf("NewMetadataService: %v", err)
	}
	newer, err := svc.CreateProject(ctx, serverapi.ProjectCreateRequest{
		DisplayName:   "Newer edit",
		ProjectKey:    "NEW",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	taskActivityUnixMs := time.Now().UTC().UnixMilli() + 10_000
	workflowStore, err := workflowstore.New(store, workflowstore.WithNow(func() time.Time {
		return time.UnixMilli(taskActivityUnixMs)
	}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflow, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Activity Board"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, older.ProjectID, workflow.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: older.ProjectID, Title: "Recent task", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	home, err := svc.ListProjectHome(ctx, serverapi.ProjectHomeListRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListProjectHome: %v", err)
	}
	if got := projectHomeIDs(home.Projects); len(got) != 2 || got[0] != older.ProjectID || got[1] != newer.Binding.ProjectID {
		t.Fatalf("project order after task activity = %+v, want [%s %s]", got, older.ProjectID, newer.Binding.ProjectID)
	}

	if _, err := store.DB().ExecContext(ctx, `UPDATE projects SET updated_at_unix_ms = ? WHERE id = ?`, taskActivityUnixMs+1, newer.Binding.ProjectID); err != nil {
		t.Fatalf("touch newer project edit time: %v", err)
	}
	home, err = svc.ListProjectHome(ctx, serverapi.ProjectHomeListRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListProjectHome after edit: %v", err)
	}
	if got := projectHomeIDs(home.Projects); len(got) != 2 || got[0] != newer.Binding.ProjectID || got[1] != older.ProjectID {
		t.Fatalf("project order after edit = %+v, want [%s %s]", got, newer.Binding.ProjectID, older.ProjectID)
	}

	projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), older.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.SetName("Recent chat"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	sessionActivityUnixMs := taskActivityUnixMs + 2
	if _, err := store.DB().ExecContext(ctx, `UPDATE sessions SET updated_at_unix_ms = ? WHERE id = ?`, sessionActivityUnixMs, sess.Meta().SessionID); err != nil {
		t.Fatalf("touch session activity: %v", err)
	}
	home, err = svc.ListProjectHome(ctx, serverapi.ProjectHomeListRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListProjectHome after session activity: %v", err)
	}
	if got := projectHomeIDs(home.Projects); len(got) != 2 || got[0] != older.ProjectID || got[1] != newer.Binding.ProjectID {
		t.Fatalf("project order after session activity = %+v, want [%s %s]", got, older.ProjectID, newer.Binding.ProjectID)
	}
	if got := home.Projects[0].UpdatedAtUnixMs; got != sessionActivityUnixMs {
		t.Fatalf("latest session activity timestamp = %d, want %d", got, sessionActivityUnixMs)
	}
}

func TestMetadataServiceSortsProjectHomeByTaskChildActivitySources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		touch func(t *testing.T, ctx context.Context, fixture projectHomeActivityFixture)
	}{
		{
			name: "task_comments",
			touch: func(t *testing.T, ctx context.Context, fixture projectHomeActivityFixture) {
				t.Helper()
				if _, err := fixture.store.DB().ExecContext(ctx, homeSortTaskCommentSQL,
					"comment-home-sort", string(fixture.task.ID), fixture.highUnixMs, fixture.highUnixMs,
				); err != nil {
					t.Fatalf("insert comment activity: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newProjectHomeActivityFixture(t, ctx)
			assertProjectHomeOrder(t, ctx, fixture.svc, []string{fixture.newer.Binding.ProjectID, fixture.older.ProjectID})

			tc.touch(t, ctx, fixture)

			home := assertProjectHomeOrder(t, ctx, fixture.svc, []string{fixture.older.ProjectID, fixture.newer.Binding.ProjectID})
			if home.Projects[0].UpdatedAtUnixMs != fixture.highUnixMs {
				t.Fatalf("latest activity timestamp = %d, want %d", home.Projects[0].UpdatedAtUnixMs, fixture.highUnixMs)
			}
		})
	}
}

func TestProjectHomeOrderingAdvancesOnCurrentNodeMutation(t *testing.T) {
	ctx := context.Background()
	fixture := newProjectHomeActivityFixture(t, ctx)
	assertProjectHomeOrder(t, ctx, fixture.svc, []string{fixture.newer.Binding.ProjectID, fixture.older.ProjectID})

	definition, _, err := fixture.workflowStore.GetDefinition(ctx, fixture.task.WorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var terminalNodeID string
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindTerminal {
			terminalNodeID = string(workflow.NodeIDOf(node))
			break
		}
	}
	if terminalNodeID == "" {
		t.Fatal("terminal Node is missing")
	}
	fixture.setNow(fixture.highUnixMs)
	if _, err := fixture.workflowStore.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: workflow.NodeID(terminalNodeID),
	}); err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}

	home := assertProjectHomeOrder(t, ctx, fixture.svc, []string{fixture.older.ProjectID, fixture.newer.Binding.ProjectID})
	if home.Projects[0].UpdatedAtUnixMs != fixture.highUnixMs {
		t.Fatalf("Current Node mutation activity timestamp = %d, want %d", home.Projects[0].UpdatedAtUnixMs, fixture.highUnixMs)
	}
}

func BenchmarkMetadataServiceListProjectHomeSummaries(b *testing.B) {
	ctx := context.Background()
	store, _, first := newProjectViewMetadataStore(b)
	svc, err := NewMetadataService(store, "")
	if err != nil {
		b.Fatalf("NewMetadataService: %v", err)
	}
	workflowStore, err := workflowstore.New(store, workflowstore.WithNow(func() time.Time {
		return time.UnixMilli(1)
	}))
	if err != nil {
		b.Fatalf("workflowstore.New: %v", err)
	}
	workflow, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Profile Board"})
	if err != nil {
		b.Fatalf("CreateWorkflow: %v", err)
	}
	projectIDs := []string{first.ProjectID}
	for index := 1; index < 250; index++ {
		created, err := svc.CreateProject(ctx, serverapi.ProjectCreateRequest{
			DisplayName:   fmt.Sprintf("Project %03d", index),
			ProjectKey:    fmt.Sprintf("P%03d", index),
			WorkspaceRoot: b.TempDir(),
		})
		if err != nil {
			b.Fatalf("CreateProject %d: %v", index, err)
		}
		projectIDs = append(projectIDs, created.Binding.ProjectID)
	}
	for projectIndex, projectID := range projectIDs {
		if _, err := workflowStore.LinkWorkflow(ctx, projectID, workflow.ID, true); err != nil {
			b.Fatalf("LinkWorkflow %d: %v", projectIndex, err)
		}
		for taskIndex := 0; taskIndex < 3; taskIndex++ {
			task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: projectID, Title: fmt.Sprintf("Task %d", taskIndex), Body: "Body"})
			if err != nil {
				b.Fatalf("CreateTask %d/%d: %v", projectIndex, taskIndex, err)
			}
			if _, err := workflowStore.AddComment(ctx, task.ID, "Comment", "user", "bench"); err != nil {
				b.Fatalf("AddComment %d/%d: %v", projectIndex, taskIndex, err)
			}
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := svc.ListProjectHome(ctx, serverapi.ProjectHomeListRequest{PageSize: 40}); err != nil {
			b.Fatalf("ListProjectHome: %v", err)
		}
	}
}

type projectHomeActivityFixture struct {
	store         *metadata.Store
	svc           *Service
	older         metadata.Binding
	newer         serverapi.ProjectCreateResponse
	task          workflowstore.TaskRecord
	workflowStore *workflowstore.Store
	setNow        func(int64)
	highUnixMs    int64
}

func newProjectHomeActivityFixture(t *testing.T, ctx context.Context) projectHomeActivityFixture {
	t.Helper()
	store, _, older := newProjectViewMetadataStore(t)
	svc, err := NewMetadataService(store, "")
	if err != nil {
		t.Fatalf("NewMetadataService: %v", err)
	}
	newer, err := svc.CreateProject(ctx, serverapi.ProjectCreateRequest{
		DisplayName:   "Newer project",
		ProjectKey:    "NEW",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	nowUnixMs := int64(1)
	workflowStore, err := workflowstore.New(store, workflowstore.WithNow(func() time.Time {
		return time.UnixMilli(nowUnixMs)
	}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflow, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Activity Board"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, older.ProjectID, workflow.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: older.ProjectID, Title: "Low task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return projectHomeActivityFixture{
		store:         store,
		svc:           svc,
		older:         older,
		newer:         newer,
		task:          task,
		workflowStore: workflowStore,
		setNow:        func(value int64) { nowUnixMs = value },
		highUnixMs:    time.Now().UTC().UnixMilli() + 10_000,
	}
}

func assertProjectHomeOrder(t testing.TB, ctx context.Context, svc *Service, want []string) serverapi.ProjectHomeListResponse {
	t.Helper()
	home, err := svc.ListProjectHome(ctx, serverapi.ProjectHomeListRequest{PageSize: len(want)})
	if err != nil {
		t.Fatalf("ListProjectHome: %v", err)
	}
	got := projectHomeIDs(home.Projects)
	if len(got) != len(want) {
		t.Fatalf("project count = %d, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("project order = %+v, want %+v", got, want)
		}
	}
	return home
}

func projectHomeIDs(projects []serverapi.ProjectHomeSummary) []string {
	out := make([]string, 0, len(projects))
	for _, project := range projects {
		out = append(out, project.ProjectID)
	}
	return out
}
