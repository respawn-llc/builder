package sqlitegen

import (
	"database/sql"
	"testing"
)

func TestLifecycleTaskStateFunctionsResolveOneTaskWithoutMaterializingTheRoot(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	calls := 0
	token, release, err := RegisterLifecycleTaskStateResolver(func(taskID string) (LifecycleTaskQueryState, error) {
		calls++
		if taskID != "task-live" {
			return LifecycleTaskQueryState{}, nil
		}
		return LifecycleTaskQueryState{
			Flags: LifecycleTaskStateOwned |
				LifecycleTaskStateRunning |
				LifecycleTaskStateWaitingQuestion,
			CurrentNodeIDs: []string{"node-a", "node-b"},
		}, nil
	})
	if err != nil {
		t.Fatalf("RegisterLifecycleTaskStateResolver: %v", err)
	}
	t.Cleanup(release)

	var flags int64
	var nodeIDs string
	if err := db.QueryRow(
		`SELECT kent_lifecycle_task_state_v1(?, ?), kent_lifecycle_current_node_ids_v1(?, ?)`,
		token,
		"task-live",
		token,
		"task-live",
	).Scan(&flags, &nodeIDs); err != nil {
		t.Fatalf("query lifecycle Task state: %v", err)
	}
	wantFlags := LifecycleTaskStateOwned | LifecycleTaskStateRunning | LifecycleTaskStateWaitingQuestion
	if flags != wantFlags || nodeIDs != `["node-a","node-b"]` {
		t.Fatalf("lifecycle Task state = flags:%d nodes:%s, want flags:%d nodes:[node-a,node-b]", flags, nodeIDs, wantFlags)
	}
	if calls != 2 {
		t.Fatalf("lifecycle resolver calls = %d, want one exact lookup per scalar projection", calls)
	}

	var absentFlags int64
	var absentNodeIDs sql.NullString
	if err := db.QueryRow(
		`SELECT kent_lifecycle_task_state_v1(?, ?), kent_lifecycle_current_node_ids_v1(?, ?)`,
		token,
		"task-durable",
		token,
		"task-durable",
	).Scan(&absentFlags, &absentNodeIDs); err != nil {
		t.Fatalf("query absent lifecycle Task state: %v", err)
	}
	if absentFlags != 0 || absentNodeIDs.Valid {
		t.Fatalf("absent lifecycle Task state = flags:%d nodes:%+v, want no overlay", absentFlags, absentNodeIDs)
	}

	release()
	if err := db.QueryRow(
		`SELECT kent_lifecycle_task_state_v1(?, ?)`,
		token,
		"task-live",
	).Scan(&flags); err == nil {
		t.Fatal("released lifecycle resolver remained queryable")
	}
}
