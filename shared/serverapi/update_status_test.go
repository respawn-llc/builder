package serverapi_test

import (
	"testing"

	. "core/shared/serverapi"
)

func TestUpdateStatusResultJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		want UpdateStatusResult
	}{
		{
			name: "current",
			want: CurrentUpdateStatusResult("1.2.3", "1.2.3"),
		},
		{
			name: "available",
			want: AvailableUpdateStatusResult("1.2.3", "1.3.0"),
		},
		{
			name: "check unavailable",
			want: CheckUnavailableUpdateStatusResult(),
		},
		{
			name: "check failed",
			want: FailedUpdateStatusResult("403 Forbidden"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.want.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			assertUpdateStatusResultAccessors(t, test.want)
		})
	}
}

func assertUpdateStatusResultAccessors(t *testing.T, result UpdateStatusResult) {
	t.Helper()
	switch result.Kind() {
	case UpdateStatusCurrent, UpdateStatusAvailable:
		versions := result.Versions()
		if versions == nil || versions.Current == "" || versions.Latest == "" {
			t.Fatal("version result did not expose structurally present versions")
		}
		if result.Failure() != nil {
			t.Fatal("version result exposed a failure cause")
		}
	case UpdateStatusCheckUnavailable:
		if result.Versions() != nil {
			t.Fatal("check-unavailable result exposed versions")
		}
		if result.Failure() != nil {
			t.Fatal("check-unavailable result exposed a failure cause")
		}
	case UpdateStatusCheckFailed:
		if result.Versions() != nil {
			t.Fatal("check-failed result exposed versions")
		}
		failure := result.Failure()
		if failure == nil || failure.Cause == "" {
			t.Fatal("check-failed result did not expose its structurally present cause")
		}
	default:
		t.Fatalf("unexpected kind %q", result.Kind())
	}
}
