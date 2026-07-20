package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"core/shared/rollbacktarget"
)

type legacyEventV0DecodeResult struct {
	Sequence           int64
	Record             *EventRecord
	FallbackCompletion *legacyToolCompletionFallback
	Dropped            bool
	SnapshotClass      legacyToolSnapshotClass
}

type legacyToolSnapshotClass uint8

const (
	legacyToolSnapshotNone legacyToolSnapshotClass = iota
	legacyToolSnapshotAuthoritative
	legacyToolSnapshotGeneratedRaw
	legacyToolSnapshotUnsupportedMissingRaw
	legacyToolSnapshotAbsent
)

type legacyToolSnapshotError struct {
	Sequence  int64
	ItemIndex int
	Type      ProviderHistoryItemType
	Reason    ToolCompletionProviderItemErrorReason
}

func (e legacyToolSnapshotError) Error() string {
	return fmt.Sprintf(
		"legacy tool completion %d provider item %d is invalid (type=%q reason=%q)",
		e.Sequence,
		e.ItemIndex,
		e.Type,
		e.Reason,
	)
}

func (e legacyToolSnapshotError) Unwrap() error {
	return ErrToolCompletionProviderItem
}

type legacyToolCompletionFallback struct {
	Sequence      int64
	StepID        *string
	CallID        string
	Name          string
	IsError       bool
	Output        *migrationValueSource
	Summary       *string
	CondensedText *string
	Presentation  *migrationValueSource
}

func (f *legacyToolCompletionFallback) Close() error {
	if f == nil {
		return nil
	}
	return errors.Join(f.Output.Close(), f.Presentation.Close())
}

var legacyEnvelopeFields = migrationKnownFieldSet{
	"seq",
	"timestamp",
	"kind",
	"step_id",
	"payload",
}

func decodeLegacyEventV0(
	source io.ReaderAt,
	start int64,
	end int64,
	ledger *migrationResourceLedger,
) (legacyEventV0DecodeResult, error) {
	scanner, err := newMigrationJSONScanner(source, start, end, ledger)
	if err != nil {
		return legacyEventV0DecodeResult{}, err
	}
	envelope, err := scanner.ScanObject(legacyEnvelopeFields)
	closeErr := scanner.Close()
	if err != nil {
		return legacyEventV0DecodeResult{}, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return legacyEventV0DecodeResult{}, closeErr
	}
	seqRange, seqPresent := envelope.Value(0)
	timestampRange, timestampPresent := envelope.Value(1)
	kindRange, kindPresent := envelope.Value(2)
	stepRange, stepPresent := envelope.Value(3)
	payloadRange, payloadPresent := envelope.Value(4)
	if !seqPresent || !timestampPresent || !kindPresent || !payloadPresent {
		return legacyEventV0DecodeResult{}, fmt.Errorf("legacy event envelope is missing a required field")
	}
	var sequence int64
	if err := decodeLegacyJSONRange(source, seqRange, &sequence); err != nil {
		return legacyEventV0DecodeResult{}, fmt.Errorf("decode legacy event sequence: %w", err)
	}
	var timestamp time.Time
	if err := decodeLegacyJSONRange(source, timestampRange, &timestamp); err != nil || timestamp.IsZero() {
		return legacyEventV0DecodeResult{}, fmt.Errorf("decode legacy event timestamp: %w", err)
	}
	var kind string
	if err := decodeLegacyJSONRange(source, kindRange, &kind); err != nil {
		return legacyEventV0DecodeResult{}, fmt.Errorf("decode legacy event kind: %w", err)
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return legacyEventV0DecodeResult{}, fmt.Errorf("legacy event kind is required")
	}
	var stepID *string
	if stepPresent {
		var decodedStepID string
		if err := decodeLegacyJSONRange(source, stepRange, &decodedStepID); err != nil {
			return legacyEventV0DecodeResult{}, fmt.Errorf("decode legacy event step identity: %w", err)
		}
		if strings.TrimSpace(decodedStepID) != "" {
			stepID = &decodedStepID
		}
	}

	var payload EventRecordPayload
	result := legacyEventV0DecodeResult{Sequence: sequence}
	switch kind {
	case string(EventKindMessage):
		payload, err = decodeLegacyMessageV0(source, payloadRange)
	case string(EventKindLocalEntry):
		payload, err = decodeLegacyLocalEntryV0(source, payloadRange)
	case string(EventKindToolCompletion):
		var completion *ToolCompletionRecord
		var fallback *legacyToolCompletionFallback
		var class legacyToolSnapshotClass
		completion, fallback, class, err = decodeLegacyToolCompletionV0(
			source,
			payloadRange,
			sequence,
			ledger,
		)
		result.SnapshotClass = class
		if err != nil {
			return result, err
		}
		if fallback != nil {
			fallback.Sequence = sequence
			fallback.StepID = cloneOptionalString(stepID)
			result.FallbackCompletion = fallback
			return result, nil
		}
		payload = *completion
	case string(EventKindHistoryReplace):
		var dropped bool
		payload, dropped, err = decodeLegacyHistoryReplacementV0(
			source,
			payloadRange,
			ledger,
		)
		if dropped {
			result.Dropped = true
			return result, err
		}
	case string(EventKindCacheRequest):
		payload, err = decodeLegacyCacheRequestV0(source, payloadRange)
	case string(EventKindCacheResponse):
		payload, err = decodeLegacyCacheResponseV0(source, payloadRange)
	case string(EventKindCacheWarning):
		payload, err = decodeLegacyCacheWarningV0(source, payloadRange)
	default:
		result.Dropped = true
		return result, nil
	}
	if err != nil {
		return legacyEventV0DecodeResult{}, err
	}
	record, err := NewEventRecord(sequence, stepID, payload)
	if err != nil {
		return legacyEventV0DecodeResult{}, fmt.Errorf("canonicalize legacy %s event: %w", kind, err)
	}
	result.Record = &record
	return result, nil
}

