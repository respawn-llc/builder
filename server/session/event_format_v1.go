package session

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/invariant"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

const (
	EventLogContract  = "kent.session.events"
	EventLogVersionV1 = 1
	EventLogVersionV2 = 2
	CacheDigestV1     = 1
)

type EventLogHeader struct {
	Contract string `json:"contract"`
	Version  int    `json:"version"`
}

func encodeEventLogHeaderV1() ([]byte, error) {
	return encodeEventLogHeader(EventLogVersionV1)
}

func encodeEventLogHeader(version int) ([]byte, error) {
	line, err := json.Marshal(EventLogHeader{
		Contract: EventLogContract,
		Version:  version,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal event log header: %w", err)
	}
	return line, nil
}

func decodeEventLogHeader(line []byte) (EventLogHeader, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return EventLogHeader{}, fmt.Errorf("event log header is required")
	}
	var header EventLogHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return EventLogHeader{}, fmt.Errorf("decode event log header: %w", err)
	}
	if header.Contract != EventLogContract {
		return EventLogHeader{}, fmt.Errorf("unsupported event log contract %q", header.Contract)
	}
	if header.Version != EventLogVersionV1 && header.Version != EventLogVersionV2 {
		return EventLogHeader{}, fmt.Errorf("unsupported event log version %d", header.Version)
	}
	return header, nil
}

type EventKind string

const (
	EventKindMessage          EventKind = "message"
	EventKindToolCompletion   EventKind = "tool_completed"
	EventKindLocalEntry       EventKind = "local_entry"
	EventKindHistoryReplace   EventKind = "history_replaced"
	EventKindCacheRequest     EventKind = "cache_request_observed"
	EventKindCacheResponse    EventKind = "cache_response_observed"
	EventKindCacheWarning     EventKind = "cache_warning"
	EventKindReviewerFeedback EventKind = "reviewer_feedback"
	EventKindReviewerError    EventKind = "reviewer_error"
)

type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
	MessageRoleDeveloper MessageRole = "developer"
)

type EventRecordPayload interface {
	eventKind() EventKind
	validate() error
}

type EventRecord struct {
	seq               int64
	stepID            *string
	payload           EventRecordPayload
	committedAtUnixMs *transcript.CommittedAtUnixMs
}

func NewEventRecord(seq int64, stepID *string, payload EventRecordPayload) (EventRecord, error) {
	return newEventRecord(seq, stepID, payload, nil)
}

