package serverapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
)

type AttentionNotificationSubscribeRequest struct{}

func (r AttentionNotificationSubscribeRequest) Validate() error {
	return nil
}

type AttentionSessionNotificationSubscribeRequest struct {
	SessionID                    string `json:"session_id"`
	IncludePendingPromptSnapshot bool   `json:"include_pending_prompt_snapshot,omitempty"`
}

func (r AttentionSessionNotificationSubscribeRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

type AttentionNotificationSubscription interface {
	Next(context.Context) (clientui.AttentionNotificationEvent, error)
	Close() error
}

type attentionEventValidator func(clientui.AttentionNotificationEvent) error
type attentionTargetValidator func(clientui.AttentionNotificationTarget) error
type attentionFocusValidator func(clientui.AttentionNotificationTaskDetailFocus) error
type attentionPayloadValidator func(clientui.AttentionNotification) error

var attentionEventValidators = map[clientui.AttentionNotificationEventType]attentionEventValidator{
	clientui.AttentionNotificationEventPending:          validatePendingAttentionEvent,
	clientui.AttentionNotificationEventResolved:         validateResolvedAttentionEvent,
	clientui.AttentionNotificationEventSnapshotComplete: validateSnapshotCompleteAttentionEvent,
}

var attentionSources = map[clientui.AttentionNotificationSource]struct{}{
	clientui.AttentionNotificationSourceLive:     {},
	clientui.AttentionNotificationSourceSnapshot: {},
}

var attentionTargetValidators = map[clientui.AttentionNotificationTargetKind]attentionTargetValidator{
	clientui.AttentionNotificationTargetWorkflowTask:  validateWorkflowTaskAttentionTarget,
	clientui.AttentionNotificationTargetSessionPrompt: validateSessionPromptAttentionTarget,
}

var attentionFocusValidators = map[clientui.AttentionNotificationFocusKind]attentionFocusValidator{
	clientui.AttentionNotificationFocusQuestion:       validateQuestionAttentionFocus,
	clientui.AttentionNotificationFocusApproval:       validateApprovalAttentionFocus,
	clientui.AttentionNotificationFocusInterruptedRun: validateInterruptedRunAttentionFocus,
}

var attentionPayloadValidators = map[clientui.AttentionNotificationKind]attentionPayloadValidator{
	clientui.AttentionNotificationKindQuestion:       validateQuestionAttentionPayload,
	clientui.AttentionNotificationKindApproval:       validateApprovalAttentionPayload,
	clientui.AttentionNotificationKindInterruptedRun: validateInterruptedRunAttentionPayload,
}

func ValidateAttentionNotificationEvent(event clientui.AttentionNotificationEvent) error {
	if event.Sequence == 0 {
		return errors.New("attention notification sequence is required")
	}
	validator, ok := attentionEventValidators[event.Type]
	if !ok {
		return fmt.Errorf("unsupported attention notification event type %q", event.Type)
	}
	return validator(event)
}

func validatePendingAttentionEvent(event clientui.AttentionNotificationEvent) error {
	if err := validateAttentionNotificationSource(event.Source); err != nil {
		return err
	}
	if event.ID != nil || event.Kind != "" || event.OccurredAt != nil {
		return errors.New("pending attention notification must not carry id, kind, or time envelope payload")
	}
	if event.Pending == nil {
		return errors.New("pending attention notification payload is required")
	}
	return validateAttentionNotification(*event.Pending)
}

func validateResolvedAttentionEvent(event clientui.AttentionNotificationEvent) error {
	if err := validateAttentionNotificationSource(event.Source); err != nil {
		return err
	}
	if event.Pending != nil {
		return errors.New("resolved attention notification must not carry pending payload")
	}
	if event.ID == nil {
		return errors.New("resolved attention notification id is required")
	}
	if err := validateAttentionNotificationID(*event.ID); err != nil {
		return err
	}
	if event.Kind == "" {
		return errors.New("resolved attention notification kind is required")
	}
	if event.ID.Kind != event.Kind {
		return errors.New("resolved attention notification id kind must match event kind")
	}
	if !supportedAttentionKind(event.Kind) {
		return fmt.Errorf("unsupported attention notification kind %q", event.Kind)
	}
	if event.OccurredAt == nil || event.OccurredAt.IsZero() {
		return errors.New("resolved attention notification occurred_at is required")
	}
	return nil
}

func validateSnapshotCompleteAttentionEvent(event clientui.AttentionNotificationEvent) error {
	if event.Source != clientui.AttentionNotificationSourceSnapshot {
		return errors.New("snapshot_complete attention notification source must be snapshot")
	}
	if event.Pending != nil || event.ID != nil || event.Kind != "" || event.OccurredAt != nil {
		return errors.New("snapshot_complete attention notification must not carry id, kind, time, or pending payload")
	}
	if event.SessionID == "" {
		return errors.New("snapshot_complete attention notification session_id is required")
	}
	return nil
}

func validateAttentionNotification(notification clientui.AttentionNotification) error {
	if err := validateAttentionNotificationID(notification.ID); err != nil {
		return err
	}
	if notification.Kind == "" {
		return errors.New("attention notification kind is required")
	}
	if notification.ID.Kind != notification.Kind {
		return errors.New("attention notification id kind must match notification kind")
	}
	if notification.Revision == 0 {
		return errors.New("attention notification revision is required")
	}
	if notification.OccurredAt.IsZero() {
		return errors.New("attention notification occurred_at is required")
	}
	if err := validateAttentionNotificationTarget(notification.Target); err != nil {
		return err
	}
	payloadValidator, ok := attentionPayloadValidators[notification.Kind]
	if !ok {
		return fmt.Errorf("unsupported attention notification kind %q", notification.Kind)
	}
	return payloadValidator(notification)
}

func validateAttentionNotificationID(id clientui.AttentionNotificationID) error {
	if id.Kind == "" {
		return errors.New("attention notification id kind is required")
	}
	if !supportedAttentionKind(id.Kind) {
		return fmt.Errorf("unsupported attention notification id kind %q", id.Kind)
	}
	if strings.TrimSpace(id.UUID) == "" {
		return errors.New("attention notification id uuid is required")
	}
	return nil
}

func validateAttentionNotificationSource(source clientui.AttentionNotificationSource) error {
	if _, ok := attentionSources[source]; ok {
		return nil
	}
	return fmt.Errorf("unsupported attention notification source %q", source)
}

func supportedAttentionKind(kind clientui.AttentionNotificationKind) bool {
	_, ok := attentionPayloadValidators[kind]
	return ok
}

func validateAttentionNotificationTarget(target clientui.AttentionNotificationTarget) error {
	validator, ok := attentionTargetValidators[target.Kind]
	if !ok {
		return fmt.Errorf("unsupported attention notification target kind %q", target.Kind)
	}
	return validator(target)
}

func validateWorkflowTaskAttentionTarget(target clientui.AttentionNotificationTarget) error {
	if target.TaskID == "" {
		return errors.New("workflow-task attention notification target task_id is required")
	}
	if target.Focus == nil {
		return errors.New("workflow-task attention notification target focus is required")
	}
	return validateTaskDetailFocus(*target.Focus)
}

func validateSessionPromptAttentionTarget(target clientui.AttentionNotificationTarget) error {
	if target.SessionID == "" {
		return errors.New("session-prompt attention notification target session_id is required")
	}
	return nil
}

func validateTaskDetailFocus(focus clientui.AttentionNotificationTaskDetailFocus) error {
	validator, ok := attentionFocusValidators[focus.Kind]
	if !ok {
		return fmt.Errorf("unsupported attention notification focus kind %q", focus.Kind)
	}
	return validator(focus)
}

func validateQuestionAttentionFocus(focus clientui.AttentionNotificationTaskDetailFocus) error {
	if len(focus.AskIDs) == 0 {
		return errors.New("question attention notification focus ask_ids is required")
	}
	for _, askID := range focus.AskIDs {
		if askID == "" {
			return errors.New("question attention notification focus ask_ids must be non-empty")
		}
	}
	return nil
}

func validateApprovalAttentionFocus(focus clientui.AttentionNotificationTaskDetailFocus) error {
	if focus.TaskTransitionID == "" {
		return errors.New("approval attention notification focus task_transition_id is required")
	}
	return nil
}

func validateInterruptedRunAttentionFocus(focus clientui.AttentionNotificationTaskDetailFocus) error {
	if focus.RunID == "" {
		return errors.New("interrupted-run attention notification focus run_id is required")
	}
	return nil
}

func validateQuestionAttentionPayload(notification clientui.AttentionNotification) error {
	if notification.Question == nil {
		return errors.New("question attention notification payload is required")
	}
	if notification.Approval != nil || notification.InterruptedRun != nil {
		return errors.New("question attention notification must not carry approval or interrupted-run payloads")
	}
	if err := validateQuestionAttentionState(*notification.Question); err != nil {
		return err
	}
	if notification.Target.Kind == clientui.AttentionNotificationTargetWorkflowTask &&
		(notification.Target.Focus == nil || notification.Target.Focus.Kind != clientui.AttentionNotificationFocusQuestion) {
		return errors.New("question attention notification workflow-task target focus kind must be question")
	}
	if notification.Target.Kind == clientui.AttentionNotificationTargetWorkflowTask &&
		!sameStringSet(notification.Target.Focus.AskIDs, notification.Question.PreparedAskIDs) {
		return errors.New("question attention notification focus ask_ids must match prepared ask ids")
	}
	return nil
}

func validateApprovalAttentionPayload(notification clientui.AttentionNotification) error {
	if notification.Approval == nil {
		return errors.New("approval attention notification payload is required")
	}
	if notification.Question != nil || notification.InterruptedRun != nil {
		return errors.New("approval attention notification must not carry question or interrupted-run payloads")
	}
	if strings.TrimSpace(notification.Approval.TaskTransitionID) == "" && strings.TrimSpace(notification.Approval.Message) == "" {
		return errors.New("approval attention notification payload requires task_transition_id or message")
	}
	if notification.Target.Kind == clientui.AttentionNotificationTargetWorkflowTask &&
		(notification.Target.Focus == nil || notification.Target.Focus.Kind != clientui.AttentionNotificationFocusApproval) {
		return errors.New("approval attention notification workflow-task target focus kind must be approval")
	}
	if notification.Target.Kind == clientui.AttentionNotificationTargetWorkflowTask &&
		strings.TrimSpace(notification.Approval.TaskTransitionID) == "" {
		return errors.New("approval attention notification workflow-task payload task_transition_id is required")
	}
	if notification.Target.Focus != nil && notification.Target.Focus.TaskTransitionID != notification.Approval.TaskTransitionID {
		return errors.New("approval attention notification focus task_transition_id must match approval payload")
	}
	return nil
}

func validateInterruptedRunAttentionPayload(notification clientui.AttentionNotification) error {
	if notification.InterruptedRun == nil {
		return errors.New("interrupted-run attention notification payload is required")
	}
	if notification.Question != nil || notification.Approval != nil {
		return errors.New("interrupted-run attention notification must not carry question or approval payloads")
	}
	if notification.Target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		return errors.New("interrupted-run attention notification target must be workflow task")
	}
	if notification.Target.RunID == "" {
		return errors.New("interrupted-run attention notification target run_id is required")
	}
	if notification.Target.Focus == nil || notification.Target.Focus.Kind != clientui.AttentionNotificationFocusInterruptedRun {
		return errors.New("interrupted-run attention notification workflow-task target focus kind must be interrupted_run")
	}
	if notification.Target.Focus.RunID != notification.Target.RunID || notification.InterruptedRun.RunID != notification.Target.RunID {
		return errors.New("interrupted-run attention notification run ids must match")
	}
	return nil
}