type legacyMessageV0 struct {
	Role                 MessageRole               `json:"role"`
	MessageType          MessageType               `json:"message_type,omitempty"`
	SourcePath           string                    `json:"source_path,omitempty"`
	WorktreeContext      *WorktreeContext          `json:"worktree_context,omitempty"`
	Content              string                    `json:"content,omitempty"`
	CompactContent       string                    `json:"compact_content,omitempty"`
	Name                 string                    `json:"name,omitempty"`
	ToolCallID           string                    `json:"tool_call_id,omitempty"`
	Phase                MessagePhase              `json:"phase,omitempty"`
	BackgroundActivityID string                    `json:"background_activity_id,omitempty"`
	BackgroundExitCode   *int                      `json:"background_exit_code,omitempty"`
	ToolCalls            []legacyMessageToolCallV0 `json:"tool_calls,omitempty"`
	ReasoningItems       []MessageReasoningRecord  `json:"reasoning_items,omitempty"`
}

type legacyMessageToolCallV0 struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Presentation json.RawMessage `json:"presentation,omitempty"`
	Input        json.RawMessage `json:"input"`
	Custom       bool            `json:"custom,omitempty"`
	CustomInput  string          `json:"custom_input,omitempty"`
}

func decodeLegacyMessageV0(
	source io.ReaderAt,
	payloadRange migrationJSONValueRange,
) (MessageRecord, error) {
	var legacy legacyMessageV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return MessageRecord{}, fmt.Errorf("decode legacy message payload: %w", err)
	}
	record := MessageRecord{
		Role:                 legacy.Role,
		MessageType:          optionalLegacyMessageType(legacy.MessageType),
		SourcePath:           optionalLegacyText(legacy.SourcePath),
		WorktreeContext:      CloneWorktreeContext(legacy.WorktreeContext),
		Content:              optionalLegacyText(legacy.Content),
		CompactContent:       optionalLegacyText(legacy.CompactContent),
		Name:                 optionalLegacyText(legacy.Name),
		ToolCallID:           optionalLegacyText(legacy.ToolCallID),
		Phase:                optionalLegacyMessagePhase(legacy.Phase),
		BackgroundActivityID: optionalLegacyText(legacy.BackgroundActivityID),
		BackgroundExitCode:   cloneOptionalInt(legacy.BackgroundExitCode),
	}
	if len(legacy.ToolCalls) > 0 {
		record.ToolCalls = make([]MessageToolCallRecord, 0, len(legacy.ToolCalls))
		for _, call := range legacy.ToolCalls {
			input := append(json.RawMessage(nil), call.Input...)
			kind := ToolCallKindFunction
			if call.Custom {
				kind = ToolCallKindCustom
			}
			record.ToolCalls = append(record.ToolCalls, MessageToolCallRecord{
				CallID:       call.ID,
				Name:         call.Name,
				Kind:         kind,
				Presentation: append(json.RawMessage(nil), call.Presentation...),
				Input:        input,
				CustomInput:  optionalLegacyText(call.CustomInput),
			})
		}
	}
	if len(legacy.ReasoningItems) > 0 {
		record.ReasoningItems = append([]MessageReasoningRecord(nil), legacy.ReasoningItems...)
	}
	return record, nil
}

