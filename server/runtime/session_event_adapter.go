package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

var ErrUnsupportedSessionProviderItem = errors.New("unsupported session provider item")

type UnsupportedSessionProviderItemError struct {
	Index int
	Type  llm.ResponseItemType
}

func (e UnsupportedSessionProviderItemError) Error() string {
	return fmt.Sprintf("provider item %d has unsupported type %q", e.Index, e.Type)
}

func (e UnsupportedSessionProviderItemError) Unwrap() error {
	return ErrUnsupportedSessionProviderItem
}

func sessionMessageRecordFromLLM(message llm.Message) (session.MessageRecord, error) {
	record := session.MessageRecord{
		Role:                 session.MessageRole(message.Role),
		MessageType:          convertOptionalString[llm.MessageType, session.MessageType](message.MessageType),
		SourcePath:           textutil.Pointer(message.SourcePath),
		WorktreeContext:      session.CloneWorktreeContext(message.WorktreeContext),
		Content:              textutil.Pointer(message.Content),
		CompactContent:       textutil.Pointer(message.CompactContent),
		Name:                 textutil.Pointer(message.Name),
		ToolCallID:           textutil.Pointer(message.ToolCallID),
		Phase:                convertOptionalString[llm.MessagePhase, session.MessagePhase](message.Phase),
		BackgroundActivityID: textutil.Pointer(message.BackgroundActivityID),
		BackgroundExitCode:   textutil.Pointer(message.BackgroundExitCode),
	}
	if len(message.ToolCalls) > 0 {
		record.ToolCalls = make([]session.MessageToolCallRecord, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			input := append(json.RawMessage(nil), call.Input...)
			if len(input) == 0 {
				if call.Custom && call.CustomInput != nil &&
					strings.TrimSpace(*call.CustomInput) != "" {
					input = normalizeRuntimeToolInput(*call.CustomInput)
				} else {
					input = json.RawMessage(`{}`)
				}
			}
			kind := session.ToolCallKindFunction
			if call.Custom {
				kind = session.ToolCallKindCustom
			}
			record.ToolCalls = append(record.ToolCalls, session.MessageToolCallRecord{
				CallID:       call.ID,
				Name:         call.Name,
				Kind:         kind,
				Presentation: append(json.RawMessage(nil), call.Presentation...),
				Input:        input,
				CustomInput:  textutil.Pointer(call.CustomInput),
			})
		}
	}
	if len(message.ReasoningItems) > 0 {
		record.ReasoningItems = make([]session.MessageReasoningRecord, 0, len(message.ReasoningItems))
		for _, reasoning := range message.ReasoningItems {
			record.ReasoningItems = append(record.ReasoningItems, session.MessageReasoningRecord{
				ID:               reasoning.ID,
				EncryptedContent: reasoning.EncryptedContent,
			})
		}
	}
	return record, nil
}

func llmMessageFromSessionRecord(record session.MessageRecord) llm.Message {
	message := llm.Message{
		Role:                 llm.Role(record.Role),
		MessageType:          convertOptionalString[session.MessageType, llm.MessageType](record.MessageType),
		SourcePath:           textutil.Pointer(record.SourcePath),
		WorktreeContext:      session.CloneWorktreeContext(record.WorktreeContext),
		Content:              textutil.Pointer(record.Content),
		CompactContent:       textutil.Pointer(record.CompactContent),
		Name:                 textutil.Pointer(record.Name),
		ToolCallID:           textutil.Pointer(record.ToolCallID),
		Phase:                convertOptionalString[session.MessagePhase, llm.MessagePhase](record.Phase),
		BackgroundActivityID: textutil.Pointer(record.BackgroundActivityID),
		BackgroundExitCode:   textutil.Pointer(record.BackgroundExitCode),
	}
	if len(record.ToolCalls) > 0 {
		message.ToolCalls = make([]llm.ToolCall, 0, len(record.ToolCalls))
		for _, call := range record.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, llm.ToolCall{
				ID:           call.CallID,
				Name:         call.Name,
				Presentation: append(json.RawMessage(nil), call.Presentation...),
				Input:        append(json.RawMessage(nil), call.Input...),
				Custom:       call.Kind == session.ToolCallKindCustom,
				CustomInput:  textutil.Pointer(call.CustomInput),
			})
		}
	}
	if len(record.ReasoningItems) > 0 {
		message.ReasoningItems = make([]llm.ReasoningItem, 0, len(record.ReasoningItems))
		for _, reasoning := range record.ReasoningItems {
			message.ReasoningItems = append(message.ReasoningItems, llm.ReasoningItem{
				ID:               reasoning.ID,
				EncryptedContent: reasoning.EncryptedContent,
			})
		}
	}
	return message
}