func newEventRecord(
	seq int64,
	stepID *string,
	payload EventRecordPayload,
	committedAtUnixMs *transcript.CommittedAtUnixMs,
) (EventRecord, error) {
	if seq <= 0 {
		return EventRecord{}, fmt.Errorf("event sequence must be positive: %d", seq)
	}
	normalizedStepID, err := normalizeOptionalEventIdentity("step identity", stepID)
	if err != nil {
		return EventRecord{}, err
	}
	if payload == nil {
		return EventRecord{}, fmt.Errorf("event payload is required")
	}
	switch typed := payload.(type) {
	case *ReviewerFeedbackRecord:
		if typed == nil {
			return EventRecord{}, fmt.Errorf("event payload is required")
		}
		copied := *typed
		copied.Suggestions = append([]string(nil), typed.Suggestions...)
		payload = copied
	case *ReviewerErrorRecord:
		if typed == nil {
			return EventRecord{}, fmt.Errorf("event payload is required")
		}
		copied := *typed
		payload = copied
	}
	switch payload.(type) {
	case ReviewerFeedbackRecord, ReviewerErrorRecord:
		if stepID == nil || strings.TrimSpace(*stepID) == "" {
			return EventRecord{}, fmt.Errorf("%s payload requires an enclosing step identity", payload.eventKind())
		}
		if _, err := runtimeids.ParseCanonicalUUIDv4(*stepID, "step identity"); err != nil {
			return EventRecord{}, fmt.Errorf("%s payload step identity: %w", payload.eventKind(), err)
		}
	}
	if err := payload.validate(); err != nil {
		return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), err)
	}
	if err := transcript.ValidateCommittedAtUnixMs(committedAtUnixMs); err != nil {
		return EventRecord{}, err
	}
	eligible, err := eventPayloadEligibleForCommittedTime(payload)
	if err != nil {
		return EventRecord{}, fmt.Errorf("evaluate committed time eligibility: %w", err)
	}
	if committedAtUnixMs != nil && !eligible {
		return EventRecord{}, fmt.Errorf(
			"committed time is not allowed for ineligible %s payload",
			payload.eventKind(),
		)
	}
	switch typed := payload.(type) {
	case MessageRecord:
		normalized, normalizeErr := normalizeMessageRecord(typed)
		if normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		payload = normalized
	case ToolCompletionRecord:
		normalized, normalizeErr := normalizeToolCompletionRecord(typed)
		if normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		payload = normalized
	case LocalEntryRecord:
		typed.Role = strings.TrimSpace(typed.Role)
		var normalizeErr error
		if typed.Text, normalizeErr = normalizeOptionalEventText("text", typed.Text); normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		if typed.Text != nil {
			trimmed := strings.TrimSpace(*typed.Text)
			typed.Text = &trimmed
		}
		if typed.CondensedText, normalizeErr = normalizeOptionalEventText("condensed text", typed.CondensedText); normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		if typed.DiagnosticKey, normalizeErr = normalizeOptionalEventIdentity("diagnostic key", typed.DiagnosticKey); normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		if typed.NoticeID, normalizeErr = normalizeOptionalEventIdentity("notice identity", typed.NoticeID); normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		if typed.AfterToolCallID, normalizeErr = normalizeOptionalEventIdentity("after-tool call identity", typed.AfterToolCallID); normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		if typed.DurationMs != nil {
			duration := *typed.DurationMs
			typed.DurationMs = &duration
		}
		if typed.ToolOutputRepair != nil {
			repair := *typed.ToolOutputRepair
			typed.ToolOutputRepair = &repair
		}
		if typed.ProviderModelMismatch != nil {
			mismatch := *typed.ProviderModelMismatch
			mismatch.RequestedModel = strings.TrimSpace(mismatch.RequestedModel)
			mismatch.ServedModel = strings.TrimSpace(mismatch.ServedModel)
			typed.ProviderModelMismatch = &mismatch
		}
		payload = typed
	case ReviewerFeedbackRecord:
		typed.Suggestions = append([]string(nil), typed.Suggestions...)
		payload = typed
	case ReviewerErrorRecord:
		payload = typed
	case HistoryReplacementRecord:
		normalized, normalizeErr := normalizeHistoryReplacementRecord(typed)
		if normalizeErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), normalizeErr)
		}
		payload = normalized
	case CacheRequestObservationRecord:
		typed.CacheKey = strings.TrimSpace(typed.CacheKey)
		typed.TerminalHash = strings.TrimSpace(typed.TerminalHash)
		payload = typed
	case CacheResponseObservationRecord:
		typed.CacheKey = strings.TrimSpace(typed.CacheKey)
		typed.TerminalHash = strings.TrimSpace(typed.TerminalHash)
		if typed.CachedInputTokens != nil {
			cachedInputTokens := *typed.CachedInputTokens
			typed.CachedInputTokens = &cachedInputTokens
		}
		payload = typed
	case CacheWarningRecord:
		cacheKey, cacheKeyErr := normalizeOptionalEventIdentity("cache key", typed.CacheKey)
		if cacheKeyErr != nil {
			return EventRecord{}, fmt.Errorf("%s payload: %w", payload.eventKind(), cacheKeyErr)
		}
		typed.CacheKey = cacheKey
		if typed.LostInputTokens != nil {
			lostInputTokens := *typed.LostInputTokens
			typed.LostInputTokens = &lostInputTokens
		}
		payload = typed
	}
	record := EventRecord{
		seq:               seq,
		stepID:            normalizedStepID,
		payload:           payload,
		committedAtUnixMs: committedAtUnixMs,
	}
	if record.committedAtUnixMs != nil {
		value := *record.committedAtUnixMs
		record.committedAtUnixMs = &value
	}
	return record, nil
}

func (r EventRecord) Seq() int64 {
	return r.seq
}

