package runtime

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"core/server/session"
	"core/shared/transcript"

	"github.com/google/uuid"
)

const maxPendingBackgroundDeliveryDiagnosticBytes = 4 << 10

type backgroundDeliveryStage string

const backgroundDeliveryStageAutomaticSteering backgroundDeliveryStage = "automatic_steering"
const backgroundDeliveryStageRouting backgroundDeliveryStage = "routing"
const backgroundDeliveryStagePreparation backgroundDeliveryStage = "preparation"

// PendingBackgroundDeliveryDiagnostic is the bounded recovery record for one
// failed automatic completion delivery. It deliberately owns no error or
// completion payload: those values can retain arbitrary process output.
type PendingBackgroundDeliveryDiagnostic struct {
	processID string
	activity  uuid.UUID
	stage     backgroundDeliveryStage
	attempt   uint64
	detail    string
}

// BackgroundOwnerPollFinalization is the commit-gated owner-relative result
// returned by runtime composition after a terminal write_stdin record commits.
// A present diagnostic has moved out of Manager and must become diagnostic-only
// scheduler work in the caller Engine's same output mutation.
type BackgroundOwnerPollFinalization struct {
	Finalized  bool
	Diagnostic *PendingBackgroundDeliveryDiagnostic
}

func newPendingBackgroundDeliveryDiagnostic(
	processID string,
	activity uuid.UUID,
	stage backgroundDeliveryStage,
	attempt uint64,
	cause error,
) PendingBackgroundDeliveryDiagnostic {
	processID = strings.TrimSpace(processID)
	if processID == "" {
		panic("background delivery diagnostic requires process id")
	}
	if activity.Version() != 4 {
		panic(fmt.Sprintf("background delivery diagnostic requires UUIDv4 activity id: %q", activity))
	}
	if stage != backgroundDeliveryStageAutomaticSteering &&
		stage != backgroundDeliveryStageRouting &&
		stage != backgroundDeliveryStagePreparation {
		panic(fmt.Sprintf("background delivery diagnostic has unsupported stage %q", stage))
	}
	if attempt == 0 {
		panic("background delivery diagnostic requires attempt")
	}
	detail := "background delivery failed"
	if cause != nil {
		detail = cause.Error()
	}
	return PendingBackgroundDeliveryDiagnostic{
		processID: processID,
		activity:  activity,
		stage:     stage,
		attempt:   attempt,
		detail:    boundedUTF8DiagnosticDetail(detail),
	}
}

// NewBackgroundRoutingDiagnostic retains an Authority routing failure without
// retaining the error object or shell payload. It is committed by the next
// eligible owner resource before the Manager completion is replayed.
func NewBackgroundRoutingDiagnostic(
	processID string,
	activity uuid.UUID,
	cause error,
) PendingBackgroundDeliveryDiagnostic {
	return newPendingBackgroundDeliveryDiagnostic(
		processID,
		activity,
		backgroundDeliveryStageRouting,
		1,
		cause,
	)
}

func NewBackgroundRoutingDiagnosticDetail(
	processID string,
	activity uuid.UUID,
	attempt uint64,
	detail string,
) PendingBackgroundDeliveryDiagnostic {
	return newPendingBackgroundDeliveryDiagnostic(
		processID,
		activity,
		backgroundDeliveryStageRouting,
		attempt,
		errors.New(detail),
	)
}

func boundedUTF8DiagnosticDetail(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maxPendingBackgroundDeliveryDiagnosticBytes {
		return value
	}
	value = value[:maxPendingBackgroundDeliveryDiagnosticBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (d PendingBackgroundDeliveryDiagnostic) message() string {
	return fmt.Sprintf(
		"Background completion delivery for process %s failed during %s (attempt %d): %s",
		d.processID,
		d.stage,
		d.attempt,
		d.detail,
	)
}

// BackgroundDeliveryError is an ephemeral operation error. The original cause
// is intentionally never copied into retained scheduler or Authority state.
type BackgroundDeliveryError struct {
	ProcessID string
	Activity  uuid.UUID
	Stage     backgroundDeliveryStage
	Attempt   uint64
	Cause     error
}

func (e *BackgroundDeliveryError) Error() string {
	if e == nil {
		return "background delivery failed"
	}
	return fmt.Sprintf(
		"background delivery failed: process_id=%s activity_id=%s stage=%s attempt=%d: %v",
		e.ProcessID,
		e.Activity,
		e.Stage,
		e.Attempt,
		e.Cause,
	)
}

func (e *BackgroundDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *Engine) commitBackgroundDeliveryDiagnostic(diagnostic PendingBackgroundDeliveryDiagnostic) (session.CommitReceipt, error) {
	text := diagnostic.message()
	receipt, err := e.steerWithCommitReceipt("", steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:       text,
	}))
	e.SetStreamingError(text)
	return receipt, err
}

// CommitPendingBackgroundDeliveryDiagnostic is the ordered runtime boundary
// used when Session ownership hands a preserved delivery diagnostic to a new
// eligible resource. It does not schedule a model continuation.
func (e *Engine) CommitPendingBackgroundDeliveryDiagnostic(
	diagnostic PendingBackgroundDeliveryDiagnostic,
) (session.CommitReceipt, error) {
	receipt, err := e.commitBackgroundDeliveryDiagnostic(diagnostic)
	if !receipt.Committed {
		return receipt, &BackgroundDeliveryError{
			ProcessID: diagnostic.processID,
			Activity:  diagnostic.activity,
			Stage:     diagnostic.stage,
			Attempt:   diagnostic.attempt,
			Cause:     err,
		}
	}
	return receipt, err
}