func sessionToolCompletionRecordFromRuntime(
	result tools.Result,
	providerItems []llm.ResponseItem,
) (session.ToolCompletionRecord, error) {
	outputKind, err := sessionToolOutputKind(providerItems)
	if err != nil {
		return session.ToolCompletionRecord{}, err
	}
	record := session.ToolCompletionRecord{
		CallID:        result.CallID,
		Name:          string(result.Name),
		OutputKind:    outputKind,
		IsError:       result.IsError,
		Output:        append(json.RawMessage(nil), result.Output...),
		Summary:       textutil.Pointer(result.Summary),
		CondensedText: textutil.Pointer(result.CondensedText),
	}
	if result.Presentation != nil {
		record.Presentation = transcript.EncodeToolCallMeta(*result.Presentation)
	}
	if len(providerItems) > 0 {
		record.ProviderItems = make([]session.ToolCompletionProviderItem, 0, len(providerItems))
		for index, item := range providerItems {
			snapshot, snapshotErr := sessionProviderItemFromLLM(index, item)
			if snapshotErr != nil {
				return session.ToolCompletionRecord{}, snapshotErr
			}
			record.ProviderItems = append(record.ProviderItems, snapshot)
		}
	}
	return record, nil
}

func storedToolCompletionFromSessionRecord(
	record session.ToolCompletionRecord,
) storedToolCompletion {
	var presentation *transcript.ToolCallMeta
	if len(record.Presentation) > 0 {
		decoded, ok := transcript.DecodeToolCallMeta(record.Presentation)
		if !ok {
			panic("decoded session tool completion has invalid presentation")
		}
		presentation = decoded
	}
	providerItems := make([]llm.ResponseItem, 0, len(record.ProviderItems))
	for _, item := range record.ProviderItems {
		providerItem := llm.ResponseItem{
			Type:         llm.ResponseItemType(item.Type),
			Name:         textutil.Pointer(item.Name),
			CallID:       textutil.Pointer(item.CallID),
			Raw:          append(json.RawMessage(nil), item.Raw...),
			LinkedCallID: textutil.Pointer(item.LinkedCallID),
			LinkKind: convertOptionalString[
				session.ProviderItemLinkKind,
				llm.ResponseItemLinkKind,
			](item.LinkKind),
		}
		switch item.Type {
		case session.ProviderInputItemTypeFunctionCallOutput, session.ProviderInputItemTypeCustomToolOutput:
			providerItem.Output = append(json.RawMessage(nil), record.Output...)
		}
		providerItems = append(providerItems, providerItem)
	}
	return storedToolCompletion{
		CallID:        record.CallID,
		Name:          record.Name,
		IsError:       record.IsError,
		Output:        append(json.RawMessage(nil), record.Output...),
		Summary:       textutil.Pointer(record.Summary),
		CondensedText: textutil.Pointer(record.CondensedText),
		Presentation:  presentation,
		ProviderItems: providerItems,
	}
}

func sessionToolCompletionRecordFromStored(
	completion storedToolCompletion,
) (session.ToolCompletionRecord, error) {
	result := tools.Result{
		CallID:        completion.CallID,
		Name:          toolspec.ID(completion.Name),
		IsError:       completion.IsError,
		Output:        append(json.RawMessage(nil), completion.Output...),
		Summary:       completion.Summary,
		CondensedText: completion.CondensedText,
		Presentation:  completion.Presentation,
	}
	return sessionToolCompletionRecordFromRuntime(result, completion.ProviderItems)
}