func validateQuestionAttentionState(state clientui.AttentionNotificationQuestionState) error {
	if len(state.PreparedAskIDs) == 0 {
		return errors.New("question attention notification prepared_ask_ids is required")
	}
	if err := validateNonEmptyStringList("question attention notification prepared_ask_ids", state.PreparedAskIDs); err != nil {
		return err
	}
	if err := validateNonEmptyStringList("question attention notification materialized_ask_ids", state.MaterializedAskIDs); err != nil {
		return err
	}
	if err := validateNonEmptyStringList("question attention notification current_unresolved_ask_ids", state.CurrentUnresolvedAskIDs); err != nil {
		return err
	}
	if err := validateNonEmptyStringList("question attention notification skipped_ask_ids", state.SkippedAskIDs); err != nil {
		return err
	}
	if state.DisplayCount <= 0 {
		return errors.New("question attention notification display_count must be positive")
	}
	if state.MaterializedCount != len(state.MaterializedAskIDs) {
		return errors.New("question attention notification materialized_count must match materialized_ask_ids")
	}
	if !stringListSubset(state.MaterializedAskIDs, state.PreparedAskIDs) ||
		!stringListSubset(state.CurrentUnresolvedAskIDs, state.MaterializedAskIDs) ||
		!stringListSubset(state.SkippedAskIDs, state.PreparedAskIDs) {
		return errors.New("question attention notification ask id lists must be consistent")
	}
	if state.DisplayCount != len(state.PreparedAskIDs)-len(state.SkippedAskIDs) {
		return errors.New("question attention notification display_count must match non-skipped prepared asks")
	}
	return nil
}

func validateNonEmptyStringList(label string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must contain only non-empty ids", label)
		}
	}
	return nil
}

func stringListSubset(values []string, allowed []string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := allowedSet[value]; !ok {
			return false
		}
	}
	return true
}

func sameStringSet(left []string, right []string) bool {
	return stringListSubset(left, right) && stringListSubset(right, left)
}
