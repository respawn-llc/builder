package sqlitegen

import "testing"

func TestListWorkflowTaskStatusProjectionByTasksUsesLiveQuestionAndApprovalPrecedence(t *testing.T) {
	db := openSQLiteFixture(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE workflow_task_status_records (
	task_id TEXT PRIMARY KEY,
	is_done INTEGER NOT NULL,
	kind TEXT NOT NULL,
	primary_status_rank INTEGER NOT NULL,
	node_ids_json TEXT NOT NULL,
	attention_types_json TEXT NOT NULL
);
INSERT INTO workflow_task_status_records
	(task_id, is_done, kind, primary_status_rank, node_ids_json, attention_types_json)
VALUES
	('done', 1, 'running', 5, '[]', '[]'),
	('question', 0, 'active', 8, '[]', '[]'),
	('approval-live', 0, 'active', 8, '[]', '[]'),
	('both-live', 0, 'active', 8, '[]', '[]'),
	('approval-durable', 0, 'waiting_approval', 3, '[]', '["approval"]'),
	('running', 0, 'active', 8, '[]', '[]'),
	('queued', 0, 'active', 8, '[]', '[]'),
	('stale-running', 0, 'running', 5, '[]', '[]'),
	('stale-queued', 0, 'queued', 6, '[]', '[]'),
	('stale-question', 0, 'waiting_question', 2, '[]', '["question"]')
`); err != nil {
		t.Fatalf("create status projection fixture: %v", err)
	}

	rows, err := New(db).ListWorkflowTaskStatusProjectionByTasks(t.Context(), ListWorkflowTaskStatusProjectionByTasksParams{
		TaskIdsJson: `["approval-durable","approval-live","both-live","done","queued","question","running","stale-question","stale-queued","stale-running"]`,
		LiveTaskStatesJson: `[
				{"task_id":"approval-durable","has_running":true},
				{"task_id":"approval-live","has_waiting_approval":true},
				{"task_id":"both-live","has_waiting_approval":true,"waiting_question":true},
				{"task_id":"done","has_waiting_approval":true,"waiting_question":true},
				{"task_id":"question","waiting_question":true},
				{"task_id":"queued","has_queued":true},
				{"task_id":"running","has_running":true}
			]`,
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskStatusProjectionByTasks: %v", err)
	}
	if len(rows) != 10 {
		t.Fatalf("status projection rows = %d, want 10: %+v", len(rows), rows)
	}
	byTask := make(map[string]ListWorkflowTaskStatusProjectionByTasksRow, len(rows))
	for _, row := range rows {
		byTask[row.TaskID] = row
	}
	for taskID, want := range map[string]struct {
		kind      string
		rank      int64
		attention string
	}{
		"done":             {kind: "done", rank: 1, attention: "[]"},
		"question":         {kind: "waiting_question", rank: 2, attention: `["question"]`},
		"approval-live":    {kind: "waiting_approval", rank: 3, attention: `["approval"]`},
		"both-live":        {kind: "waiting_question", rank: 2, attention: `["approval","question"]`},
		"approval-durable": {kind: "waiting_approval", rank: 3, attention: `["approval"]`},
		"running":          {kind: "running", rank: 5, attention: "[]"},
		"queued":           {kind: "queued", rank: 6, attention: "[]"},
		"stale-running":    {kind: "active", rank: 8, attention: "[]"},
		"stale-queued":     {kind: "active", rank: 8, attention: "[]"},
		"stale-question":   {kind: "active", rank: 8, attention: `[]`},
	} {
		row, ok := byTask[taskID]
		if !ok {
			t.Fatalf("status projection omitted task %q", taskID)
		}
		if row.Kind != want.kind || row.PrimaryStatusRank != want.rank || row.AttentionTypesJson != want.attention {
			t.Errorf("status projection %q = kind=%q rank=%d attention=%q, want kind=%q rank=%d attention=%q",
				taskID, row.Kind, row.PrimaryStatusRank, row.AttentionTypesJson, want.kind, want.rank, want.attention)
		}
	}
}
