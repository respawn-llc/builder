package sqlitegen

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"core/server/workflow/label"

	sqlitedriver "modernc.org/sqlite"
)

func init() {
	sqlitedriver.MustRegisterCollationUtf8(
		"kent_label_casefold_v1",
		func(left string, right string) int {
			return label.Compare(label.Name(left), label.Name(right))
		},
	)
}

func TestListTaskAssignedLabelIDsByTasksUsesBoundedTaskAssignmentIndex(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE project_labels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL
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
		listTaskAssignedLabelIDsByTasks,
		"sqlite_autoindex_task_label_assignments_1",
		"task-selected",
	)
	requireQueryUsesIndex(
		t,
		db,
		listTaskAssignedLabelIDsByTasks,
		"sqlite_autoindex_project_labels_1",
		"task-selected",
	)

	if _, err := db.Exec(`
INSERT INTO project_labels (id, name) VALUES
	('label-zulu', 'Zulu'),
	('label-alpha', 'alpha'),
	('label-unrelated', 'Unrelated');
INSERT INTO task_label_assignments (task_id, label_id) VALUES
	('task-selected', 'label-zulu'),
	('task-selected', 'label-alpha'),
	('task-unselected', 'label-unrelated');`); err != nil {
		t.Fatalf("seed query-plan fixture: %v", err)
	}
	rows, err := New(db).ListTaskAssignedLabelIDsByTasks(
		context.Background(),
		[]string{"task-selected", "task-selected-empty"},
	)
	if err != nil {
		t.Fatalf("ListTaskAssignedLabelIDsByTasks: %v", err)
	}
	want := []ListTaskAssignedLabelIDsByTasksRow{
		{TaskID: "task-selected", LabelID: "label-alpha"},
		{TaskID: "task-selected", LabelID: "label-zulu"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("selected task label rows = %+v, want %+v", rows, want)
	}
}
