package requestmemo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoDoesNotReplayCanceledOrDeadlineExceededOutcome(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{
			name:    "canceled",
			wantErr: context.Canceled,
		},
		{
			name:    "deadline exceeded",
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memo := New[string, string]()
			calls := 0

			first, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
				return a == b
			}, func(context.Context) (string, error) {
				calls++
				return "", tt.wantErr
			})
			if err != tt.wantErr {
				t.Fatalf("first error = %v, want %v", err, tt.wantErr)
			}
			if first != "" {
				t.Fatalf("first response = %q, want empty", first)
			}

			second, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
				return a == b
			}, func(context.Context) (string, error) {
				calls++
				return "ok", nil
			})
			if err != nil {
				t.Fatalf("second error = %v", err)
			}
			if second != "ok" {
				t.Fatalf("second response = %q, want ok", second)
			}
			if calls != 2 {
				t.Fatalf("run calls = %d, want 2", calls)
			}
		})
	}
}

func TestMemoDoesNotReplayGenericErrorOutcome(t *testing.T) {
	memo := New[string, string]()
	calls := 0

	first, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		calls++
		return "", errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("first error = %v, want boom", err)
	}
	if first != "" {
		t.Fatalf("first response = %q, want empty", first)
	}

	second, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("second error = %v", err)
	}
	if second != "ok" {
		t.Fatalf("second response = %q, want ok", second)
	}
	if calls != 2 {
		t.Fatalf("run calls = %d, want 2", calls)
	}
}

func TestMemoRerunsWaitingRetryAfterNonMemoizedInflightCompletion(t *testing.T) {
	memo := New[string, string]()
	started := make(chan struct{})
	allowCancel := make(chan struct{})
	firstDone := make(chan error, 1)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	go func() {
		_, err := memo.Do(firstCtx, "req-1", "same", func(a string, b string) bool {
			return a == b
		}, func(ctx context.Context) (string, error) {
			close(started)
			<-allowCancel
			return "", ctx.Err()
		})
		firstDone <- err
	}()

	<-started
	secondDone := make(chan struct {
		resp string
		err  error
	}, 1)
	go func() {
		resp, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
			return a == b
		}, func(context.Context) (string, error) {
			return "ok", nil
		})
		secondDone <- struct {
			resp string
			err  error
		}{resp: resp, err: err}
	}()

	close(allowCancel)
	cancelFirst()
	if err := <-firstDone; err != context.Canceled {
		t.Fatalf("first error = %v, want canceled", err)
	}
	second := <-secondDone
	if second.err != nil {
		t.Fatalf("second error = %v", second.err)
	}
	if second.resp != "ok" {
		t.Fatalf("second response = %q, want ok", second.resp)
	}
}

func TestMemoPrunesExpiredEntriesBelowCapacity(t *testing.T) {
	memo := New[string, string]()
	base := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	now := base
	memo.now = func() time.Time { return now }
	memo.ttl = time.Minute
	memo.maxEntries = 16

	firstCalls := 0
	first, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		firstCalls++
		return "first", nil
	})
	if err != nil {
		t.Fatalf("first call error = %v", err)
	}
	if first != "first" {
		t.Fatalf("first response = %q, want first", first)
	}

	now = now.Add(2 * time.Minute)
	if _, err := memo.Do(context.Background(), "req-2", "other", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		return "second", nil
	}); err != nil {
		t.Fatalf("second request error = %v", err)
	}

	replayed, err := memo.Do(context.Background(), "req-1", "same", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		firstCalls++
		return "fresh", nil
	})
	if err != nil {
		t.Fatalf("replay error = %v", err)
	}
	if replayed != "fresh" {
		t.Fatalf("replayed response = %q, want fresh", replayed)
	}
	if firstCalls != 2 {
		t.Fatalf("req-1 run calls = %d, want 2", firstCalls)
	}
}

func TestMemoDoesNotGrowPastCapacityWhenOnlyInflightEntriesExist(t *testing.T) {
	memo := New[string, string]()
	base := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	memo.now = func() time.Time { return base }
	memo.maxEntries = 2
	memo.entries["req-1"] = &entry[string, string]{req: "a", done: make(chan struct{}), createdAt: base}
	memo.entries["req-2"] = &entry[string, string]{req: "b", done: make(chan struct{}), createdAt: base.Add(time.Second)}

	runCalls := 0
	resp, err := memo.Do(context.Background(), "req-3", "c", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		runCalls++
		return "fresh", nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp != "fresh" {
		t.Fatalf("response = %q, want fresh", resp)
	}
	if runCalls != 1 {
		t.Fatalf("run calls = %d, want 1", runCalls)
	}
	if got := len(memo.entries); got != 2 {
		t.Fatalf("memo size = %d, want 2", got)
	}
	if _, ok := memo.entries["req-3"]; ok {
		t.Fatalf("did not expect req-3 to be memoized at capacity, entries=%+v", memo.entries)
	}

	resp, err = memo.Do(context.Background(), "req-3", "c", func(a string, b string) bool {
		return a == b
	}, func(context.Context) (string, error) {
		runCalls++
		return "fresh-again", nil
	})
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if resp != "fresh-again" {
		t.Fatalf("second response = %q, want fresh-again", resp)
	}
	if runCalls != 2 {
		t.Fatalf("run calls after second request = %d, want 2", runCalls)
	}
}