type legacyLocalEntryV0 struct {
	Visibility      EntryVisibility `json:"visibility,omitempty"`
	Role            string          `json:"role"`
	Text            string          `json:"text"`
	CondensedText   string          `json:"condensed_text,omitempty"`
	DiagnosticKey   string          `json:"diagnostic_key,omitempty"`
	NoticeID        string          `json:"notice_id,omitempty"`
	AfterToolCallID *string         `json:"after_tool_call_id,omitempty"`
}

func decodeLegacyLocalEntryV0(source io.ReaderAt, payloadRange migrationJSONValueRange) (LocalEntryRecord, error) {
	var legacy legacyLocalEntryV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return LocalEntryRecord{}, fmt.Errorf("decode legacy local entry: %w", err)
	}
	visibility := legacy.Visibility
	switch strings.ToLower(strings.TrimSpace(string(visibility))) {
	case "", "auto":
		visibility = EntryVisibilityAuto
	case "o", "ongoing", "all":
		visibility = EntryVisibilityOngoing
	case "oc", "ongoing_collapsed":
		visibility = EntryVisibilityOngoingCollapsed
	case "d", "detail", "verbose":
		visibility = EntryVisibilityDetail
	case "x", "hidden":
		visibility = EntryVisibilityHidden
	}
	return LocalEntryRecord{
		Visibility:      visibility,
		Role:            legacy.Role,
		Text:            legacy.Text,
		CondensedText:   optionalLegacyText(legacy.CondensedText),
		DiagnosticKey:   optionalLegacyText(legacy.DiagnosticKey),
		NoticeID:        optionalLegacyText(legacy.NoticeID),
		AfterToolCallID: cloneOptionalString(legacy.AfterToolCallID),
	}, nil
}

type legacyProviderItemV0 struct {
	Type                 ProviderHistoryItemType         `json:"type"`
	Role                 MessageRole                     `json:"role,omitempty"`
	MessageType          MessageType                     `json:"message_type,omitempty"`
	SourcePath           string                          `json:"source_path,omitempty"`
	WorktreeContext      *WorktreeContext                `json:"worktree_context,omitempty"`
	Phase                MessagePhase                    `json:"phase,omitempty"`
	ID                   string                          `json:"id,omitempty"`
	Name                 string                          `json:"name,omitempty"`
	CallID               string                          `json:"call_id,omitempty"`
	Content              string                          `json:"content,omitempty"`
	CompactContent       string                          `json:"compact_content,omitempty"`
	BackgroundActivityID string                          `json:"background_activity_id,omitempty"`
	BackgroundExitCode   *int                            `json:"background_exit_code,omitempty"`
	ToolPresentation     json.RawMessage                 `json:"tool_presentation,omitempty"`
	Arguments            json.RawMessage                 `json:"arguments,omitempty"`
	CustomInput          string                          `json:"custom_input,omitempty"`
	Output               json.RawMessage                 `json:"output,omitempty"`
	ReasoningSummary     []ProviderHistoryReasoningEntry `json:"reasoning_summary,omitempty"`
	EncryptedContent     string                          `json:"encrypted_content,omitempty"`
	Raw                  json.RawMessage                 `json:"raw,omitempty"`
	LinkedCallID         string                          `json:"linked_call_id,omitempty"`
	LinkKind             ProviderItemLinkKind            `json:"link_kind,omitempty"`
}

