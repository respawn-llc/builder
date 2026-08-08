package sqlitegen

import (
	"testing"
)

func TestLifecycleTaskStateFunctionResolvesOneTaskWithoutMaterializingTheRoot(t *testing.T) {
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
		}, nil
	})
	if err != nil {
		t.Fatalf("RegisterLifecycleTaskStateResolver: %v", err)
	}
	t.Cleanup(release)

	var flags int64
	if err := db.QueryRow(
		`SELECT kent_lifecycle_task_state_v1(?, ?)`,
		token,
		"task-live",
	).Scan(&flags); err != nil {
		t.Fatalf("query lifecycle Task state: %v", err)
	}
	wantFlags := LifecycleTaskStateOwned | LifecycleTaskStateRunning | LifecycleTaskStateWaitingQuestion
	if flags != wantFlags {
		t.Fatalf("lifecycle Task state flags = %d, want %d", flags, wantFlags)
	}
	if calls != 1 {
		t.Fatalf("lifecycle resolver calls = %d, want one exact lookup", calls)
	}

	var absentFlags int64
	if err := db.QueryRow(
		`SELECT kent_lifecycle_task_state_v1(?, ?)`,
		token,
		"task-durable",
	).Scan(&absentFlags); err != nil {
		t.Fatalf("query absent lifecycle Task state: %v", err)
	}
	if absentFlags != 0 {
		t.Fatalf("absent lifecycle Task state flags = %d, want no overlay", absentFlags)
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
