package workflowview

import (
	"context"
	"database/sql"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestAttentionListUsesOneDatabaseReadForInterruptedRunPage(t *testing.T) {
	tests := []struct {
		name             string
		workflowCount    int
		tasksPerWorkflow int
	}{
		{name: "four tasks under one workflow", workflowCount: 1, tasksPerWorkflow: 4},
		{name: "one task under each of four workflows", workflowCount: 4, tasksPerWorkflow: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
			for workflowIndex := 0; workflowIndex < tt.workflowCount; workflowIndex++ {
				workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
				if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, workflowIndex == 0); err != nil {
					t.Fatalf("LinkWorkflow %d: %v", workflowIndex, err)
				}
				for taskIndex := 0; taskIndex < tt.tasksPerWorkflow; taskIndex++ {
					createActionableInterruptedAttentionTask(t, ctx, workflowStore, binding.ProjectID, workflowID)
				}
			}

			counter := &attentionCountingDBTX{db: metadataStore.DB()}
			attention, err := NewAttention(sqlitegen.New(counter), NewTaskProjector(), nil, nil)
			if err != nil {
				t.Fatalf("NewAttention: %v", err)
			}
			counter.reset()

			response, err := attention.List(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 4})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(response.Items) != 4 {
				t.Fatalf("attention items = %+v, want four interrupted-run items", response.Items)
			}
			for _, item := range response.Items {
				if item.Kind != attentionKindInterruptedRun {
					t.Fatalf("attention item = %+v, want interrupted-run", item)
				}
			}
			if counter.queryContextCalls != 1 ||
				counter.queryRowContextCalls != 0 ||
				counter.prepareContextCalls != 0 ||
				counter.readCalls() != 1 {
				t.Fatalf(
					"attention database executions = query=%d query_row=%d prepare=%d total=%d, want query=1 query_row=0 prepare=0 total=1",
					counter.queryContextCalls,
					counter.queryRowContextCalls,
					counter.prepareContextCalls,
					counter.readCalls(),
				)
			}
		})
	}
}

func createActionableInterruptedAttentionTask(
	t *testing.T,
	ctx context.Context,
	store *workflowstore.Store,
	projectID string,
	workflowID workflow.WorkflowID,
) {
	t.Helper()
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		Title:      "Interrupted",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
}

type attentionCountingDBTX struct {
	db                   *sql.DB
	queryContextCalls    int
	queryRowContextCalls int
	prepareContextCalls  int
}

func (d *attentionCountingDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return d.db.ExecContext(ctx, query, args...)
}

func (d *attentionCountingDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	d.prepareContextCalls++
	return d.db.PrepareContext(ctx, query)
}

func (d *attentionCountingDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	d.queryContextCalls++
	return d.db.QueryContext(ctx, query, args...)
}

func (d *attentionCountingDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	d.queryRowContextCalls++
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d *attentionCountingDBTX) reset() {
	d.queryContextCalls = 0
	d.queryRowContextCalls = 0
	d.prepareContextCalls = 0
}

func (d *attentionCountingDBTX) readCalls() int {
	return d.queryContextCalls + d.queryRowContextCalls + d.prepareContextCalls
}
