package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectActivityCoordinatorClosesAdmissionBeforeDrainingActivePermits(t *testing.T) {
	var coordinator projectActivityCoordinator
	release, err := coordinator.AcquireProjectActivity("project-a")
	if err != nil {
		t.Fatalf("AcquireActive: %v", err)
	}
	coordinator.CloseAdmission()
	if _, err := coordinator.AcquireProjectActivity("project-b"); !errors.Is(err, ErrProjectActivityAdmissionClosed) {
		t.Fatalf("AcquireProjectActivity after CloseAdmission error = %v, want %v", err, ErrProjectActivityAdmissionClosed)
	}

	drained := make(chan error, 1)
	go func() {
		drained <- coordinator.Drain(context.Background())
	}()
	select {
	case err := <-drained:
		t.Fatalf("Drain completed before active permit release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not complete after active permit release")
	}
}

func TestCoreCloseClosesProjectAdmissionBeforeSchedulerAndDrainsPermits(t *testing.T) {
	var coordinator projectActivityCoordinator
	release, err := coordinator.AcquireProjectActivity("project-a")
	if err != nil {
		t.Fatalf("AcquireActive: %v", err)
	}
	var calls []string
	appCore := &Core{
		activity: &coordinator,
		bundles: &Bundles{
			cleanup: []lifecycleResource{
				{name: "root lock", close: func() error {
					calls = append(calls, "root lock")
					return nil
				}},
				{name: "project activity drain", close: func() error {
					calls = append(calls, "project activity drain")
					return coordinator.Drain(context.Background())
				}},
				{name: "workflow scheduler", close: func() error {
					calls = append(calls, "workflow scheduler")
					return nil
				}},
				{name: "project activity admission", close: func() error {
					calls = append(calls, "project activity admission")
					coordinator.CloseAdmission()
					return nil
				}},
			},
		},
	}
	closed := make(chan error, 1)
	go func() {
		closed <- appCore.Close()
	}()
	select {
	case err := <-closed:
		t.Fatalf("Close completed before active permit release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := coordinator.AcquireProjectActivity("project-b"); !errors.Is(err, ErrProjectActivityAdmissionClosed) {
		t.Fatalf("AcquireProjectActivity during shutdown error = %v, want %v", err, ErrProjectActivityAdmissionClosed)
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not complete after active permit release")
	}
	if want := []string{"project activity admission", "workflow scheduler", "project activity drain", "root lock"}; !sameStrings(calls, want) {
		t.Fatalf("close calls = %v, want %v", calls, want)
	}
}

func TestProjectActivityCoordinatorBeginDeleteClosesOnlyProjectAdmissionAndDrainsActivePermits(t *testing.T) {
	var coordinator projectActivityCoordinator
	release, err := coordinator.AcquireProjectActivity("project-a")
	if err != nil {
		t.Fatalf("AcquireProjectActivity: %v", err)
	}

	tokenResult := make(chan struct {
		token *projectDeleteToken
		err   error
	}, 1)
	go func() {
		token, beginErr := coordinator.BeginDelete(context.Background(), "project-a")
		tokenResult <- struct {
			token *projectDeleteToken
			err   error
		}{token: token, err: beginErr}
	}()

	select {
	case result := <-tokenResult:
		t.Fatalf("BeginDelete completed before active permit release: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := coordinator.AcquireProjectActivity("project-a"); !errors.Is(err, ErrProjectActivityProjectAdmissionClosed) {
		t.Fatalf("AcquireProjectActivity deleting project error = %v, want %v", err, ErrProjectActivityProjectAdmissionClosed)
	}
	otherRelease, err := coordinator.AcquireProjectActivity("project-b")
	if err != nil {
		t.Fatalf("AcquireProjectActivity other project: %v", err)
	}
	otherRelease()

	release()
	var token *projectDeleteToken
	select {
	case result := <-tokenResult:
		if result.err != nil {
			t.Fatalf("BeginDelete: %v", result.err)
		}
		token = result.token
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDelete did not complete after active permit release")
	}
	token.Reopen()
	reopened, err := coordinator.AcquireProjectActivity("project-a")
	if err != nil {
		t.Fatalf("AcquireProjectActivity after delete reopen: %v", err)
	}
	reopened()
}

func TestProjectActivityCoordinatorCanceledDeleteReopensProjectAdmission(t *testing.T) {
	var coordinator projectActivityCoordinator
	release, err := coordinator.AcquireProjectActivity("project-a")
	if err != nil {
		t.Fatalf("AcquireProjectActivity: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	deleteResult := make(chan error, 1)
	go func() {
		_, beginErr := coordinator.BeginDelete(ctx, "project-a")
		deleteResult <- beginErr
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, admissionErr := coordinator.AcquireProjectActivity("project-a")
		if errors.Is(admissionErr, ErrProjectActivityProjectAdmissionClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("BeginDelete did not close project admission, last error = %v", admissionErr)
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case beginErr := <-deleteResult:
		if !errors.Is(beginErr, context.Canceled) {
			t.Fatalf("BeginDelete error = %v, want %v", beginErr, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BeginDelete did not return after cancellation")
	}

	reopened, err := coordinator.AcquireProjectActivity("project-a")
	if err != nil {
		t.Fatalf("AcquireProjectActivity after canceled delete: %v", err)
	}
	reopened()
	release()
}

func TestProjectActivityCoordinatorDrainTracksCreateDeleteAndCleanupTokens(t *testing.T) {
	tests := []struct {
		name    string
		acquire func(*projectActivityCoordinator) (func(), error)
	}{
		{
			name: "create reservation",
			acquire: func(coordinator *projectActivityCoordinator) (func(), error) {
				reservation, err := coordinator.ReserveCreate("project-a")
				if err != nil {
					return nil, err
				}
				return reservation.Release, nil
			},
		},
		{
			name: "delete token",
			acquire: func(coordinator *projectActivityCoordinator) (func(), error) {
				token, err := coordinator.BeginDelete(context.Background(), "project-a")
				if err != nil {
					return nil, err
				}
				return token.Reopen, nil
			},
		},
		{
			name: "cleanup token",
			acquire: func(coordinator *projectActivityCoordinator) (func(), error) {
				deleteToken, err := coordinator.BeginDelete(context.Background(), "project-a")
				if err != nil {
					return nil, err
				}
				cleanupToken, err := deleteToken.PromoteToCleanup(1)
				if err != nil {
					return nil, err
				}
				return cleanupToken.Release, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var coordinator projectActivityCoordinator
			release, err := test.acquire(&coordinator)
			if err != nil {
				t.Fatalf("acquire token: %v", err)
			}
			coordinator.CloseAdmission()

			drained := make(chan error, 1)
			go func() {
				drained <- coordinator.Drain(context.Background())
			}()
			select {
			case err := <-drained:
				t.Fatalf("Drain completed before token release: %v", err)
			case <-time.After(50 * time.Millisecond):
			}

			release()
			select {
			case err := <-drained:
				if err != nil {
					t.Fatalf("Drain: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Drain did not complete after token release")
			}
		})
	}
}

func TestProjectActivityCoordinatorCleanupTokenKeepsAdmissionClosedAndSerializesRetry(t *testing.T) {
	var coordinator projectActivityCoordinator
	token, err := coordinator.BeginDelete(context.Background(), "project-a")
	if err != nil {
		t.Fatalf("BeginDelete: %v", err)
	}
	cleanup, err := token.PromoteToCleanup(7)
	if err != nil {
		t.Fatalf("PromoteToCleanup: %v", err)
	}
	if _, err := coordinator.AcquireProjectActivity("project-a"); !errors.Is(err, ErrProjectActivityProjectAdmissionClosed) {
		t.Fatalf("AcquireProjectActivity during cleanup error = %v, want %v", err, ErrProjectActivityProjectAdmissionClosed)
	}
	if _, err := coordinator.AcquireCleanupRetry(context.Background(), "project-a", 8); !errors.Is(err, ErrProjectActivityLifecycleGenerationMismatch) {
		t.Fatalf("AcquireCleanupRetry mismatched generation error = %v, want %v", err, ErrProjectActivityLifecycleGenerationMismatch)
	}

	retryResult := make(chan struct {
		token *projectCleanupToken
		err   error
	}, 1)
	go func() {
		retry, retryErr := coordinator.AcquireCleanupRetry(context.Background(), "project-a", 7)
		retryResult <- struct {
			token *projectCleanupToken
			err   error
		}{token: retry, err: retryErr}
	}()
	select {
	case result := <-retryResult:
		t.Fatalf("AcquireCleanupRetry completed while cleanup token was active: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	cleanup.Release()
	select {
	case result := <-retryResult:
		if result.err != nil {
			t.Fatalf("AcquireCleanupRetry: %v", result.err)
		}
		result.token.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("AcquireCleanupRetry did not complete after cleanup release")
	}
}

func TestProjectActivityCoordinatorCreateReservationUpgradesToActivePermit(t *testing.T) {
	var coordinator projectActivityCoordinator
	reservation, err := coordinator.ReserveCreate("project-a")
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	if _, err := coordinator.BeginDelete(context.Background(), "project-a"); !errors.Is(err, ErrProjectActivityCreateInProgress) {
		t.Fatalf("BeginDelete while create reserved error = %v, want %v", err, ErrProjectActivityCreateInProgress)
	}
	release, err := reservation.Activate()
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	release()
	if _, err := coordinator.BeginDelete(context.Background(), "project-a"); err != nil {
		t.Fatalf("BeginDelete after active permit release: %v", err)
	}
}

func sameStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