func (r EventRecord) StepID() *string {
	if r.stepID == nil {
		return nil
	}
	stepID := *r.stepID
	return &stepID
}

func (r EventRecord) CommittedAtUnixMs() *transcript.CommittedAtUnixMs {
	if r.committedAtUnixMs == nil {
		return nil
	}
	value := *r.committedAtUnixMs
	return &value
}

func (r EventRecord) Kind() (EventKind, error) {
	if r.payload == nil {
		err := errors.New("session event record invariant violated: payload is missing")
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"read_event_record_kind",
			err,
		))
		return "", err
	}
	return r.payload.eventKind(), nil
}

func (r EventRecord) Payload() (EventRecordPayload, error) {
	if r.payload == nil {
		err := errors.New("session event record invariant violated: payload is missing")
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"read_event_record_payload",
			err,
		))
		return nil, err
	}
	return r.payload, nil
}

type EntryVisibility string

const (
	EntryVisibilityAuto             EntryVisibility = "auto"
	EntryVisibilityOngoing          EntryVisibility = "ongoing"
	EntryVisibilityOngoingCollapsed EntryVisibility = "ongoing_collapsed"
	EntryVisibilityDetail           EntryVisibility = "detail"
	EntryVisibilityHidden           EntryVisibility = "hidden"
)

type LocalEntryRecord struct {
	Visibility            EntryVisibility                         `json:"visibility"`
	Role                  string                                  `json:"role"`
	Text                  *string                                 `json:"text"`
	DurationMs            *int64                                  `json:"duration_ms,omitempty"`
	CondensedText         *string                                 `json:"condensed_text,omitempty"`
	DiagnosticKey         *string                                 `json:"diagnostic_key,omitempty"`
	NoticeID              *string                                 `json:"notice_id,omitempty"`
	AfterToolCallID       *string                                 `json:"after_tool_call_id,omitempty"`
	ToolOutputRepair      *transcript.ToolOutputRepairNotice      `json:"tool_output_repair,omitempty"`
	ProviderModelMismatch *transcript.ProviderModelMismatchNotice `json:"provider_model_mismatch,omitempty"`
}

type ReviewerFeedbackRecord struct {
	ID          runtimeids.ReviewerFeedbackID `json:"id"`
	Suggestions []string                      `json:"suggestions"`
	Visibility  EntryVisibility               `json:"visibility"`
}

type ReviewerErrorRecord struct {
	ID     runtimeids.ReviewerErrorID `json:"id"`
	Detail string                     `json:"detail"`
}

type CompactionMode string

const (
	CompactionModeAuto                   CompactionMode = "auto"
	CompactionModeHandoff                CompactionMode = "handoff"
	CompactionModeManual                 CompactionMode = "manual"
	CompactionModeWorkflowPostCompletion CompactionMode = "workflow_post_completion"
)

type CacheScope string

const (
	CacheScopeConversation CacheScope = "conversation"
	CacheScopeReviewer     CacheScope = "reviewer"
)

type CacheRequestObservationRecord struct {
	DigestVersion int        `json:"digest_version"`
	CacheKey      string     `json:"cache_key"`
	Scope         CacheScope `json:"scope"`
	ChunkCount    int        `json:"chunk_count"`
	TerminalHash  string     `json:"terminal_hash"`
}

type CacheResponseObservationRecord struct {
	DigestVersion     int        `json:"digest_version"`
	CacheKey          string     `json:"cache_key"`
	Scope             CacheScope `json:"scope"`
	ChunkCount        int        `json:"chunk_count"`
	TerminalHash      string     `json:"terminal_hash"`
	CachedInputTokens *int       `json:"cached_input_tokens,omitempty"`
}

type CacheWarningReason string

const (
	CacheWarningReasonCompaction   CacheWarningReason = "compaction"
	CacheWarningReasonNonPostfix   CacheWarningReason = "non_postfix"
	CacheWarningReasonReuseDropped CacheWarningReason = "reuse_dropped"
)

type CacheWarningRecord struct {
	Scope           CacheScope         `json:"scope"`
	Reason          CacheWarningReason `json:"reason"`
	CacheKey        *string            `json:"cache_key,omitempty"`
	LostInputTokens *int               `json:"lost_input_tokens,omitempty"`
}

