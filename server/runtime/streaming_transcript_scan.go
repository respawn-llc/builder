package runtime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
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
	scan                 *inMemoryTranscriptScan
	completions          map[string]tools.Result
	completionProvenance map[string]*TranscriptCommittedRowProvenance
	materialized         map[string]struct{}
	cacheWarningMode     config.CacheWarningMode

	turn turnBuffer

	lastCommittedAssistantFinalAnswer *string
}

type turnBuffer struct {
	assistant              *llm.Message
	assistantProvenance    *TranscriptCommittedRowProvenance
	assistantStepID        string
	callIDs                []string
	materialized           []llm.Message
	materializedProvenance []*TranscriptCommittedRowProvenance
	materializedSteps      []string
	localEntries           []storedLocalEntry
	localEntryProvenance   []*TranscriptCommittedRowProvenance
	localEntrySteps        []string
}

func newStreamingTranscriptScan(req inMemoryTranscriptScanRequest, cacheWarningMode config.CacheWarningMode) *streamingTranscriptScan {
	completions := make(map[string]tools.Result)
	materialized := make(map[string]struct{})
	return &streamingTranscriptScan{
		scan:                 newInMemoryTranscriptScan(req, completions, materialized),
		completions:          completions,
		completionProvenance: make(map[string]*TranscriptCommittedRowProvenance),
		materialized:         materialized,
		cacheWarningMode:     cacheWarningMode,
	}
}

