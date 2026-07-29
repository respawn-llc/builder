package runtime

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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