func (CacheWarningRecord) eventKind() EventKind {
	return EventKindCacheWarning
}

func (r CacheWarningRecord) validate() error {
	if err := validateCacheScope(r.Scope); err != nil {
		return err
	}
	switch r.Reason {
	case CacheWarningReasonCompaction, CacheWarningReasonNonPostfix, CacheWarningReasonReuseDropped:
	default:
		return fmt.Errorf("unsupported cache warning reason %q", r.Reason)
	}
	if _, err := normalizeOptionalEventIdentity("cache key", r.CacheKey); err != nil {
		return err
	}
	if r.LostInputTokens != nil && *r.LostInputTokens <= 0 {
		return fmt.Errorf("lost input tokens must be positive when present: %d", *r.LostInputTokens)
	}
	return nil
}

func (CacheResponseObservationRecord) eventKind() EventKind {
	return EventKindCacheResponse
}

func (r CacheResponseObservationRecord) validate() error {
	request := CacheRequestObservationRecord{
		DigestVersion: r.DigestVersion,
		CacheKey:      r.CacheKey,
		Scope:         r.Scope,
		ChunkCount:    r.ChunkCount,
		TerminalHash:  r.TerminalHash,
	}
	if err := request.validate(); err != nil {
		return err
	}
	if r.CachedInputTokens != nil && *r.CachedInputTokens < 0 {
		return fmt.Errorf("cached input tokens must not be negative: %d", *r.CachedInputTokens)
	}
	return nil
}

func (CacheRequestObservationRecord) eventKind() EventKind {
	return EventKindCacheRequest
}

func (r CacheRequestObservationRecord) validate() error {
	if r.DigestVersion != CacheDigestV1 {
		return fmt.Errorf("unsupported cache digest version %d", r.DigestVersion)
	}
	if strings.TrimSpace(r.CacheKey) == "" {
		return fmt.Errorf("cache key is required")
	}
	if err := validateCacheScope(r.Scope); err != nil {
		return err
	}
	if r.ChunkCount <= 0 {
		return fmt.Errorf("chunk count must be positive: %d", r.ChunkCount)
	}
	if strings.TrimSpace(r.TerminalHash) == "" {
		return fmt.Errorf("terminal hash is required")
	}
	decodedHash, err := hex.DecodeString(strings.TrimSpace(r.TerminalHash))
	if err != nil || len(decodedHash) != 32 {
		return fmt.Errorf("terminal hash must be a SHA-256 hexadecimal digest")
	}
	return nil
}

func (HistoryReplacementRecord) eventKind() EventKind {
	return EventKindHistoryReplace
}

func (r HistoryReplacementRecord) validate() error {
	_, err := normalizeHistoryReplacementRecord(r)
	return err
}

func (LocalEntryRecord) eventKind() EventKind {
	return EventKindLocalEntry
}

func (r LocalEntryRecord) validate() error {
	switch r.Visibility {
	case EntryVisibilityAuto, EntryVisibilityOngoing, EntryVisibilityOngoingCollapsed,
		EntryVisibilityDetail, EntryVisibilityHidden:
	default:
		return fmt.Errorf("unsupported visibility %q", r.Visibility)
	}
	if strings.TrimSpace(r.Role) == "" {
		return fmt.Errorf("role is required")
	}
	if r.Text == nil && r.ToolOutputRepair == nil && r.ProviderModelMismatch == nil {
		return fmt.Errorf("text or typed notice facts are required")
	}
	if r.Text != nil && strings.TrimSpace(*r.Text) == "" {
		return fmt.Errorf("text must be non-empty when present")
	}
	if r.ToolOutputRepair != nil && !r.ToolOutputRepair.Valid() {
		return fmt.Errorf("tool-output repair facts are invalid")
	}
	if r.ProviderModelMismatch != nil && !r.ProviderModelMismatch.Valid() {
		return fmt.Errorf("provider-model mismatch facts are invalid")
	}
	if r.ToolOutputRepair != nil && r.ProviderModelMismatch != nil {
		return fmt.Errorf("local entry cannot carry multiple typed notice facts")
	}
	if r.DurationMs != nil && *r.DurationMs < 0 {
		return fmt.Errorf("duration_ms must not be negative")
	}
	return nil
}

