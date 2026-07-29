package runtime

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"core/server/tools"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestPendingBackgroundDeliveryDiagnosticBoundsAndCopiesFailureDetail(t *testing.T) {
	payload := string([]byte{0xff}) + strings.Repeat("x", maxPendingBackgroundDeliveryDiagnosticBytes*2)
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"process-1",
		uuid.New(),
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New(payload),
	)
	if len(diagnostic.detail) > maxPendingBackgroundDeliveryDiagnosticBytes {
		t.Fatalf("retained detail bytes = %d, limit = %d", len(diagnostic.detail), maxPendingBackgroundDeliveryDiagnosticBytes)
	}
	if !utf8.ValidString(diagnostic.detail) {
		t.Fatal("retained detail is not valid UTF-8")
	}
	if diagnostic.attempt != 1 || diagnostic.processID != "process-1" || diagnostic.activity == uuid.Nil {
		t.Fatalf("diagnostic identity = %+v", diagnostic)
	}
}

func TestPendingBackgroundDeliveryDiagnosticRejectsInvalidRequiredIdentity(t *testing.T) {
	tests := []struct {
		name string
		run  func()
	}{
		{
			name: "process",
			run: func() {
				_ = newPendingBackgroundDeliveryDiagnostic("", uuid.New(), backgroundDeliveryStageAutomaticSteering, 1, errors.New("failed"))
			},
		},
		{
			name: "activity",
			run: func() {
				_ = newPendingBackgroundDeliveryDiagnostic("process-1", uuid.Nil, backgroundDeliveryStageAutomaticSteering, 1, errors.New("failed"))
			},
		},
		{
			name: "attempt",
			run: func() {
				_ = newPendingBackgroundDeliveryDiagnostic("process-1", uuid.New(), backgroundDeliveryStageAutomaticSteering, 0, errors.New("failed"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor invariant panic")
				}
			}()
			test.run()
		})
	}
}

func TestBackgroundDeliveryDiagnosticPersistsOnlyAfterCommit(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"process-1",
		uuid.New(),
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New("persistence failed"),
	)
	blocker := mustBlockTestEventLogAppends(t, store)

	receipt, err := engine.commitBackgroundDeliveryDiagnostic(diagnostic)
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted diagnostic receipt = %+v error = %v", receipt, err)
	}
	before := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			before++
		}
	}

	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log appends: %v", err)
	}
	receipt, err = engine.commitBackgroundDeliveryDiagnostic(diagnostic)
	if err != nil || !receipt.Committed {
		t.Fatalf("committed diagnostic receipt = %+v error = %v", receipt, err)
	}
	after := 0
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			after++
		}
	}
	if after != before+1 {
		t.Fatalf("committed background delivery diagnostics = %d, want %d", after, before+1)
	}
}