func sessionLocalEntryRecordFromRuntime(
	entry storedLocalEntry,
) (session.LocalEntryRecord, error) {
	visibility, err := sessionEntryVisibilityFromRuntime(entry.Visibility)
	if err != nil {
		return session.LocalEntryRecord{}, err
	}
	record := session.LocalEntryRecord{
		Visibility:       visibility,
		Role:             entry.Role,
		Text:             textutil.OptionalExactString(entry.Text),
		DurationMs:       textutil.Pointer(entry.DurationMs),
		CondensedText:    textutil.Pointer(entry.CondensedText),
		DiagnosticKey:    textutil.Pointer(entry.DiagnosticKey),
		NoticeID:         textutil.Pointer(entry.NoticeID),
		AfterToolCallID:  textutil.Pointer(entry.AfterToolCallID),
		ToolOutputRepair: textutil.Pointer(entry.ToolOutputRepair),
	}
	return record, nil
}

func sessionEntryVisibilityFromRuntime(
	visibility transcript.EntryVisibility,
) (session.EntryVisibility, error) {
	switch transcript.NormalizeEntryVisibility(visibility) {
	case transcript.EntryVisibilityAuto:
		return session.EntryVisibilityAuto, nil
	case transcript.EntryVisibilityOngoing:
		return session.EntryVisibilityOngoing, nil
	case transcript.EntryVisibilityOngoingCollapsed:
		return session.EntryVisibilityOngoingCollapsed, nil
	case transcript.EntryVisibilityDetail:
		return session.EntryVisibilityDetail, nil
	case transcript.EntryVisibilityHidden:
		return session.EntryVisibilityHidden, nil
	default:
		return "", fmt.Errorf("unsupported runtime transcript visibility %q", visibility)
	}
}

func runtimeEntryVisibilityFromSession(
	visibility session.EntryVisibility,
) transcript.EntryVisibility {
	switch visibility {
	case session.EntryVisibilityAuto:
		return transcript.EntryVisibilityAuto
	case session.EntryVisibilityOngoing:
		return transcript.EntryVisibilityOngoing
	case session.EntryVisibilityOngoingCollapsed:
		return transcript.EntryVisibilityOngoingCollapsed
	case session.EntryVisibilityDetail:
		return transcript.EntryVisibilityDetail
	case session.EntryVisibilityHidden:
		return transcript.EntryVisibilityHidden
	default:
		panic(fmt.Sprintf("decoded session local entry has unsupported visibility %q", visibility))
	}
}

func storedLocalEntryFromSessionRecord(
	record session.LocalEntryRecord,
) storedLocalEntry {
	text, _ := textutil.OptionalExact(record.Text)
	return storedLocalEntry{
		Visibility:       runtimeEntryVisibilityFromSession(record.Visibility),
		Role:             record.Role,
		Text:             text,
		DurationMs:       textutil.Pointer(record.DurationMs),
		CondensedText:    textutil.Pointer(record.CondensedText),
		DiagnosticKey:    textutil.Pointer(record.DiagnosticKey),
		NoticeID:         textutil.Pointer(record.NoticeID),
		AfterToolCallID:  textutil.Pointer(record.AfterToolCallID),
		ToolOutputRepair: textutil.Pointer(record.ToolOutputRepair),
	}
}

func sessionCacheRequestRecordFromRuntime(
	observation persistedCacheRequestObserved,
) (session.CacheRequestObservationRecord, error) {
	record := session.CacheRequestObservationRecord{
		DigestVersion: observation.DigestVersion,
		CacheKey:      observation.CacheKey,
		Scope:         session.CacheScope(observation.Scope),
		ChunkCount:    observation.ChunkCount,
		TerminalHash:  observation.TerminalHash,
	}
	return record, nil
}