func (ReviewerFeedbackRecord) eventKind() EventKind {
	return EventKindReviewerFeedback
}

func (r ReviewerFeedbackRecord) validate() error {
	if r.ID.IsZero() {
		return fmt.Errorf("reviewer feedback identity is required")
	}
	if len(r.Suggestions) == 0 {
		return fmt.Errorf("reviewer feedback suggestions are required")
	}
	for index, suggestion := range r.Suggestions {
		if strings.TrimSpace(suggestion) == "" {
			return fmt.Errorf("reviewer feedback suggestion %d is required", index)
		}
	}
	switch r.Visibility {
	case EntryVisibilityOngoing, EntryVisibilityOngoingCollapsed:
		return nil
	default:
		return fmt.Errorf("reviewer feedback visibility must be ongoing or ongoing_collapsed, got %q", r.Visibility)
	}
}

func (ReviewerErrorRecord) eventKind() EventKind {
	return EventKindReviewerError
}

func (r ReviewerErrorRecord) validate() error {
	if r.ID.IsZero() {
		return fmt.Errorf("reviewer error identity is required")
	}
	if strings.TrimSpace(r.Detail) == "" {
		return fmt.Errorf("reviewer error detail is required")
	}
	return nil
}

type eventRecordV1Envelope struct {
	Seq               int64                         `json:"seq"`
	Kind              EventKind                     `json:"kind"`
	StepID            *string                       `json:"step_id,omitempty"`
	CommittedAtUnixMs *transcript.CommittedAtUnixMs `json:"committed_at_unix_ms,omitempty"`
	Payload           json.RawMessage               `json:"payload"`
}

func (e *eventRecordV1Envelope) UnmarshalJSON(data []byte) error {
	type envelopeAlias eventRecordV1Envelope
	var decoded envelopeAlias
	if _, _, err := transcript.DecodeCommittedAtUnixMsField(data, "committed_at_unix_ms"); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = eventRecordV1Envelope(decoded)
	return nil
}

func encodeEventRecordV1(record EventRecord) ([]byte, error) {
	return encodeEventRecord(record, encodeEventRecordPayloadV1)
}

func encodeEventRecordPayloadV1(payload EventRecordPayload) ([]byte, error) {
	switch typed := payload.(type) {
	case ToolCompletionRecord:
		return encodeToolCompletionRecordV1(typed)
	case HistoryReplacementRecord:
		return encodeHistoryReplacementRecordV1(typed)
	default:
		return json.Marshal(payload)
	}
}

func encodeEventRecord(
	record EventRecord,
	encodePayload func(EventRecordPayload) ([]byte, error),
) ([]byte, error) {
	normalized, err := newEventRecord(
		record.seq,
		record.stepID,
		record.payload,
		record.committedAtUnixMs,
	)
	if err != nil {
		return nil, err
	}
	payload, err := encodePayload(normalized.payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", normalized.payload.eventKind(), err)
	}
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	if err := writeMarshaledJSONField(&buffer, "seq", normalized.seq, false); err != nil {
		return nil, err
	}
	if err := writeMarshaledJSONField(
		&buffer,
		"kind",
		normalized.payload.eventKind(),
		true,
	); err != nil {
		return nil, err
	}
	if normalized.stepID != nil {
		if err := writeMarshaledJSONField(&buffer, "step_id", normalized.stepID, true); err != nil {
			return nil, err
		}
	}
	if normalized.committedAtUnixMs != nil {
		if err := writeMarshaledJSONField(
			&buffer,
			"committed_at_unix_ms",
			normalized.committedAtUnixMs,
			true,
		); err != nil {
			return nil, err
		}
	}
	if err := writeJSONField(&buffer, "payload", payload, true); err != nil {
		return nil, err
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func decodeEventRecordV1(line []byte) (EventRecord, error) {
	var envelope eventRecordV1Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return EventRecord{}, fmt.Errorf("decode event record: %w", err)
	}
	payload, err := decodeEventRecordPayloadV1(
		envelope.Kind,
		func(target any) error {
			return json.Unmarshal(envelope.Payload, target)
		},
	)
	if err != nil {
		return EventRecord{}, err
	}
	return newEventRecord(
		envelope.Seq,
		envelope.StepID,
		payload,
		envelope.CommittedAtUnixMs,
	)
}

func encodeEventRecordV2(record EventRecord) ([]byte, error) {
	if err := validateEventRecordV2(record); err != nil {
		return nil, err
	}
	return encodeEventRecord(record, encodeEventRecordPayloadV2)
}

func decodeEventRecordV2(line []byte) (EventRecord, error) {
	var envelope eventRecordV1Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return EventRecord{}, fmt.Errorf("decode event record: %w", err)
	}
	payload, err := decodeEventRecordPayloadV2(
		envelope.Kind,
		func(target any) error {
			return json.Unmarshal(envelope.Payload, target)
		},
	)
	if err != nil {
		return EventRecord{}, err
	}
	record, err := newEventRecord(
		envelope.Seq,
		envelope.StepID,
		payload,
		envelope.CommittedAtUnixMs,
	)
	if err != nil {
		return EventRecord{}, err
	}
	if err := validateEventRecordV2(record); err != nil {
		return EventRecord{}, fmt.Errorf(
			"event sequence %d kind %q: %w",
			record.Seq(),
			envelope.Kind,
			err,
		)
	}
	return record, nil
}

