package runtime

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// backgroundNoticeIdentity is the stable identity of one terminal shell
// completion. Developer notices that are not terminal shell work intentionally
// have no identity; they cannot participate in terminal finalization.
type backgroundNoticeIdentity struct {
	processID string
	activity  uuid.UUID
}

func newBackgroundNoticeIdentity(processID string, activity uuid.UUID) backgroundNoticeIdentity {
	processID = strings.TrimSpace(processID)
	if processID == "" {
		panic("background notice identity requires process id")
	}
	// Manager-originated events have a UUIDv4 Activity. The runtime also
	// supports synthetic internal notices that predate activity correlation;
	// Authority rejects non-v4 external shell events before they reach here.
	if activity != uuid.Nil && activity.Version() != 4 {
		panic(fmt.Sprintf("background notice identity requires UUIDv4 activity id: %q", activity))
	}
	return backgroundNoticeIdentity{processID: processID, activity: activity}
}

type queuedBackgroundNotice struct {
	key        uuid.UUID
	identity   *backgroundNoticeIdentity
	intent     steeringIntent
	diagnostic *PendingBackgroundDeliveryDiagnostic
}

func newTerminalBackgroundNotice(
	processID string,
	activity uuid.UUID,
	intent steeringIntent,
) queuedBackgroundNotice {
	identity := newBackgroundNoticeIdentity(processID, activity)
	return queuedBackgroundNotice{key: uuid.New(), identity: &identity, intent: intent}
}

func newDeveloperBackgroundNotice(intent steeringIntent) queuedBackgroundNotice {
	return queuedBackgroundNotice{key: uuid.New(), intent: intent}
}

func (n queuedBackgroundNotice) hasIdentity() bool {
	return n.identity != nil
}

func (n queuedBackgroundNotice) processID() string {
	if n.identity == nil {
		return ""
	}
	return n.identity.processID
}

func (n queuedBackgroundNotice) activityID() uuid.UUID {
	if n.identity == nil {
		return uuid.Nil
	}
	return n.identity.activity
}

func (n queuedBackgroundNotice) matches(processID string, activity uuid.UUID) bool {
	return n.identity != nil &&
		n.identity.processID == strings.TrimSpace(processID) &&
		n.identity.activity == activity
}

type backgroundNoticeState interface {
	backgroundNoticeState()
	queuedBackgroundNotice() (queuedBackgroundNotice, bool)
}

type pendingBackgroundNotice struct {
	notice queuedBackgroundNotice
}

func (pendingBackgroundNotice) backgroundNoticeState() {}
func (s pendingBackgroundNotice) queuedBackgroundNotice() (queuedBackgroundNotice, bool) {
	return s.notice, true
}

// reservedBackgroundNotice remains in scheduler state while persistence is in
// flight. Its receipt, not the start of steering, decides whether it settles,
// retries, or is withdrawn by Workflow retirement.
type reservedBackgroundNotice struct {
	notice      queuedBackgroundNotice
	reservation uint64
}

func (reservedBackgroundNotice) backgroundNoticeState() {}
func (s reservedBackgroundNotice) queuedBackgroundNotice() (queuedBackgroundNotice, bool) {
	return s.notice, true
}

func newReservedBackgroundNotice(notice queuedBackgroundNotice, reservation uint64) reservedBackgroundNotice {
	if reservation == 0 {
		panic("reserved background notice requires reservation")
	}
	return reservedBackgroundNotice{notice: notice, reservation: reservation}
}

type retryDeferredBackgroundNotice struct {
	notice     queuedBackgroundNotice
	generation uint64
}

func (retryDeferredBackgroundNotice) backgroundNoticeState() {}
func (s retryDeferredBackgroundNotice) queuedBackgroundNotice() (queuedBackgroundNotice, bool) {
	return s.notice, true
}

func newRetryDeferredBackgroundNotice(notice queuedBackgroundNotice, generation uint64) retryDeferredBackgroundNotice {
	if generation == 0 {
		panic("retry-deferred background notice requires generation")
	}
	return retryDeferredBackgroundNotice{notice: notice, generation: generation}
}

// withdrawingBackgroundNotice is a Workflow-owned reservation that must not
// begin another automatic attempt. It remains visible until its in-flight
// receipt is classified by the scheduler.
type withdrawingBackgroundNotice struct {
	notice      queuedBackgroundNotice
	reservation uint64
}

func (withdrawingBackgroundNotice) backgroundNoticeState() {}
func (s withdrawingBackgroundNotice) queuedBackgroundNotice() (queuedBackgroundNotice, bool) {
	return s.notice, true
}

func newWithdrawingBackgroundNotice(notice queuedBackgroundNotice, reservation uint64) withdrawingBackgroundNotice {
	if reservation == 0 {
		panic("withdrawing background notice requires reservation")
	}
	return withdrawingBackgroundNotice{notice: notice, reservation: reservation}
}

// diagnosticOnlyBackgroundNotice preserves a user-visible delivery failure
// after the terminal completion itself reached final acceptance.
type diagnosticOnlyBackgroundNotice struct {
	diagnostic PendingBackgroundDeliveryDiagnostic
}

func (diagnosticOnlyBackgroundNotice) backgroundNoticeState() {}
func (diagnosticOnlyBackgroundNotice) queuedBackgroundNotice() (queuedBackgroundNotice, bool) {
	return queuedBackgroundNotice{}, false
}

func newDiagnosticOnlyBackgroundNotice(diagnostic PendingBackgroundDeliveryDiagnostic) diagnosticOnlyBackgroundNotice {
	if diagnostic.processID == "" || diagnostic.activity.Version() != 4 || diagnostic.attempt == 0 {
		panic("diagnostic-only background notice requires a valid diagnostic")
	}
	return diagnosticOnlyBackgroundNotice{diagnostic: diagnostic}
}

type deferredBackgroundRetryPermit struct {
	generation uint64
}

func newDeferredBackgroundRetryPermit(generation uint64) deferredBackgroundRetryPermit {
	if generation == 0 {
		panic("background retry permit requires generation")
	}
	return deferredBackgroundRetryPermit{generation: generation}
}

type backgroundLifecycleTask interface {
	backgroundLifecycleTask()
	backgroundLifecycleTaskAttempt() uint64
}

type scheduledBackgroundLifecycleTask struct {
	attempt uint64
}

func (scheduledBackgroundLifecycleTask) backgroundLifecycleTask() {}
func (t scheduledBackgroundLifecycleTask) backgroundLifecycleTaskAttempt() uint64 {
	return t.attempt
}

type runningBackgroundLifecycleTask struct {
	attempt uint64
}

func (runningBackgroundLifecycleTask) backgroundLifecycleTask() {}
func (t runningBackgroundLifecycleTask) backgroundLifecycleTaskAttempt() uint64 {
	return t.attempt
}

func nextBackgroundLifecycleAttempt(current uint64) uint64 {
	current++
	if current == 0 {
		panic("background lifecycle task attempt overflow")
	}
	return current
}

func backgroundLifecycleTaskAttempt(task backgroundLifecycleTask) uint64 {
	if task == nil {
		panic("background lifecycle task is absent")
	}
	attempt := task.backgroundLifecycleTaskAttempt()
	if attempt == 0 {
		panic(fmt.Sprintf("background lifecycle task has invalid attempt %d", attempt))
	}
	return attempt
}