func sessionCacheResponseRecordFromRuntime(
	observation persistedCacheResponseObserved,
) (session.CacheResponseObservationRecord, error) {
	record := session.CacheResponseObservationRecord{
		DigestVersion: observation.DigestVersion,
		CacheKey:      observation.CacheKey,
		Scope:         session.CacheScope(observation.Scope),
		ChunkCount:    observation.ChunkCount,
		TerminalHash:  observation.TerminalHash,
	}
	record.CachedInputTokens = textutil.Pointer(observation.CachedInputTokens)
	return record, nil
}

func sessionCacheWarningRecordFromRuntime(
	warning transcript.CacheWarning,
) (session.CacheWarningRecord, error) {
	record := session.CacheWarningRecord{
		Scope:    session.CacheScope(warning.Scope),
		Reason:   session.CacheWarningReason(warning.Reason),
		CacheKey: textutil.Pointer(warning.CacheKey),
	}
	record.LostInputTokens = textutil.Pointer(warning.LostInputTokens)
	return record, nil
}

func persistedCacheRequestObservedFromSessionRecord(
	record session.CacheRequestObservationRecord,
) persistedCacheRequestObserved {
	return persistedCacheRequestObserved{
		DigestVersion: record.DigestVersion,
		CacheKey:      record.CacheKey,
		Scope:         transcript.CacheWarningScope(record.Scope),
		ChunkCount:    record.ChunkCount,
		TerminalHash:  record.TerminalHash,
	}
}

func persistedCacheResponseObservedFromSessionRecord(
	record session.CacheResponseObservationRecord,
) persistedCacheResponseObserved {
	observation := persistedCacheResponseObserved{
		DigestVersion:     record.DigestVersion,
		CacheKey:          record.CacheKey,
		Scope:             transcript.CacheWarningScope(record.Scope),
		ChunkCount:        record.ChunkCount,
		TerminalHash:      record.TerminalHash,
		CachedInputTokens: textutil.Pointer(record.CachedInputTokens),
	}
	return observation
}

func cacheWarningFromSessionRecord(record session.CacheWarningRecord) transcript.CacheWarning {
	warning := transcript.CacheWarning{
		Scope:           transcript.CacheWarningScope(record.Scope),
		Reason:          transcript.CacheWarningReason(record.Reason),
		CacheKey:        textutil.Pointer(record.CacheKey),
		LostInputTokens: textutil.Pointer(record.LostInputTokens),
	}
	return warning
}

func sessionHistoryReplacementRecordFromRuntime(
	payload historyReplacementPayload,
) (session.HistoryReplacementRecord, error) {
	record := session.HistoryReplacementRecord{
		Engine:                            payload.Engine,
		Mode:                              session.CompactionMode(payload.Mode),
		CommittedEntryStart:               textutil.Pointer(payload.CommittedEntryStart),
		PendingHandoffFutureMessage:       textutil.Pointer(payload.PendingHandoffFutureMessage),
		LastCommittedAssistantFinalAnswer: textutil.Pointer(payload.LastCommittedAssistantFinalAnswer),
		LatestRollbackCandidate:           textutil.Pointer(payload.LatestRollbackCandidate),
	}
	record.CompactionNumber = textutil.Pointer(payload.CompactionNumber)
	preparedItems := llm.PrepareOpenAIInputItems(payload.Items)
	if len(preparedItems) > 0 {
		record.Items = make([]session.ProviderHistoryItem, 0, len(preparedItems))
		for index, item := range preparedItems {
			if len(item.Raw) == 0 &&
				item.Type != llm.ResponseItemTypeFunctionCallOutput &&
				item.Type != llm.ResponseItemTypeCustomToolOutput {
				return session.HistoryReplacementRecord{}, session.ProviderHistoryItemError{
					Index:  index,
					Type:   session.ProviderHistoryItemType(item.Type),
					Reason: session.ProviderHistoryItemMissingRaw,
				}
			}
			historyItem, err := sessionProviderHistoryItemFromLLM(index, item)
			if err != nil {
				return session.HistoryReplacementRecord{}, err
			}
			record.Items = append(record.Items, historyItem)
		}
	}
	return record, nil
}