type legacyToolCompletionV0 struct {
	CallID        string                  `json:"call_id"`
	Name          string                  `json:"name"`
	IsError       *bool                   `json:"is_error"`
	Output        json.RawMessage         `json:"output"`
	Summary       string                  `json:"summary,omitempty"`
	CondensedText string                  `json:"condensed_text,omitempty"`
	Presentation  json.RawMessage         `json:"presentation,omitempty"`
	ProviderItems *[]legacyProviderItemV0 `json:"provider_items,omitempty"`
}

var legacyToolCompletionFields = migrationKnownFieldSet{
	"call_id",
	"name",
	"is_error",
	"output",
	"summary",
	"condensed_text",
	"presentation",
	"provider_items",
}

var legacyProviderItemRawField = migrationKnownFieldSet{"raw"}

func decodeLegacyToolCompletionV0(
	source io.ReaderAt,
	payloadRange migrationJSONValueRange,
	sequence int64,
	ledger *migrationResourceLedger,
) (*ToolCompletionRecord, *legacyToolCompletionFallback, legacyToolSnapshotClass, error) {
	fields, err := scanLegacyObjectRange(
		source,
		payloadRange,
		ledger,
		legacyToolCompletionFields,
	)
	if err != nil {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf(
			"scan legacy tool completion: %w",
			err,
		)
	}
	providerItemsRange, providerItemsPresent := fields.Value(7)
	providerItemsNull := false
	if providerItemsPresent {
		providerItemsNull, err = legacyJSONRangeIsNull(source, providerItemsRange)
		if err != nil {
			return nil, nil, legacyToolSnapshotNone, fmt.Errorf(
				"inspect legacy tool completion provider snapshot: %w",
				err,
			)
		}
	}
	if !providerItemsPresent || providerItemsNull {
		fallback, fallbackErr := decodeLegacyToolCompletionFallbackV0(
			source,
			fields,
			ledger,
		)
		if fallbackErr != nil {
			return nil, nil, legacyToolSnapshotAbsent, fallbackErr
		}
		return nil, fallback, legacyToolSnapshotAbsent, nil
	}
	var legacy legacyToolCompletionV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf("decode legacy tool completion: %w", err)
	}
	if legacy.IsError == nil {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf(
			"legacy tool completion is_error is required",
		)
	}
	if len(*legacy.ProviderItems) == 0 {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf(
			"legacy tool completion provider snapshot is present but empty",
		)
	}
	record := ToolCompletionRecord{
		CallID: legacy.CallID, Name: legacy.Name, IsError: *legacy.IsError,
		Output:  append(json.RawMessage(nil), legacy.Output...),
		Summary: optionalLegacyText(legacy.Summary), CondensedText: optionalLegacyText(legacy.CondensedText),
		Presentation: append(json.RawMessage(nil), legacy.Presentation...),
	}
	class := legacyToolSnapshotAuthoritative
	var outputKind *ToolOutputKind
	scannedItemCount := 0
	err = scanLegacyObjectArrayRange(
		source,
		providerItemsRange,
		ledger,
		legacyProviderItemRawField,
		func(index int, itemFields migrationScannedObject) error {
			if index >= len(*legacy.ProviderItems) {
				return fmt.Errorf(
					"legacy tool completion provider snapshot count changed during decode",
				)
			}
			rawRange, rawPresent := itemFields.Value(0)
			if rawPresent {
				raw, copyErr := copyLegacyLexicalJSONRange(source, rawRange, ledger)
				if copyErr != nil {
					return fmt.Errorf(
						"copy legacy tool completion provider item %d Raw: %w",
						index,
						copyErr,
					)
				}
				(*legacy.ProviderItems)[index].Raw = raw
			}
			scannedItemCount++
			return nil
		},
	)
	if err != nil {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf(
			"scan legacy tool completion provider snapshot: %w",
			err,
		)
	}
	if scannedItemCount != len(*legacy.ProviderItems) {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf(
			"legacy tool completion provider snapshot count changed during decode",
		)
	}
	for index, item := range *legacy.ProviderItems {
		snapshot := ToolCompletionProviderItem{
			Type: ProviderInputItemType(item.Type), Name: optionalLegacyText(item.Name),
			CallID: optionalLegacyText(item.CallID), Raw: append(json.RawMessage(nil), item.Raw...),
			LinkedCallID: optionalLegacyText(item.LinkedCallID),
		}
		if item.LinkKind != "" {
			linkKind := item.LinkKind
			snapshot.LinkKind = &linkKind
		}
		if len(snapshot.Raw) == 0 {
			switch snapshot.Type {
			case ProviderInputItemTypeFunctionCallOutput, ProviderInputItemTypeCustomToolOutput:
				class = legacyToolSnapshotGeneratedRaw
			default:
				return nil, nil, legacyToolSnapshotUnsupportedMissingRaw, legacyToolSnapshotError{
					Sequence:  sequence,
					ItemIndex: index,
					Type:      item.Type,
					Reason:    ToolCompletionProviderItemMissingRaw,
				}
			}
		}
		switch snapshot.Type {
		case ProviderInputItemTypeFunctionCallOutput, ProviderInputItemTypeCustomToolOutput:
			kind := ToolOutputKindFunction
			if snapshot.Type == ProviderInputItemTypeCustomToolOutput {
				kind = ToolOutputKindCustom
			}
			if outputKind != nil && *outputKind != kind {
				return nil, nil, class, legacyToolSnapshotError{
					Sequence:  sequence,
					ItemIndex: index,
					Type:      item.Type,
					Reason:    ToolCompletionProviderItemInvalidFacts,
				}
			}
			outputKind = &kind
			if len(snapshot.Raw) == 0 {
				if len(item.Output) == 0 || snapshot.CallID == nil {
					return nil, nil, class, legacyToolSnapshotError{
						Sequence:  sequence,
						ItemIndex: index,
						Type:      item.Type,
						Reason:    ToolCompletionProviderItemInvalidFacts,
					}
				}
				raw, rawErr := encodeMissingProviderOutputRaw(snapshot.Type, *snapshot.CallID, item.Output)
				if rawErr != nil {
					return nil, nil, class, errors.Join(
						legacyToolSnapshotError{
							Sequence:  sequence,
							ItemIndex: index,
							Type:      item.Type,
							Reason:    ToolCompletionProviderItemInvalidFacts,
						},
						rawErr,
					)
				}
				snapshot.Raw = raw
			}
		case ProviderInputItemTypeOther:
		default:
			return nil, nil, legacyToolSnapshotNone, legacyToolSnapshotError{
				Sequence:  sequence,
				ItemIndex: index,
				Type:      item.Type,
				Reason:    ToolCompletionProviderItemUnsupportedType,
			}
		}
		record.ProviderItems = append(record.ProviderItems, snapshot)
	}
	if outputKind == nil {
		return nil, nil, legacyToolSnapshotNone, fmt.Errorf("legacy provider snapshot has no output item")
	}
	record.OutputKind = *outputKind
	return &record, nil, class, nil
}