func validateEventRecordV2(record EventRecord) error {
	payload, err := record.Payload()
	if err != nil {
		return err
	}
	completion, ok := payload.(ToolCompletionRecord)
	if !ok {
		return nil
	}
	isQuestion := completion.Name == askQuestionToolName
	successfulQuestion := isQuestion && !completion.IsError
	if err := validateV2QuestionAnswerPlacement(
		completion.Name,
		completion.IsError,
		completion.QuestionAnswer != nil,
	); err != nil {
		return err
	}
	switch {
	case successfulQuestion && record.CommittedAtUnixMs() == nil:
		return errors.New("successful ask_question completion requires a committed timestamp")
	default:
		return nil
	}
}

func validateV2QuestionAnswerPlacement(
	toolName string,
	isError bool,
	answerPresent bool,
) error {
	successfulQuestion := toolName == askQuestionToolName && !isError
	switch {
	case successfulQuestion && !answerPresent:
		return errors.New("successful ask_question completion requires typed Question-answer facts")
	case answerPresent && !successfulQuestion:
		return errors.New("typed Question-answer facts require a successful ask_question completion")
	default:
		return nil
	}
}

func encodeEventRecordPayloadV2(payload EventRecordPayload) ([]byte, error) {
	switch typed := payload.(type) {
	case ToolCompletionRecord:
		return encodeToolCompletionRecordV2(typed)
	case HistoryReplacementRecord:
		return encodeHistoryReplacementRecordV1(typed)
	default:
		return json.Marshal(payload)
	}
}

func encodeToolCompletionRecordV2(record ToolCompletionRecord) ([]byte, error) {
	payload, err := encodeToolCompletionRecordV1(record)
	if err != nil || record.QuestionAnswer == nil {
		return payload, err
	}
	answer, err := json.Marshal(record.QuestionAnswer)
	if err != nil {
		return nil, err
	}
	payload = payload[:len(payload)-1]
	payload = append(payload, []byte(`,"question_answer":`)...)
	payload = append(payload, answer...)
	payload = append(payload, '}')
	return payload, nil
}

func decodeEventRecordPayloadV2(
	kind EventKind,
	decode func(any) error,
) (EventRecordPayload, error) {
	if kind != EventKindToolCompletion {
		return decodeEventRecordPayloadV1(kind, decode)
	}
	var wire struct {
		CallID         string                       `json:"call_id"`
		Name           string                       `json:"name"`
		OutputKind     ToolOutputKind               `json:"output_kind"`
		IsError        *bool                        `json:"is_error"`
		Output         json.RawMessage              `json:"output"`
		Summary        *string                      `json:"summary,omitempty"`
		CondensedText  *string                      `json:"condensed_text,omitempty"`
		Presentation   json.RawMessage              `json:"presentation,omitempty"`
		ProviderItems  []ToolCompletionProviderItem `json:"provider_items,omitempty"`
		QuestionAnswer *QuestionAnswerRecord        `json:"question_answer,omitempty"`
	}
	if err := decode(&wire); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", kind, err)
	}
	if wire.IsError == nil {
		return nil, fmt.Errorf("decode %s payload: is_error is required", kind)
	}
	return ToolCompletionRecord{
		CallID: wire.CallID, Name: wire.Name, OutputKind: wire.OutputKind,
		IsError: *wire.IsError, Output: wire.Output, Summary: wire.Summary,
		CondensedText: wire.CondensedText, Presentation: wire.Presentation,
		ProviderItems: wire.ProviderItems, QuestionAnswer: wire.QuestionAnswer,
	}, nil
}

