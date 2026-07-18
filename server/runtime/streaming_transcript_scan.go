package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

// streamingTranscriptScan projects transcript-visible state from a stream of
// persisted events while retaining only the requested window plus a single
// in-flight assistant turn. It never materializes the full event history:
// committed entries outside the page/tail window are discarded as they stream
// by (see inMemoryTranscriptScan.appendEntry), and tool completions/materialized
// tool-result messages are held only until the assistant turn that owns them is
// flushed.
//
// An assistant turn's tool result can be persisted as a tool_completed event
// (synthesized at render) and/or a RoleTool message (materialized at render).
// Whether a call is materialized is only known once the turn's later events have
// streamed in, so the assistant message is buffered until the turn closes (the
// next non-tool event or end of stream) before its entries are emitted.
type streamingTranscriptScan struct {
	scan             *inMemoryTranscriptScan
	completions      map[string]tools.Result
	materialized     map[string]struct{}
	cacheWarningMode config.CacheWarningMode

	turn turnBuffer

	lastCommittedAssistantFinalAnswer string
}

type turnBuffer struct {
	assistant         *llm.Message
	assistantStepID   string
	callIDs           []string
	materialized      []llm.Message
	materializedSteps []string
	localEntries      []storedLocalEntry
	localEntrySteps   []string
}

func newStreamingTranscriptScan(req inMemoryTranscriptScanRequest, cacheWarningMode config.CacheWarningMode) *streamingTranscriptScan {
	completions := make(map[string]tools.Result)
	materialized := make(map[string]struct{})
	return &streamingTranscriptScan{
		scan:             newInMemoryTranscriptScan(req, completions, materialized),
		completions:      completions,
		materialized:     materialized,
		cacheWarningMode: cacheWarningMode,
	}
}

func (s *streamingTranscriptScan) ApplyPersistedEvent(evt session.Event) error {
	if s == nil {
		return nil
	}
	switch strings.TrimSpace(evt.Kind) {
	case "message":
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			return fmt.Errorf("decode message event: %w", err)
		}
		for _, reconstructed := range reconstructPersistedMessages(msg) {
			s.applyReconstructedMessage(reconstructed, evt.Seq, evt.StepID)
		}
	case "tool_completed":
		var completion storedToolCompletion
		if err := json.Unmarshal(evt.Payload, &completion); err != nil {
			return fmt.Errorf("decode tool_completed event: %w", err)
		}
		callID := strings.TrimSpace(completion.CallID)
		if callID == "" {
			return nil
		}
		s.completions[callID] = tools.Result{
			CallID:        completion.CallID,
			Name:          toolspec.ID(completion.Name),
			IsError:       completion.IsError,
			Output:        completion.Output,
			Summary:       completion.Summary,
			CondensedText: completion.CondensedText,
			Presentation:  completion.Presentation,
		}
	case "local_entry":
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			return fmt.Errorf("decode local_entry event: %w", err)
		}
		if entry.AfterToolCallID != nil {
			callID := strings.TrimSpace(*entry.AfterToolCallID)
			if callID == "" {
				return errors.New("decode local_entry event: after-tool call identity is empty")
			}
			if s.turn.assistant == nil || !s.turnOwnsCall(callID) {
				return fmt.Errorf(
					"decode local_entry event: after-tool call identity is outside the buffered assistant turn (call_id=%q)",
					callID,
				)
			}
			s.turn.localEntries = append(s.turn.localEntries, entry)
			s.turn.localEntrySteps = append(s.turn.localEntrySteps, strings.TrimSpace(evt.StepID))
			return nil
		}
		s.closeTurn()
		s.appendLocalEntry(entry, evt.StepID)
	case sessionEventCacheWarning:
		s.closeTurn()
		var warning transcript.CacheWarning
		if err := json.Unmarshal(evt.Payload, &warning); err != nil {
			return fmt.Errorf("decode %s event: %w", sessionEventCacheWarning, err)
		}
		s.scan.appendEntry(ChatEntry{
			StepID:     strings.TrimSpace(evt.StepID),
			Visibility: cacheWarningEntryVisibility(s.cacheWarningMode),
			Role:       cacheWarningTranscriptRole,
			Text:       transcript.CacheWarningText(warning),
		})
	case "history_replaced":
		s.closeTurn()
		payload, ignoredLegacy, err := decodePersistedHistoryReplacementPayload(evt.Payload)
		if err != nil {
			return fmt.Errorf("%w: %w", errDecodeHistoryReplacedEvent, err)
		}
		if ignoredLegacy {
			return nil
		}
		s.scan.MarkCompactionBoundary()
		for _, entry := range transcriptEntriesFromHistoryReplacement(llm.PrepareOpenAIInputItems(payload.Items)) {
			entry.StepID = strings.TrimSpace(evt.StepID)
			s.scan.appendEntry(entry)
		}
		if answer := strings.TrimSpace(payload.LastCommittedAssistantFinalAnswer); answer != "" {
			s.lastCommittedAssistantFinalAnswer = payload.LastCommittedAssistantFinalAnswer
		}
	}
	return nil
}