func decodeLegacyToolCompletionFallbackV0(
	source io.ReaderAt,
	fields migrationScannedObject,
	ledger *migrationResourceLedger,
) (_ *legacyToolCompletionFallback, resultErr error) {
	callIDRange, callIDPresent := fields.Value(0)
	nameRange, namePresent := fields.Value(1)
	isErrorRange, isErrorPresent := fields.Value(2)
	outputRange, outputPresent := fields.Value(3)
	if !callIDPresent || !namePresent || !isErrorPresent || !outputPresent {
		return nil, fmt.Errorf("legacy tool completion fallback is missing a required field")
	}
	var callID string
	if err := decodeLegacyJSONRange(source, callIDRange, &callID); err != nil {
		return nil, fmt.Errorf("decode legacy tool completion call identity: %w", err)
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, fmt.Errorf("canonicalize legacy tool completion fallback: call identity is required")
	}
	var name string
	if err := decodeLegacyJSONRange(source, nameRange, &name); err != nil {
		return nil, fmt.Errorf("decode legacy tool completion name: %w", err)
	}
	name = strings.TrimSpace(name)
	var isError bool
	if err := decodeLegacyJSONRange(source, isErrorRange, &isError); err != nil {
		return nil, fmt.Errorf("decode legacy tool completion error fact: %w", err)
	}
	decodeOptionalText := func(slot int, label string) (*string, error) {
		valueRange, present := fields.Value(slot)
		if !present {
			return nil, nil
		}
		var value string
		if err := decodeLegacyJSONRange(source, valueRange, &value); err != nil {
			return nil, fmt.Errorf("decode legacy tool completion %s: %w", label, err)
		}
		normalized, err := normalizeOptionalEventText(label, &value)
		if err != nil {
			return nil, fmt.Errorf("canonicalize legacy tool completion fallback: %w", err)
		}
		return normalized, nil
	}
	summary, err := decodeOptionalText(4, "summary")
	if err != nil {
		return nil, err
	}
	condensedText, err := decodeOptionalText(5, "condensed text")
	if err != nil {
		return nil, err
	}
	fallback := &legacyToolCompletionFallback{
		CallID:        callID,
		Name:          name,
		IsError:       isError,
		Output:        (&migrationValueStore{source: source, ledger: ledger}).Lexical(outputRange),
		Summary:       summary,
		CondensedText: condensedText,
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, fallback.Close())
		}
	}()
	if presentationRange, present := fields.Value(6); present {
		fallback.Presentation = (&migrationValueStore{
			source: source,
			ledger: ledger,
		}).Lexical(presentationRange)
	}
	return fallback, nil
}