func decodeEventRecordPayloadV1(
	kind EventKind,
	decode func(any) error,
) (EventRecordPayload, error) {
	if decode == nil {
		return nil, fmt.Errorf("event record payload decoder is required")
	}
	if err := validateEventKind(kind); err != nil {
		return nil, err
	}
	var payload EventRecordPayload
	switch kind {
	case EventKindMessage:
		var message MessageRecord
		if err := decode(&message); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = message
	case EventKindToolCompletion:
		var wire toolCompletionRecordV1Wire
		if err := decode(&wire); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		if wire.IsError == nil {
			return nil, fmt.Errorf("decode %s payload: is_error is required", kind)
		}
		completion := ToolCompletionRecord{
			CallID:        wire.CallID,
			Name:          wire.Name,
			OutputKind:    wire.OutputKind,
			IsError:       *wire.IsError,
			Output:        wire.Output,
			Summary:       wire.Summary,
			CondensedText: wire.CondensedText,
			Presentation:  wire.Presentation,
			ProviderItems: wire.ProviderItems,
		}
		payload = completion
	case EventKindLocalEntry:
		var entry LocalEntryRecord
		if err := decode(&entry); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = entry
	case EventKindReviewerFeedback:
		var feedback ReviewerFeedbackRecord
		if err := decode(&feedback); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = feedback
	case EventKindReviewerError:
		var reviewerError ReviewerErrorRecord
		if err := decode(&reviewerError); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = reviewerError
	case EventKindHistoryReplace:
		var replacement HistoryReplacementRecord
		if err := decode(&replacement); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = replacement
	case EventKindCacheRequest:
		var observation CacheRequestObservationRecord
		if err := decode(&observation); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = observation
	case EventKindCacheResponse:
		var observation CacheResponseObservationRecord
		if err := decode(&observation); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = observation
	case EventKindCacheWarning:
		var warning CacheWarningRecord
		if err := decode(&warning); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", kind, err)
		}
		payload = warning
	default:
		panic(fmt.Sprintf("validated event kind %q has no payload decoder", kind))
	}
	return payload, nil
}

func validateEventKind(kind EventKind) error {
	switch kind {
	case EventKindMessage,
		EventKindToolCompletion,
		EventKindLocalEntry,
		EventKindHistoryReplace,
		EventKindCacheRequest,
		EventKindCacheResponse,
		EventKindCacheWarning,
		EventKindReviewerFeedback,
		EventKindReviewerError:
		return nil
	default:
		return fmt.Errorf("unsupported event kind %q", kind)
	}
}

func normalizeOptionalEventIdentity(name string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, fmt.Errorf("%s must be non-empty when present", name)
	}
	return &trimmed, nil
}

func normalizeOptionalEventText(name string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if strings.TrimSpace(*value) == "" {
		return nil, fmt.Errorf("%s must be non-empty when present", name)
	}
	copied := *value
	return &copied, nil
}

func validateJSONValue(name string, value json.RawMessage) error {
	if len(bytes.TrimSpace(value)) == 0 {
		return fmt.Errorf("%s JSON value is required", name)
	}
	if !json.Valid(value) {
		return fmt.Errorf("%s must be valid JSON", name)
	}
	return nil
}

func validateCacheScope(scope CacheScope) error {
	switch scope {
	case CacheScopeConversation, CacheScopeReviewer:
		return nil
	default:
		return fmt.Errorf("unsupported cache scope %q", scope)
	}
}
