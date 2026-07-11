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
