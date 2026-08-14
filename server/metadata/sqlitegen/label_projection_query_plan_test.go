package sqlitegen

import (
	"context"
	"reflect"
	"testing"
)

func TestListTaskAssignedLabelsByTasksUsesBoundedTaskAssignmentIndex(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE project_labels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	ordinal INTEGER NOT NULL
);
CREATE TABLE task_label_assignments (
	task_id TEXT NOT NULL,
	label_id TEXT NOT NULL,
	PRIMARY KEY (task_id, label_id)
);`); err != nil {
		t.Fatalf("create query-plan fixture: %v", err)
	}

	requireQueryUsesIndex(
		t,
		db,
		listTaskAssignedLabelsByTasks,
		"sqlite_autoindex_task_label_assignments_1",
		"task-selected",
	)
	requireQueryUsesIndex(
		t,
		db,
		listTaskAssignedLabelsByTasks,
		"sqlite_autoindex_project_labels_1",
		"task-selected",
	)

	if _, err := db.Exec(`
INSERT INTO project_labels (id, name, ordinal) VALUES
	('label-zulu', 'Zulu', 1),
	('label-alpha', 'alpha', 2),
	('label-unrelated', 'Unrelated', 1);
INSERT INTO task_label_assignments (task_id, label_id) VALUES
	('task-selected', 'label-zulu'),
	('task-selected', 'label-alpha'),
	('task-unselected', 'label-unrelated');`); err != nil {
		t.Fatalf("seed query-plan fixture: %v", err)
	}
	rows, err := New(db).ListTaskAssignedLabelsByTasks(
		context.Background(),
		[]string{"task-selected", "task-selected-empty"},
	)
	if err != nil {
		t.Fatalf("ListTaskAssignedLabelsByTasks: %v", err)
	}
	want := []ListTaskAssignedLabelsByTasksRow{
		{TaskID: "task-selected", LabelID: "label-zulu", LabelName: "Zulu"},
		{TaskID: "task-selected", LabelID: "label-alpha", LabelName: "alpha"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("selected task label rows = %+v, want %+v", rows, want)
	}
}