func legacyJSONRangeIsNull(
	source io.ReaderAt,
	valueRange migrationJSONValueRange,
) (bool, error) {
	if valueRange.Size() != int64(len("null")) {
		return false, nil
	}
	var value [4]byte
	if _, err := source.ReadAt(value[:], valueRange.Start); err != nil {
		return false, err
	}
	return string(value[:]) == "null", nil
}

type legacyHistoryReplacementV0 struct {
	Engine                            string                           `json:"engine"`
	Mode                              CompactionMode                   `json:"mode"`
	WorkflowRunID                     string                           `json:"workflow_run_id,omitempty"`
	CompactionNumber                  int                              `json:"compaction_number,omitempty"`
	CommittedEntryStart               *int                             `json:"committed_entry_start,omitempty"`
	PendingHandoffFutureMessage       string                           `json:"pending_handoff_future_message,omitempty"`
	LastCommittedAssistantFinalAnswer string                           `json:"last_committed_assistant_final_answer,omitempty"`
	LatestRollbackCandidate           *rollbacktarget.CandidateLocator `json:"latest_rollback_candidate,omitempty"`
	Items                             *[]legacyProviderItemV0          `json:"items"`
}

func decodeLegacyHistoryReplacementV0(
	source io.ReaderAt,
	payloadRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) (EventRecordPayload, bool, error) {
	fields, err := scanLegacyObjectRange(
		source,
		payloadRange,
		ledger,
		legacyHistoryReplacementFields,
	)
	if err != nil {
		return nil, false, fmt.Errorf("scan legacy history replacement: %w", err)
	}
	var legacy legacyHistoryReplacementV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return nil, false, fmt.Errorf("decode legacy history replacement: %w", err)
	}
	if IsLegacyReviewerRollbackHistoryReplacementEngine(legacy.Engine) {
		return nil, true, nil
	}
	if legacy.Items == nil {
		return nil, false, fmt.Errorf("legacy history replacement items are required")
	}
	itemsRange, itemsPresent := fields.Value(8)
	if !itemsPresent {
		return nil, false, fmt.Errorf(
			"legacy history replacement items presence is inconsistent",
		)
	}
	record := HistoryReplacementRecord{
		Engine: legacy.Engine, Mode: legacy.Mode,
		WorkflowRunID:                     optionalLegacyText(legacy.WorkflowRunID),
		CommittedEntryStart:               cloneOptionalInt(legacy.CommittedEntryStart),
		PendingHandoffFutureMessage:       optionalLegacyText(legacy.PendingHandoffFutureMessage),
		LastCommittedAssistantFinalAnswer: optionalLegacyText(legacy.LastCommittedAssistantFinalAnswer),
		LatestRollbackCandidate:           cloneRollbackCandidate(legacy.LatestRollbackCandidate),
	}
	if legacy.CompactionNumber != 0 {
		record.CompactionNumber = &legacy.CompactionNumber
	}
	scannedItemCount := 0
	err = scanLegacyObjectArrayRange(
		source,
		itemsRange,
		ledger,
		legacyProviderItemRawField,
		func(index int, itemFields migrationScannedObject) error {
			if index >= len(*legacy.Items) {
				return fmt.Errorf(
					"legacy history replacement item count changed during decode",
				)
			}
			rawRange, rawPresent := itemFields.Value(0)
			if rawPresent {
				raw, copyErr := copyLegacyLexicalJSONRange(source, rawRange, ledger)
				if copyErr != nil {
					return fmt.Errorf(
						"copy legacy history replacement item %d Raw: %w",
						index,
						copyErr,
					)
				}
				(*legacy.Items)[index].Raw = raw
			}
			scannedItemCount++
			return nil
		},
	)
	if err != nil {
		return nil, false, fmt.Errorf("scan legacy history replacement items: %w", err)
	}
	if scannedItemCount != len(*legacy.Items) {
		return nil, false, fmt.Errorf(
			"legacy history replacement item count changed during decode",
		)
	}
	for _, item := range *legacy.Items {
		historyItem := ProviderHistoryItem{
			Type: item.Type, Role: optionalLegacyRole(item.Role), MessageType: optionalLegacyMessageType(item.MessageType),
			SourcePath: optionalLegacyText(item.SourcePath), WorktreeContext: CloneWorktreeContext(item.WorktreeContext),
			Phase: optionalLegacyMessagePhase(item.Phase), ID: optionalLegacyText(item.ID), Name: optionalLegacyText(item.Name),
			CallID: optionalLegacyText(item.CallID), Content: optionalLegacyText(item.Content),
			CompactContent:       optionalLegacyText(item.CompactContent),
			BackgroundActivityID: optionalLegacyText(item.BackgroundActivityID),
			BackgroundExitCode:   cloneOptionalInt(item.BackgroundExitCode),
			ToolPresentation:     append(json.RawMessage(nil), item.ToolPresentation...),
			Arguments:            append(json.RawMessage(nil), item.Arguments...), CustomInput: optionalLegacyText(item.CustomInput),
			Output: append(json.RawMessage(nil), item.Output...), ReasoningSummary: append([]ProviderHistoryReasoningEntry(nil), item.ReasoningSummary...),
			EncryptedContent: optionalLegacyText(item.EncryptedContent), Raw: append(json.RawMessage(nil), item.Raw...),
			LinkedCallID: optionalLegacyText(item.LinkedCallID),
		}
		if item.LinkKind != "" {
			linkKind := item.LinkKind
			historyItem.LinkKind = &linkKind
		}
		record.Items = append(record.Items, historyItem)
	}
	return record, false, nil
}

