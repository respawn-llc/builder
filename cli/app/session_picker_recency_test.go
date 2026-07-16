package app

import (
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestSessionPickerRelativeAgeUsesSemanticBucketsAndControlledClock(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		age  time.Duration
		want sessionPickerRelativeAgeBucket
	}{
		{name: "just now", age: 10 * time.Second, want: sessionPickerRelativeAgeJustNow},
		{name: "minutes", age: 7 * time.Minute, want: sessionPickerRelativeAgeMinutes},
		{name: "hours", age: 3 * time.Hour, want: sessionPickerRelativeAgeHours},
		{name: "days", age: 2 * 24 * time.Hour, want: sessionPickerRelativeAgeDays},
		{name: "weeks", age: 3 * 7 * 24 * time.Hour, want: sessionPickerRelativeAgeWeeks},
		{name: "months", age: 70 * 24 * time.Hour, want: sessionPickerRelativeAgeMonths},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := relativeSessionAge(now.Add(-test.age), now)
			if got.Bucket != test.want {
				t.Fatalf("relative age bucket = %q, want %q", got.Bucket, test.want)
			}
		})
	}
}

func TestSessionPickerRelativeAgeHandlesFutureClockSkewSemantically(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	got := relativeSessionAge(now.Add(15*time.Minute), now)
	if got.Bucket != sessionPickerRelativeAgeFuture {
		t.Fatalf("future-skew bucket = %q, want %q", got.Bucket, sessionPickerRelativeAgeFuture)
	}
}

func TestSessionPickerRejectsInvalidRecencyBeforeRendering(t *testing.T) {
	for _, updatedAt := range []time.Time{
		time.Time{},
		time.Unix(0, 0).UTC(),
		time.Unix(-1, 0).UTC(),
	} {
		summary := pickerTestSummary(t, "invalid-recency", updatedAt)
		response := serverapi.SessionPageResponse{
			ProjectID: "picker-project",
			Category:  summary.Category,
			Sessions:  []clientui.SessionSummary{summary},
		}
		if err := response.Validate(); err == nil {
			t.Fatalf("summary with updated_at=%v unexpectedly validated", updatedAt)
		}
	}
}