func historyReplacementPayloadFromSessionRecord(
	record session.HistoryReplacementRecord,
) historyReplacementPayload {
	payload := historyReplacementPayload{
		Engine:                            record.Engine,
		Mode:                              string(record.Mode),
		CommittedEntryStart:               textutil.Pointer(record.CommittedEntryStart),
		PendingHandoffFutureMessage:       textutil.Pointer(record.PendingHandoffFutureMessage),
		LastCommittedAssistantFinalAnswer: textutil.Pointer(record.LastCommittedAssistantFinalAnswer),
		LatestRollbackCandidate:           textutil.Pointer(record.LatestRollbackCandidate),
	}
	payload.CompactionNumber = textutil.Pointer(record.CompactionNumber)
	if len(record.Items) > 0 {
		payload.Items = make([]llm.ResponseItem, 0, len(record.Items))
		for _, item := range record.Items {
			payload.Items = append(payload.Items, llmResponseItemFromSessionHistory(item))
		}
	}
	return payload
}

func sessionProviderHistoryItemFromLLM(
	index int,
	item llm.ResponseItem,
) (session.ProviderHistoryItem, error) {
	historyItem := session.ProviderHistoryItem{
		Type:                 session.ProviderHistoryItemType(item.Type),
		Role:                 convertOptionalString[llm.Role, session.MessageRole](item.Role),
		MessageType:          convertOptionalString[llm.MessageType, session.MessageType](item.MessageType),
		SourcePath:           textutil.Pointer(item.SourcePath),
		WorktreeContext:      session.CloneWorktreeContext(item.WorktreeContext),
		Phase:                convertOptionalString[llm.MessagePhase, session.MessagePhase](item.Phase),
		ID:                   textutil.Pointer(item.ID),
		Name:                 textutil.Pointer(item.Name),
		CallID:               textutil.Pointer(item.CallID),
		Content:              textutil.Pointer(item.Content),
		CompactContent:       textutil.Pointer(item.CompactContent),
		BackgroundActivityID: textutil.Pointer(item.BackgroundActivityID),
		BackgroundExitCode:   textutil.Pointer(item.BackgroundExitCode),
		ToolPresentation:     append(json.RawMessage(nil), item.ToolPresentation...),
		Arguments:            append(json.RawMessage(nil), item.Arguments...),
		CustomInput:          textutil.Pointer(item.CustomInput),
		Output:               append(json.RawMessage(nil), item.Output...),
		EncryptedContent:     textutil.Pointer(item.EncryptedContent),
		Raw:                  append(json.RawMessage(nil), item.Raw...),
		LinkedCallID:         textutil.Pointer(item.LinkedCallID),
	}
	if len(item.ReasoningSummary) > 0 {
		historyItem.ReasoningSummary = make(
			[]session.ProviderHistoryReasoningEntry,
			0,
			len(item.ReasoningSummary),
		)
		for _, entry := range item.ReasoningSummary {
			historyItem.ReasoningSummary = append(
				historyItem.ReasoningSummary,
				session.ProviderHistoryReasoningEntry{
					Role: textutil.Pointer(entry.Role),
					Text: entry.Text,
				},
			)
		}
	}
	historyItem.LinkKind = convertOptionalString[
		llm.ResponseItemLinkKind,
		session.ProviderItemLinkKind,
	](item.LinkKind)
	return historyItem, nil
}

