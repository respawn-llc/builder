package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"core/shared/rollbacktarget"
)

type legacyEventV0 struct {
	Seq       int64           `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`
	StepID    string          `json:"step_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
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

type legacySequenceNormalizer struct {
	initialized        bool
	previousNormalized int64
	cumulativeOffset   int64
}

type legacyCallDefinition struct {
	custom bool
	name   string
}

type legacyMigrationState struct {
	calls map[string]legacyCallDefinition
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

type legacyMigrationOutput struct {
	destination             *os.File
	bytesWritten            int64
	latestRollbackCandidate *rollbacktarget.CandidateLocator
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

type legacyCacheRequestV0 struct {
	DigestVersion int        `json:"digest_version,omitempty"`
	CacheKey      string     `json:"cache_key"`
	Scope         CacheScope `json:"scope,omitempty"`
	ChunkCount    int        `json:"chunk_count"`
	TerminalHash  string     `json:"terminal_hash"`
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

type legacyCacheWarningV0 struct {
	Scope           CacheScope         `json:"scope,omitempty"`
	Reason          CacheWarningReason `json:"reason"`
	CacheKey        string             `json:"cache_key,omitempty"`
	LostInputTokens int                `json:"lost_input_tokens,omitempty"`
}

func installLegacyCurrentEventLog(
	eventsPath string,
	workspace string,
	onCommitted func(),
) error {
	return installCurrentEventLog(eventsPath, workspace, onCommitted, func(stage *os.File) error {
		source, err := openRegularSessionFile(eventsPath, "legacy session event log")
		if err != nil {
			return fmt.Errorf("open legacy session event log: %w", err)
		}
		transformErr := transformLegacyEventLogV0(source, stage)
		closeErr := source.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close legacy session event log: %w", closeErr)
		}
		return errors.Join(eventLogContractErrorOrNil(transformErr), closeErr)
	})
}

func eventLogContractErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return eventLogContractError{Err: err}
}

func transformLegacyEventLogV0(source io.Reader, destination *os.File) error {
	output := legacyMigrationOutput{destination: destination}
	if err := output.writeHeader(); err != nil {
		return err
	}

	decoder := json.NewDecoder(source)
	var normalizer legacySequenceNormalizer
	state := legacyMigrationState{calls: make(map[string]legacyCallDefinition)}
	for {
		var legacy legacyEventV0
		err := decoder.Decode(&legacy)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode legacy event: %w", err)
		}
		sequence, err := normalizer.Normalize(legacy.Seq)
		if err != nil {
			return err
		}
		record, dropped, err := state.decodeEvent(legacy, sequence)
		if err != nil {
			return err
		}
		if dropped {
			continue
		}
		record, err = output.writeRecord(record)
		if err != nil {
			return err
		}
		if err := state.observeRecord(record); err != nil {
			return err
		}
	}
}

func (s *legacyMigrationState) decodeEvent(
	legacy legacyEventV0,
	sequence int64,
) (EventRecord, bool, error) {
	if legacy.Timestamp.IsZero() {
		return EventRecord{}, false, errors.New("legacy event timestamp is required")
	}
	kind := strings.TrimSpace(legacy.Kind)
	if kind == "" {
		return EventRecord{}, false, errors.New("legacy event kind is required")
	}
	if len(legacy.Payload) == 0 {
		return EventRecord{}, false, errors.New("legacy event payload is required")
	}
	var payload EventRecordPayload
	switch kind {
	case string(EventKindMessage):
		message, err := decodeLegacyMessageV0(legacy.Payload)
		if err != nil {
			return EventRecord{}, false, err
		}
		payload = message
	case string(EventKindToolCompletion):
		completion, err := s.decodeToolCompletion(legacy.Payload)
		if err != nil {
			return EventRecord{}, false, err
		}
		payload = completion
	case string(EventKindLocalEntry):
		local, err := decodeLegacyLocalEntryV0(legacy.Payload)
		if err != nil {
			return EventRecord{}, false, err
		}
		payload = local
	case string(EventKindCacheRequest):
		request, err := decodeLegacyCacheRequestV0(legacy.Payload)
		if err != nil {
			return EventRecord{}, false, err
		}
		payload = request
	case string(EventKindCacheResponse):
		response, err := decodeLegacyCacheResponseV0(legacy.Payload)
		if err != nil {
			return EventRecord{}, false, err
		}
		payload = response
	case string(EventKindCacheWarning):
		warning, err := decodeLegacyCacheWarningV0(legacy.Payload)
		if err != nil {
			return EventRecord{}, false, err
		}
		payload = warning
	case string(EventKindHistoryReplace):
		history, dropped, err := decodeLegacyHistoryReplacementV0(legacy.Payload)
		if err != nil || dropped {
			return EventRecord{}, dropped, err
		}
		payload = history
	default:
		return EventRecord{}, true, nil
	}
	var stepID *string
	if strings.TrimSpace(legacy.StepID) != "" {
		stepID = &legacy.StepID
	}
	record, err := NewEventRecord(sequence, stepID, payload)
	if err != nil {
		return EventRecord{}, false, fmt.Errorf("canonicalize legacy %s event: %w", kind, err)
	}
	return record, false, nil
}

func decodeLegacyLocalEntryV0(payload json.RawMessage) (LocalEntryRecord, error) {
	var legacy legacyLocalEntryV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
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
		CondensedText:   optionalLegacyString(legacy.CondensedText),
		DiagnosticKey:   optionalLegacyString(legacy.DiagnosticKey),
		NoticeID:        optionalLegacyString(legacy.NoticeID),
		AfterToolCallID: cloneOptionalLegacyValue(legacy.AfterToolCallID),
	}, nil
}

func decodeLegacyCacheRequestV0(payload json.RawMessage) (CacheRequestObservationRecord, error) {
	var legacy legacyCacheRequestV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return CacheRequestObservationRecord{}, fmt.Errorf("decode legacy cache request: %w", err)
	}
	normalizeLegacyCacheFacts(&legacy.DigestVersion, &legacy.Scope)
	return CacheRequestObservationRecord(legacy), nil
}

func decodeLegacyCacheResponseV0(payload json.RawMessage) (CacheResponseObservationRecord, error) {
	var legacy legacyCacheResponseV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return CacheResponseObservationRecord{}, fmt.Errorf("decode legacy cache response: %w", err)
	}
	normalizeLegacyCacheFacts(&legacy.DigestVersion, &legacy.Scope)
	record := CacheResponseObservationRecord{
		DigestVersion: legacy.DigestVersion,
		CacheKey:      legacy.CacheKey,
		Scope:         legacy.Scope,
		ChunkCount:    legacy.ChunkCount,
		TerminalHash:  legacy.TerminalHash,
	}
	if legacy.HasCachedInputTokens {
		record.CachedInputTokens = &legacy.CachedInputTokens
	}
	return record, nil
}

func decodeLegacyCacheWarningV0(payload json.RawMessage) (CacheWarningRecord, error) {
	var legacy legacyCacheWarningV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return CacheWarningRecord{}, fmt.Errorf("decode legacy cache warning: %w", err)
	}
	if legacy.Scope == "" {
		legacy.Scope = CacheScopeConversation
	}
	record := CacheWarningRecord{
		Scope:    legacy.Scope,
		Reason:   legacy.Reason,
		CacheKey: optionalLegacyString(legacy.CacheKey),
	}
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

func decodeLegacyHistoryReplacementV0(
	payload json.RawMessage,
) (HistoryReplacementRecord, bool, error) {
	var discriminator struct {
		Engine string `json:"engine"`
	}
	if err := json.Unmarshal(payload, &discriminator); err != nil {
		return HistoryReplacementRecord{}, false, fmt.Errorf(
			"decode legacy history replacement discriminator: %w",
			err,
		)
	}
	if IsLegacyReviewerRollbackHistoryReplacementEngine(discriminator.Engine) {
		return HistoryReplacementRecord{}, true, nil
	}

	var legacy legacyHistoryReplacementV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return HistoryReplacementRecord{}, false, fmt.Errorf(
			"decode legacy history replacement: %w",
			err,
		)
	}
	if legacy.Items == nil {
		return HistoryReplacementRecord{}, false, errors.New(
			"legacy history replacement items are required",
		)
	}
	record := HistoryReplacementRecord{
		Engine:                            legacy.Engine,
		Mode:                              legacy.Mode,
		CommittedEntryStart:               cloneOptionalLegacyValue(legacy.CommittedEntryStart),
		PendingHandoffFutureMessage:       optionalLegacyString(legacy.PendingHandoffFutureMessage),
		LastCommittedAssistantFinalAnswer: optionalLegacyString(legacy.LastCommittedAssistantFinalAnswer),
		LatestRollbackCandidate:           cloneOptionalLegacyValue(legacy.LatestRollbackCandidate),
	}
	if legacy.CompactionNumber != 0 {
		record.CompactionNumber = &legacy.CompactionNumber
	}
	for _, item := range *legacy.Items {
		record.Items = append(record.Items, legacyProviderHistoryItem(item))
	}
	return record, false, nil
}

func legacyProviderHistoryItem(item legacyProviderItemV0) ProviderHistoryItem {
	record := ProviderHistoryItem{
		Type:                 item.Type,
		Role:                 optionalLegacyValue(item.Role),
		MessageType:          optionalLegacyValue(item.MessageType),
		SourcePath:           optionalLegacyString(item.SourcePath),
		WorktreeContext:      CloneWorktreeContext(item.WorktreeContext),
		Phase:                optionalLegacyValue(item.Phase),
		ID:                   optionalLegacyString(item.ID),
		Name:                 optionalLegacyString(item.Name),
		CallID:               optionalLegacyString(item.CallID),
		Content:              optionalLegacyString(item.Content),
		CompactContent:       optionalLegacyString(item.CompactContent),
		BackgroundActivityID: optionalLegacyString(item.BackgroundActivityID),
		BackgroundExitCode:   cloneOptionalLegacyValue(item.BackgroundExitCode),
		ToolPresentation:     append(json.RawMessage(nil), item.ToolPresentation...),
		Arguments:            append(json.RawMessage(nil), item.Arguments...),
		CustomInput:          optionalLegacyString(item.CustomInput),
		Output:               append(json.RawMessage(nil), item.Output...),
		ReasoningSummary:     append([]ProviderHistoryReasoningEntry(nil), item.ReasoningSummary...),
		EncryptedContent:     optionalLegacyString(item.EncryptedContent),
		Raw:                  append(json.RawMessage(nil), item.Raw...),
		LinkedCallID:         optionalLegacyString(item.LinkedCallID),
	}
	if item.LinkKind != "" {
		linkKind := item.LinkKind
		record.LinkKind = &linkKind
	}
	return record
}

func (s *legacyMigrationState) decodeToolCompletion(
	payload json.RawMessage,
) (ToolCompletionRecord, error) {
	var legacy legacyToolCompletionV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return ToolCompletionRecord{}, fmt.Errorf("decode legacy tool completion: %w", err)
	}
	if legacy.IsError == nil {
		return ToolCompletionRecord{}, errors.New("legacy tool completion is_error is required")
	}
	callID := strings.TrimSpace(legacy.CallID)
	if callID == "" {
		return ToolCompletionRecord{}, errors.New("legacy tool completion call identity is required")
	}
	if legacy.ProviderItems != nil {
		return decodeLegacyToolCompletionSnapshot(legacy)
	}
	definition, found := s.calls[callID]
	name := strings.TrimSpace(legacy.Name)
	if name == "" && found {
		name = definition.name
	}
	if name == "" {
		return ToolCompletionRecord{}, errors.New("legacy tool completion name is required")
	}
	outputKind := ToolOutputKindFunction
	itemType := ProviderInputItemTypeFunctionCallOutput
	if found && definition.custom {
		outputKind = ToolOutputKindCustom
		itemType = ProviderInputItemTypeCustomToolOutput
	}
	raw, err := encodeMissingProviderOutputRaw(itemType, callID, legacy.Output)
	if err != nil {
		return ToolCompletionRecord{}, fmt.Errorf("encode legacy tool completion provider output: %w", err)
	}
	return ToolCompletionRecord{
		CallID:        callID,
		Name:          name,
		OutputKind:    outputKind,
		IsError:       *legacy.IsError,
		Output:        append(json.RawMessage(nil), legacy.Output...),
		Summary:       optionalLegacyString(legacy.Summary),
		CondensedText: optionalLegacyString(legacy.CondensedText),
		Presentation:  append(json.RawMessage(nil), legacy.Presentation...),
		ProviderItems: []ToolCompletionProviderItem{{
			Type:   itemType,
			Name:   optionalLegacyString(name),
			CallID: optionalLegacyString(callID),
			Raw:    raw,
		}},
	}, nil
}

func decodeLegacyToolCompletionSnapshot(
	legacy legacyToolCompletionV0,
) (ToolCompletionRecord, error) {
	if len(*legacy.ProviderItems) == 0 {
		return ToolCompletionRecord{}, errors.New(
			"legacy tool completion provider snapshot is present but empty",
		)
	}
	record := ToolCompletionRecord{
		CallID:        legacy.CallID,
		Name:          legacy.Name,
		IsError:       *legacy.IsError,
		Output:        append(json.RawMessage(nil), legacy.Output...),
		Summary:       optionalLegacyString(legacy.Summary),
		CondensedText: optionalLegacyString(legacy.CondensedText),
		Presentation:  append(json.RawMessage(nil), legacy.Presentation...),
	}
	var outputKind *ToolOutputKind
	for _, item := range *legacy.ProviderItems {
		providerItem := ToolCompletionProviderItem{
			Type:         ProviderInputItemTypeOther,
			Name:         optionalLegacyString(item.Name),
			CallID:       optionalLegacyString(item.CallID),
			Raw:          append(json.RawMessage(nil), item.Raw...),
			LinkedCallID: optionalLegacyString(item.LinkedCallID),
		}
		if item.LinkKind != "" {
			linkKind := item.LinkKind
			providerItem.LinkKind = &linkKind
		}
		switch item.Type {
		case ProviderHistoryItemTypeFunctionCallOutput:
			kind := ToolOutputKindFunction
			outputKind = &kind
			providerItem.Type = ProviderInputItemTypeFunctionCallOutput
		case ProviderHistoryItemTypeCustomToolOutput:
			kind := ToolOutputKindCustom
			outputKind = &kind
			providerItem.Type = ProviderInputItemTypeCustomToolOutput
		}
		if len(providerItem.Raw) == 0 {
			if providerItem.Type == ProviderInputItemTypeOther {
				return ToolCompletionRecord{}, errors.New(
					"legacy tool completion provider item Raw is required",
				)
			}
			output := item.Output
			if len(output) == 0 {
				output = legacy.Output
			}
			raw, err := encodeMissingProviderOutputRaw(
				providerItem.Type,
				legacy.CallID,
				output,
			)
			if err != nil {
				return ToolCompletionRecord{}, err
			}
			providerItem.Raw = raw
		}
		record.ProviderItems = append(record.ProviderItems, providerItem)
	}
	if outputKind == nil {
		return ToolCompletionRecord{}, errors.New(
			"legacy tool completion provider snapshot has no output item",
		)
	}
	record.OutputKind = *outputKind
	return record, nil
}

func (s *legacyMigrationState) observeRecord(record EventRecord) error {
	payload, err := record.Payload()
	if err != nil {
		return err
	}
	switch typed := payload.(type) {
	case MessageRecord:
		s.observeMessageCalls(typed.ToolCalls)
	case HistoryReplacementRecord:
		s.calls = make(map[string]legacyCallDefinition)
		for _, item := range typed.Items {
			custom := false
			switch item.Type {
			case ProviderHistoryItemTypeFunctionCall:
			case ProviderHistoryItemTypeCustomToolCall:
				custom = true
			default:
				continue
			}
			callID := optionalLegacyPointerString(item.CallID)
			if callID == "" {
				callID = optionalLegacyPointerString(item.ID)
			}
			if callID == "" {
				continue
			}
			s.calls[callID] = legacyCallDefinition{
				custom: custom,
				name:   optionalLegacyPointerString(item.Name),
			}
		}
	}
	return nil
}

func (s *legacyMigrationState) observeMessageCalls(calls []MessageToolCallRecord) {
	for _, call := range calls {
		callID := strings.TrimSpace(call.CallID)
		if callID == "" {
			continue
		}
		s.calls[callID] = legacyCallDefinition{
			custom: call.Kind == ToolCallKindCustom,
			name:   strings.TrimSpace(call.Name),
		}
	}
}

func (o *legacyMigrationOutput) writeHeader() error {
	header, err := encodeEventLogHeaderV1()
	if err != nil {
		return err
	}
	return o.write(append(header, '\n'))
}

func (o *legacyMigrationOutput) writeRecord(record EventRecord) (EventRecord, error) {
	payload, err := record.Payload()
	if err != nil {
		return EventRecord{}, err
	}
	if replacement, ok := payload.(HistoryReplacementRecord); ok {
		replacement = rebaseHistoryReplacementRollbackCandidate(
			replacement,
			o.latestRollbackCandidate,
		)
		record, err = NewEventRecord(record.Seq(), record.StepID(), replacement)
		if err != nil {
			return EventRecord{}, fmt.Errorf(
				"rebuild migrated rollback locator for sequence %d: %w",
				record.Seq(),
				err,
			)
		}
	}
	encoded, err := encodeEventRecordV1(record)
	if err != nil {
		return EventRecord{}, fmt.Errorf("encode migrated event %d: %w", record.Seq(), err)
	}
	if err := o.write(append(encoded, '\n')); err != nil {
		return EventRecord{}, fmt.Errorf("write migrated event %d: %w", record.Seq(), err)
	}
	visibleUser, err := isForkVisibleUserMessage(record)
	if err != nil {
		return EventRecord{}, err
	}
	if visibleUser {
		o.latestRollbackCandidate = &rollbacktarget.CandidateLocator{
			UserMessageSeq:       record.Seq(),
			CandidatePageEndByte: o.bytesWritten,
		}
	}
	return record, nil
}

func (o *legacyMigrationOutput) write(payload []byte) error {
	if o == nil || o.destination == nil {
		return errors.New("legacy migration output is required")
	}
	if int64(len(payload)) > math.MaxInt64-o.bytesWritten {
		return fmt.Errorf(
			"legacy migration output offset overflow: offset=%d write=%d",
			o.bytesWritten,
			len(payload),
		)
	}
	written, err := writeAll(o.destination, payload)
	o.bytesWritten += int64(written)
	return err
}

func decodeLegacyMessageV0(payload json.RawMessage) (MessageRecord, error) {
	var legacy legacyMessageV0
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return MessageRecord{}, fmt.Errorf("decode legacy message payload: %w", err)
	}
	record := MessageRecord{
		Role:                 legacy.Role,
		MessageType:          optionalLegacyValue(legacy.MessageType),
		SourcePath:           optionalLegacyString(legacy.SourcePath),
		WorktreeContext:      CloneWorktreeContext(legacy.WorktreeContext),
		Content:              optionalLegacyString(legacy.Content),
		CompactContent:       optionalLegacyString(legacy.CompactContent),
		Name:                 optionalLegacyString(legacy.Name),
		ToolCallID:           optionalLegacyString(legacy.ToolCallID),
		Phase:                optionalLegacyValue(legacy.Phase),
		BackgroundActivityID: optionalLegacyString(legacy.BackgroundActivityID),
		BackgroundExitCode:   cloneOptionalLegacyValue(legacy.BackgroundExitCode),
	}
	if hasPartialBackgroundNoticeIdentity(record) {
		// Legacy background notices stored only a process identifier in name.
		// V1 models background identity as an all-or-nothing activity/process pair.
		record.Name = nil
		record.BackgroundActivityID = nil
	}
	for _, call := range legacy.ToolCalls {
		kind := ToolCallKindFunction
		if call.Custom {
			kind = ToolCallKindCustom
		}
		record.ToolCalls = append(record.ToolCalls, MessageToolCallRecord{
			CallID:       call.ID,
			Name:         call.Name,
			Kind:         kind,
			Presentation: append(json.RawMessage(nil), call.Presentation...),
			Input:        append(json.RawMessage(nil), call.Input...),
			CustomInput:  optionalLegacyString(call.CustomInput),
		})
	}
	record.ReasoningItems = append([]MessageReasoningRecord(nil), legacy.ReasoningItems...)
	return record, nil
}

func (n *legacySequenceNormalizer) Normalize(sequence int64) (int64, error) {
	if sequence <= 0 {
		return 0, fmt.Errorf("legacy event sequence must be positive: %d", sequence)
	}
	if n.cumulativeOffset > math.MaxInt64-sequence {
		return 0, fmt.Errorf(
			"legacy event sequence overflows cumulative offset: sequence=%d offset=%d",
			sequence,
			n.cumulativeOffset,
		)
	}
	normalized := sequence + n.cumulativeOffset
	if n.initialized && normalized <= n.previousNormalized {
		if n.previousNormalized == math.MaxInt64 {
			return 0, errors.New("legacy event sequence cannot advance beyond maximum integer")
		}
		delta := n.previousNormalized + 1 - normalized
		if delta > math.MaxInt64-n.cumulativeOffset {
			return 0, errors.New("legacy event sequence cumulative offset overflow")
		}
		n.cumulativeOffset += delta
		normalized = sequence + n.cumulativeOffset
	}
	n.initialized = true
	n.previousNormalized = normalized
	return normalized, nil
}

func optionalLegacyString(value string) *string {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func optionalLegacyValue[T ~string](value T) *T {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func cloneOptionalLegacyValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func optionalLegacyPointerString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