func (s *streamingTranscriptScan) ApplyPersistedEvent(record session.EventRecord) error {
	if s == nil {
		return nil
	}
	stepID, _ := textutil.OptionalExact(record.StepID())
	payload, err := record.Payload()
	if err != nil {
		return err
	}
	switch payload := payload.(type) {
	case session.MessageRecord:
		msg, err := llmMessageFromSessionRecord(payload)
		if err != nil {
			return fmt.Errorf("restore session message record: %w", err)
		}
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		for _, reconstructed := range reconstructPersistedMessages(msg) {
			s.applyReconstructedMessage(reconstructed, &provenance, stepID)
		}
	case session.ToolCompletionRecord:
		completion, err := storedToolCompletionFromSessionRecord(payload)
		if err != nil {
			return fmt.Errorf("restore session tool completion record: %w", err)
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
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		s.completionProvenance[callID] = cloneTranscriptCommittedRowProvenance(&provenance)
	case session.LocalEntryRecord:
		if payload.Role == string(transcript.EntryRoleReviewerStatus) {
			return nil
		}
		entry, err := storedLocalEntryFromSessionRecord(payload)
		if err != nil {
			return fmt.Errorf("restore session local entry record: %w", err)
		}
		if entry.AfterToolCallID != nil {
			callID := strings.TrimSpace(*entry.AfterToolCallID)
			if callID == "" {
				return errors.New("session local entry record: after-tool call identity is empty")
			}
			if s.turn.assistant == nil || !s.turnOwnsCall(callID) {
				return fmt.Errorf(
					"session local entry record: after-tool call identity is outside the buffered assistant turn (call_id=%q)",
					callID,
				)
			}
			provenance, provenanceErr := transcriptProvenanceFromRecord(record)
			if provenanceErr != nil {
				return provenanceErr
			}
			s.turn.localEntries = append(s.turn.localEntries, entry)
			s.turn.localEntryProvenance = append(s.turn.localEntryProvenance, cloneTranscriptCommittedRowProvenance(&provenance))
			s.turn.localEntrySteps = append(s.turn.localEntrySteps, stepID)
			return nil
		}
		s.closeTurn()
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		s.appendLocalEntry(entry, stepID, &provenance)
	case session.ReviewerFeedbackRecord:
		s.closeTurn()
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		s.scan.appendEntry(reviewerFeedbackChatEntryFromSessionRecord(payload, stepID, &provenance))
	case session.ReviewerErrorRecord:
		s.closeTurn()
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		s.scan.appendEntry(reviewerErrorChatEntryFromSessionRecord(payload, stepID, &provenance))
	case session.CacheWarningRecord:
		s.closeTurn()
		warning := cacheWarningFromSessionRecord(payload)
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		s.scan.appendEntry(ChatEntry{
			StepID:              stepID,
			Visibility:          cacheWarningEntryVisibility(s.cacheWarningMode),
			Role:                cacheWarningTranscriptRole,
			Text:                transcript.CacheWarningText(warning),
			CommittedProvenance: &provenance,
		})
	case session.HistoryReplacementRecord:
		s.closeTurn()
		replacement, err := historyReplacementPayloadFromSessionRecord(payload)
		if err != nil {
			return fmt.Errorf("restore session history replacement record: %w", err)
		}
		s.scan.MarkCompactionBoundary()
		provenance, provenanceErr := transcriptProvenanceFromRecord(record)
		if provenanceErr != nil {
			return provenanceErr
		}
		entries := transcriptEntriesFromHistoryReplacement(
			llm.PrepareOpenAIInputItems(replacement.Items),
			replacement.CompactionNumber,
		)
		for index := range entries {
			entries[index].StepID = stepID
		}
		for _, entry := range assignHistoryReplacementEntryProvenance(entries, &provenance) {
			s.scan.appendEntry(entry)
		}
		s.lastCommittedAssistantFinalAnswer = textutil.Pointer(replacement.LastCommittedAssistantFinalAnswer)
	}
	return nil
}

func (s *streamingTranscriptScan) applyReconstructedMessage(msg llm.Message, provenance *TranscriptCommittedRowProvenance, stepID string) {
	if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
		s.closeTurn()
		buffered := msg
		s.turn.assistant = &buffered
		s.turn.assistantProvenance = cloneTranscriptCommittedRowProvenance(provenance)
		s.turn.assistantStepID = strings.TrimSpace(stepID)
		s.turn.callIDs = s.turn.callIDs[:0]
		for _, call := range msg.ToolCalls {
			if callID := strings.TrimSpace(call.ID); callID != "" {
				s.turn.callIDs = append(s.turn.callIDs, callID)
			}
		}
		return
	}
	toolCallID, _ := textutil.OptionalTrimmed(msg.ToolCallID)
	if msg.Role == llm.RoleTool && s.turn.assistant != nil && s.turnOwnsCall(toolCallID) {
		s.turn.materialized = append(s.turn.materialized, msg)
		s.turn.materializedProvenance = append(s.turn.materializedProvenance, cloneTranscriptCommittedRowProvenance(provenance))
		s.turn.materializedSteps = append(s.turn.materializedSteps, strings.TrimSpace(stepID))
		return
	}
	s.closeTurn()
	s.applyMessage(msg, provenance, stepID)
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

func (s *streamingTranscriptScan) applyMessage(msg llm.Message, provenance *TranscriptCommittedRowProvenance, stepID string) {
	var seq int64
	if provenance != nil {
		seq = provenance.EventSequence
	}
	s.scan.ApplyMessage(msg, seq, stepID, s.completionProvenance)
	s.lastCommittedAssistantFinalAnswer = applyLastCommittedAssistantFinalAnswer(s.lastCommittedAssistantFinalAnswer, msg)
}

func (s *streamingTranscriptScan) closeTurn() {
	if s.turn.assistant == nil {
		return
	}
	assistant := *s.turn.assistant
	assistantProvenance := s.turn.assistantProvenance
	assistantStepID := s.turn.assistantStepID
	materialized := s.turn.materialized
	materializedProvenance := s.turn.materializedProvenance
	materializedSteps := s.turn.materializedSteps
	callIDs := s.turn.callIDs
	localEntries := s.turn.localEntries
	localEntryProvenance := s.turn.localEntryProvenance
	localEntrySteps := s.turn.localEntrySteps

	if len(materializedSteps) != len(materialized) {
		panic(fmt.Sprintf("persisted transcript tool-message step identity count mismatch: materialized_count=%d step_id_count=%d", len(materialized), len(materializedSteps)))
	}
	if len(localEntrySteps) != len(localEntries) {
		panic(fmt.Sprintf("persisted transcript local-entry step identity count mismatch: local_entry_count=%d step_id_count=%d", len(localEntries), len(localEntrySteps)))
	}
	if len(materializedProvenance) != len(materialized) {
		panic(fmt.Sprintf("persisted transcript tool-message provenance count mismatch: materialized_count=%d provenance_count=%d", len(materialized), len(materializedProvenance)))
	}
	if len(localEntryProvenance) != len(localEntries) {
		panic(fmt.Sprintf("persisted transcript local-entry provenance count mismatch: local_entry_count=%d provenance_count=%d", len(localEntries), len(localEntryProvenance)))
	}
	orderedMaterialized := make([]struct {
		message    llm.Message
		provenance *TranscriptCommittedRowProvenance
		stepID     string
	}, len(materialized))
	for index := range materialized {
		provenance := materializedProvenance[index]
		if callID, present := textutil.OptionalTrimmed(materialized[index].ToolCallID); present {
			if owner := s.completionProvenance[callID]; owner != nil {
				provenance = owner
			}
		}
		orderedMaterialized[index] = struct {
			message    llm.Message
			provenance *TranscriptCommittedRowProvenance
			stepID     string
		}{
			message:    materialized[index],
			provenance: provenance,
			stepID:     materializedSteps[index],
		}
	}
	sort.SliceStable(orderedMaterialized, func(left, right int) bool {
		return transcriptCommittedProvenanceBefore(
			orderedMaterialized[left].provenance,
			orderedMaterialized[right].provenance,
		)
	})
	for _, rm := range materialized {
		if callID, present := textutil.OptionalTrimmed(rm.ToolCallID); present {
			s.materialized[callID] = struct{}{}
		}
	}
	s.applyMessage(assistant, assistantProvenance, assistantStepID)
	for index, entry := range localEntries {
		callID := strings.TrimSpace(*entry.AfterToolCallID)
		if _, materialized := s.materialized[callID]; materialized {
			continue
		}
		s.appendLocalEntry(entry, localEntrySteps[index], localEntryProvenance[index])
	}
	for _, materializedEntry := range orderedMaterialized {
		s.applyMessage(materializedEntry.message, materializedEntry.provenance, materializedEntry.stepID)
		callID, _ := textutil.OptionalTrimmed(materializedEntry.message.ToolCallID)
		for localIndex, entry := range localEntries {
			if entry.AfterToolCallID == nil || strings.TrimSpace(*entry.AfterToolCallID) != callID {
				continue
			}
			s.appendLocalEntry(entry, localEntrySteps[localIndex], localEntryProvenance[localIndex])
		}
	}

	for _, callID := range callIDs {
		delete(s.completions, callID)
		delete(s.completionProvenance, callID)
		delete(s.materialized, callID)
	}
	s.turn = turnBuffer{
		callIDs:                callIDs[:0],
		materialized:           materialized[:0],
		materializedProvenance: materializedProvenance[:0],
		materializedSteps:      materializedSteps[:0],
		localEntries:           localEntries[:0],
		localEntryProvenance:   localEntryProvenance[:0],
		localEntrySteps:        localEntrySteps[:0],
	}
}

func (s *streamingTranscriptScan) appendLocalEntry(entry storedLocalEntry, stepID string, provenance *TranscriptCommittedRowProvenance) {
	projected := *localEntryChatEntryForStep(entry, stepID)
	projected.CommittedProvenance = cloneTranscriptCommittedRowProvenance(provenance)
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

func (s *streamingTranscriptScan) LastCommittedAssistantFinalAnswer() *string {
	s.closeTurn()
	return textutil.Pointer(s.lastCommittedAssistantFinalAnswer)
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