var legacyHistoryReplacementFields = migrationKnownFieldSet{
	"engine",
	"mode",
	"workflow_run_id",
	"compaction_number",
	"committed_entry_start",
	"pending_handoff_future_message",
	"last_committed_assistant_final_answer",
	"latest_rollback_candidate",
	"items",
}

type legacyCacheRequestV0 struct {
	DigestVersion int        `json:"digest_version,omitempty"`
	CacheKey      string     `json:"cache_key"`
	Scope         CacheScope `json:"scope,omitempty"`
	ChunkCount    int        `json:"chunk_count"`
	TerminalHash  string     `json:"terminal_hash"`
}

func decodeLegacyCacheRequestV0(source io.ReaderAt, payloadRange migrationJSONValueRange) (CacheRequestObservationRecord, error) {
	var legacy legacyCacheRequestV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return CacheRequestObservationRecord{}, err
	}
	normalizeLegacyCacheFacts(&legacy.DigestVersion, &legacy.Scope)
	return CacheRequestObservationRecord(legacy), nil
}

type legacyCacheResponseV0 struct {
	DigestVersion        int        `json:"digest_version,omitempty"`
	CacheKey             string     `json:"cache_key"`
	Scope                CacheScope `json:"scope,omitempty"`
	ChunkCount           int        `json:"chunk_count"`
	TerminalHash         string     `json:"terminal_hash"`
	HasCachedInputTokens bool       `json:"has_cached_input_tokens,omitempty"`
	CachedInputTokens    int        `json:"cached_input_tokens,omitempty"`
}