func (s *streamingTranscriptScan) applyReconstructedMessage(msg llm.Message, seq int64, stepID string) {
	if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
		s.closeTurn()
		buffered := msg
		s.turn.assistant = &buffered
		s.turn.assistantStepID = strings.TrimSpace(stepID)
		s.turn.callIDs = s.turn.callIDs[:0]
		for _, call := range msg.ToolCalls {
			if callID := strings.TrimSpace(call.ID); callID != "" {
				s.turn.callIDs = append(s.turn.callIDs, callID)
			}
		}
		return
	}
	if msg.Role == llm.RoleTool && s.turn.assistant != nil && s.turnOwnsCall(strings.TrimSpace(msg.ToolCallID)) {
		s.turn.materialized = append(s.turn.materialized, msg)
		s.turn.materializedSteps = append(s.turn.materializedSteps, strings.TrimSpace(stepID))
		return
	}
	s.closeTurn()
	s.applyMessage(msg, seq, stepID)
}

func (s *streamingTranscriptScan) turnOwnsCall(callID string) bool {
	if callID == "" {
		return false
	}
	for _, id := range s.turn.callIDs {
		if id == callID {
			return true
		}
	}
	return false
}

func (s *streamingTranscriptScan) applyMessage(msg llm.Message, seq int64, stepID string) {
	s.scan.ApplyMessage(msg, seq, stepID)
	s.lastCommittedAssistantFinalAnswer = applyLastCommittedAssistantFinalAnswer(s.lastCommittedAssistantFinalAnswer, msg)
}

func (s *streamingTranscriptScan) closeTurn() {
	if s.turn.assistant == nil {
		return
	}
	assistant := *s.turn.assistant
	assistantStepID := s.turn.assistantStepID
	materialized := s.turn.materialized
	materializedSteps := s.turn.materializedSteps
	callIDs := s.turn.callIDs
	localEntries := s.turn.localEntries
	localEntrySteps := s.turn.localEntrySteps

	if len(materializedSteps) != len(materialized) {
		panic(fmt.Sprintf("persisted transcript tool-message step identity count mismatch: materialized_count=%d step_id_count=%d", len(materialized), len(materializedSteps)))
	}
	if len(localEntrySteps) != len(localEntries) {
		panic(fmt.Sprintf("persisted transcript local-entry step identity count mismatch: local_entry_count=%d step_id_count=%d", len(localEntries), len(localEntrySteps)))
	}
	for _, rm := range materialized {
		if callID := strings.TrimSpace(rm.ToolCallID); callID != "" {
			s.materialized[callID] = struct{}{}
		}
	}
	s.applyMessage(assistant, 0, assistantStepID)
	for index, entry := range localEntries {
		callID := strings.TrimSpace(*entry.AfterToolCallID)
		if _, materialized := s.materialized[callID]; materialized {
			continue
		}
		s.appendLocalEntry(entry, localEntrySteps[index])
	}
	for index, rm := range materialized {
		s.applyMessage(rm, 0, materializedSteps[index])
		callID := strings.TrimSpace(rm.ToolCallID)
		for localIndex, entry := range localEntries {
			if entry.AfterToolCallID == nil || strings.TrimSpace(*entry.AfterToolCallID) != callID {
				continue
			}
			s.appendLocalEntry(entry, localEntrySteps[localIndex])
		}
	}

	for _, callID := range callIDs {
		delete(s.completions, callID)
		delete(s.materialized, callID)
	}
	s.turn = turnBuffer{
		callIDs:           callIDs[:0],
		materialized:      materialized[:0],
		materializedSteps: materializedSteps[:0],
		localEntries:      localEntries[:0],
		localEntrySteps:   localEntrySteps[:0],
	}
}

func (s *streamingTranscriptScan) appendLocalEntry(entry storedLocalEntry, stepID string) {
	projected := *localEntryChatEntryForStep(entry, stepID)
	s.scan.appendEntry(projected)
}

func (s *streamingTranscriptScan) PageSnapshot() transcriptPageSnapshot {
	s.closeTurn()
	return s.scan.PageSnapshot()
}

func (s *streamingTranscriptScan) RecentTailSnapshot() TranscriptWindowSnapshot {
	s.closeTurn()
	return s.scan.RecentTailSnapshot()
}

func (s *streamingTranscriptScan) TotalEntries() int {
	s.closeTurn()
	return s.scan.totalEntries
}

func (s *streamingTranscriptScan) LastCommittedAssistantFinalAnswer() string {
	s.closeTurn()
	return s.lastCommittedAssistantFinalAnswer
}

// reconstructPersistedMessages round-trips a persisted message through the same
// item encode/decode the chat projection uses, so streamed transcript entries
// are byte-identical to the historical chatStore-driven projection.
func reconstructPersistedMessages(msg llm.Message) []llm.Message {
	out := make([]llm.Message, 0, 1)
	walker := newResponseItemMessageWalker(func(m llm.Message) {
		out = append(out, m)
	})
	for _, item := range llm.ItemsFromMessages([]llm.Message{msg}) {
		walker.Apply(item)
	}
	walker.Flush()
	return out
}