func llmResponseItemFromSessionHistory(item session.ProviderHistoryItem) llm.ResponseItem {
	responseItem := llm.ResponseItem{
		Type:                 llm.ResponseItemType(item.Type),
		Role:                 convertOptionalString[session.MessageRole, llm.Role](item.Role),
		MessageType:          convertOptionalString[session.MessageType, llm.MessageType](item.MessageType),
		SourcePath:           textutil.Pointer(item.SourcePath),
		WorktreeContext:      session.CloneWorktreeContext(item.WorktreeContext),
		Phase:                convertOptionalString[session.MessagePhase, llm.MessagePhase](item.Phase),
		ID:                   textutil.Pointer(item.ID),
		Name:                 textutil.Pointer(item.Name),
		CallID:               textutil.Pointer(item.CallID),
		Content:              textutil.Pointer(item.Content),
		CompactContent:       textutil.Pointer(item.CompactContent),
		BackgroundActivityID: textutil.Pointer(item.BackgroundActivityID),
		BackgroundExitCode:   textutil.Pointer(item.BackgroundExitCode),
		ToolPresentation:     append(json.RawMessage(nil), item.ToolPresentation...),
		Arguments:            append(json.RawMessage(nil), item.Arguments...),
		CustomInput:          textutil.Pointer(item.CustomInput),
		Output:               append(json.RawMessage(nil), item.Output...),
		EncryptedContent:     textutil.Pointer(item.EncryptedContent),
		Raw:                  append(json.RawMessage(nil), item.Raw...),
		LinkedCallID:         textutil.Pointer(item.LinkedCallID),
		LinkKind: convertOptionalString[
			session.ProviderItemLinkKind,
			llm.ResponseItemLinkKind,
		](item.LinkKind),
	}
	if len(item.ReasoningSummary) > 0 {
		responseItem.ReasoningSummary = make([]llm.ReasoningEntry, 0, len(item.ReasoningSummary))
		for _, entry := range item.ReasoningSummary {
			responseItem.ReasoningSummary = append(responseItem.ReasoningSummary, llm.ReasoningEntry{
				Role: textutil.Pointer(entry.Role),
				Text: entry.Text,
			})
		}
	}
	return responseItem
}

func sessionToolOutputKind(items []llm.ResponseItem) (session.ToolOutputKind, error) {
	var outputKind *session.ToolOutputKind
	for index, item := range items {
		var kind session.ToolOutputKind
		switch item.Type {
		case llm.ResponseItemTypeFunctionCallOutput:
			kind = session.ToolOutputKindFunction
		case llm.ResponseItemTypeCustomToolOutput:
			kind = session.ToolOutputKindCustom
		case llm.ResponseItemTypeOther:
			continue
		default:
			return "", UnsupportedSessionProviderItemError{Index: index, Type: item.Type}
		}
		if outputKind != nil && *outputKind != kind {
			return "", errors.New("provider completion contains mixed function and custom output items")
		}
		outputKind = &kind
	}
	if outputKind == nil {
		return "", errors.New("provider completion output item is required")
	}
	return *outputKind, nil
}

func sessionProviderItemFromLLM(
	index int,
	item llm.ResponseItem,
) (session.ToolCompletionProviderItem, error) {
	snapshot := session.ToolCompletionProviderItem{
		Name:         textutil.Pointer(item.Name),
		CallID:       textutil.Pointer(item.CallID),
		Raw:          append(json.RawMessage(nil), item.Raw...),
		LinkedCallID: textutil.Pointer(item.LinkedCallID),
	}
	switch item.Type {
	case llm.ResponseItemTypeFunctionCallOutput:
		snapshot.Type = session.ProviderInputItemTypeFunctionCallOutput
	case llm.ResponseItemTypeCustomToolOutput:
		snapshot.Type = session.ProviderInputItemTypeCustomToolOutput
	case llm.ResponseItemTypeOther:
		snapshot.Type = session.ProviderInputItemTypeOther
	default:
		return session.ToolCompletionProviderItem{}, UnsupportedSessionProviderItemError{
			Index: index,
			Type:  item.Type,
		}
	}
	switch {
	case item.LinkKind == nil:
	case *item.LinkKind == llm.ResponseItemLinkToolOutputAttachment:
		linkKind := session.ProviderItemLinkToolOutputAttachment
		snapshot.LinkKind = &linkKind
	default:
		return session.ToolCompletionProviderItem{}, UnsupportedSessionProviderItemError{
			Index: index,
			Type:  item.Type,
		}
	}
	return snapshot, nil
}

func convertOptionalString[From ~string, To ~string](value *From) *To {
	if value == nil {
		return nil
	}
	converted := To(*value)
	return &converted
}