func decodeLegacyCacheResponseV0(source io.ReaderAt, payloadRange migrationJSONValueRange) (CacheResponseObservationRecord, error) {
	var legacy legacyCacheResponseV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return CacheResponseObservationRecord{}, err
	}
	normalizeLegacyCacheFacts(&legacy.DigestVersion, &legacy.Scope)
	record := CacheResponseObservationRecord{
		DigestVersion: legacy.DigestVersion, CacheKey: legacy.CacheKey, Scope: legacy.Scope,
		ChunkCount: legacy.ChunkCount, TerminalHash: legacy.TerminalHash,
	}
	if legacy.HasCachedInputTokens {
		record.CachedInputTokens = &legacy.CachedInputTokens
	}
	return record, nil
}

type legacyCacheWarningV0 struct {
	Scope           CacheScope         `json:"scope,omitempty"`
	Reason          CacheWarningReason `json:"reason"`
	CacheKey        string             `json:"cache_key,omitempty"`
	LostInputTokens int                `json:"lost_input_tokens,omitempty"`
}

func decodeLegacyCacheWarningV0(source io.ReaderAt, payloadRange migrationJSONValueRange) (CacheWarningRecord, error) {
	var legacy legacyCacheWarningV0
	if err := decodeLegacyJSONRange(source, payloadRange, &legacy); err != nil {
		return CacheWarningRecord{}, err
	}
	if legacy.Scope == "" {
		legacy.Scope = CacheScopeConversation
	}
	record := CacheWarningRecord{Scope: legacy.Scope, Reason: legacy.Reason, CacheKey: optionalLegacyText(legacy.CacheKey)}
	if legacy.LostInputTokens != 0 {
		record.LostInputTokens = &legacy.LostInputTokens
	}
	return record, nil
}

func normalizeLegacyCacheFacts(version *int, scope *CacheScope) {
	if *version == 0 {
		*version = CacheDigestV1
	}
	if *scope == "" {
		*scope = CacheScopeConversation
	}
}

func decodeLegacyJSONRange(
	source io.ReaderAt,
	valueRange migrationJSONValueRange,
	target any,
) error {
	decoder := json.NewDecoder(io.NewSectionReader(
		source,
		valueRange.Start,
		valueRange.Size(),
	))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("legacy JSON value contains trailing data")
		}
		return err
	}
	return nil
}

func scanLegacyObjectRange(
	source io.ReaderAt,
	valueRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
	fields migrationKnownFieldSet,
) (migrationScannedObject, error) {
	scanner, err := newMigrationJSONScanner(
		source,
		valueRange.Start,
		valueRange.End,
		ledger,
	)
	if err != nil {
		return migrationScannedObject{}, err
	}
	result, scanErr := scanner.ScanObject(fields)
	return result, errors.Join(scanErr, scanner.Close())
}

func scanLegacyObjectArrayRange(
	source io.ReaderAt,
	valueRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
	fields migrationKnownFieldSet,
	visit func(index int, object migrationScannedObject) error,
) error {
	scanner, err := newMigrationJSONScanner(
		source,
		valueRange.Start,
		valueRange.End,
		ledger,
	)
	if err != nil {
		return err
	}
	scanErr := scanner.ScanObjectArray(fields, visit)
	return errors.Join(scanErr, scanner.Close())
}

func copyLegacyLexicalJSONRange(
	source io.ReaderAt,
	valueRange migrationJSONValueRange,
	ledger *migrationResourceLedger,
) (json.RawMessage, error) {
	var buffer bytes.Buffer
	if err := copyMigrationRangeWithBuffer(
		&buffer,
		source,
		valueRange,
		ledger,
	); err != nil {
		return nil, err
	}
	return json.RawMessage(buffer.Bytes()), nil
}

func optionalLegacyText(value string) *string {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func optionalLegacyMessageType(value MessageType) *MessageType {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func optionalLegacyMessagePhase(value MessagePhase) *MessagePhase {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func optionalLegacyRole(value MessageRole) *MessageRole {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func cloneRollbackCandidate(
	value *rollbacktarget.CandidateLocator,
) *rollbacktarget.CandidateLocator {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
