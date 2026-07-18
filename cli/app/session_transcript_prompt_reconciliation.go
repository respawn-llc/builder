package app

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"core/cli/tui"
	"core/shared/clientui"
)

type ongoingTranscriptPromptOwner interface {
	snapshotTranscriptPromptOwnership() transcriptPromptOwnershipSnapshot
	commitTranscriptPromptReconciliation(transcriptPromptReconciliation)
}

type transcriptPromptOwnershipSnapshot struct {
	currentPromptID *clientui.PromptID
	retention       transcriptPromptInteractionRetention
	events          []askEvent
}

type transcriptPromptInteractionRetention uint8

const (
	transcriptPromptRetentionNone transcriptPromptInteractionRetention = iota
	transcriptPromptRetentionMeaningfulDraft
	transcriptPromptRetentionAnswerPending
)

type transcriptPromptReconciliation struct {
	events         []transcriptPromptReconciliationEvent
	canceled       []askEvent
	activeRetained bool
	source         clientui.AttentionNotificationSource
}

type transcriptPromptReconciliationEvent struct {
	prompt   clientui.TranscriptPrompt
	retained askEvent
	owned    bool
	notify   bool
}

type transcriptPromptContract struct {
	PromptID  clientui.PromptID
	SessionID string
	Kind      clientui.TranscriptPromptKind
	StepID    string
	CreatedAt time.Time
	Tool      *clientui.ToolProvenance
}

type transcriptPromptContractMismatch struct {
	PromptID clientui.PromptID
	Old      transcriptPromptContract
	New      transcriptPromptContract
}

func (e *transcriptPromptContractMismatch) Error() string {
	return fmt.Sprintf("prompt %q immutable contract mismatch", e.PromptID)
}

func prepareTranscriptPromptReconciliation(
	ownership transcriptPromptOwnershipSnapshot,
	message clientui.TranscriptMessage,
) (transcriptPromptReconciliation, bool, error) {
	var (
		incoming []clientui.TranscriptPrompt
		source   clientui.AttentionNotificationSource
		mode     clientui.TranscriptMessageKind
	)
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		incoming = message.Payload.Hydration.PendingPrompts
		source = clientui.AttentionNotificationSourceSnapshot
		mode = message.Kind
	case clientui.TranscriptMessagePromptPending:
		incoming = []clientui.TranscriptPrompt{*message.Payload.PromptPending}
		source = clientui.AttentionNotificationSourceLive
		mode = message.Kind
	case clientui.TranscriptMessagePromptResolved:
		incoming = []clientui.TranscriptPrompt{*message.Payload.PromptResolved}
		source = clientui.AttentionNotificationSourceLive
		mode = message.Kind
	default:
		return transcriptPromptReconciliation{}, false, nil
	}

	existingOrder := make([]clientui.PromptID, 0, len(ownership.events))
	existing := make(map[clientui.PromptID]askEvent, len(ownership.events))
	plan := transcriptPromptReconciliation{source: source}
	for _, event := range ownership.events {
		id := event.prompt.PromptID
		if _, duplicate := existing[id]; duplicate {
			plan.canceled = append(plan.canceled, event)
			continue
		}
		existing[id] = event
		existingOrder = append(existingOrder, id)
	}

	switch mode {
	case clientui.TranscriptMessageHydration:
		byID := make(map[clientui.PromptID]clientui.TranscriptPrompt, len(incoming))
		for _, prompt := range incoming {
			byID[prompt.PromptID] = prompt
		}
		for _, id := range existingOrder {
			event := existing[id]
			prompt, present := byID[id]
			if !present {
				plan.canceled = append(plan.canceled, event)
				continue
			}
			if mismatch := compareTranscriptPromptContract(event.prompt, prompt); mismatch != nil {
				return transcriptPromptReconciliation{}, true, mismatch
			}
		}
		ordered := append([]clientui.TranscriptPrompt(nil), incoming...)
		if len(ordered) > 1 &&
			ownership.currentPromptID != nil &&
			ordered[0].PromptID != *ownership.currentPromptID &&
			ownership.retention != transcriptPromptRetentionNone {
			for index, prompt := range ordered {
				if prompt.PromptID != *ownership.currentPromptID {
					continue
				}
				ordered = append([]clientui.TranscriptPrompt{prompt}, append(ordered[:index], ordered[index+1:]...)...)
				break
			}
		}
		for _, prompt := range ordered {
			id := prompt.PromptID
			if event, retained := existing[id]; retained {
				plan.events = append(plan.events, retainedTranscriptPromptEvent(event, prompt))
			} else {
				plan.events = append(plan.events, newTranscriptPromptEvent(prompt))
			}
		}
	case clientui.TranscriptMessagePromptPending:
		prompt := incoming[0]
		targetID := prompt.PromptID
		found := false
		for _, id := range existingOrder {
			event := existing[id]
			if id == targetID {
				if mismatch := compareTranscriptPromptContract(event.prompt, prompt); mismatch != nil {
					return transcriptPromptReconciliation{}, true, mismatch
				}
				plan.events = append(plan.events, retainedTranscriptPromptEvent(event, prompt))
				found = true
				continue
			}
			plan.events = append(plan.events, retainedTranscriptPromptEvent(event, event.prompt))
		}
		if !found {
			plan.events = append(plan.events, newTranscriptPromptEvent(prompt))
		}
	case clientui.TranscriptMessagePromptResolved:
		targetID := incoming[0].PromptID
		for _, id := range existingOrder {
			event := existing[id]
			if id == targetID {
				if mismatch := compareTranscriptPromptContract(event.prompt, incoming[0]); mismatch != nil {
					return transcriptPromptReconciliation{}, true, mismatch
				}
				plan.canceled = append(plan.canceled, event)
				continue
			}
			plan.events = append(plan.events, retainedTranscriptPromptEvent(event, event.prompt))
		}
	}

	plan.activeRetained = len(plan.events) > 0 &&
		plan.events[0].owned &&
		ownership.currentPromptID != nil &&
		plan.events[0].retained.prompt.PromptID == *ownership.currentPromptID
	return plan, true, nil
}

func compareTranscriptPromptContract(oldPrompt, newPrompt clientui.TranscriptPrompt) *transcriptPromptContractMismatch {
	oldContract := newTranscriptPromptContract(oldPrompt)
	newContract := newTranscriptPromptContract(newPrompt)
	if oldContract.SessionID == newContract.SessionID &&
		oldContract.PromptID == newContract.PromptID &&
		oldContract.Kind == newContract.Kind &&
		oldContract.StepID == newContract.StepID &&
		oldContract.CreatedAt.Equal(newContract.CreatedAt) &&
		equalToolProvenance(oldContract.Tool, newContract.Tool) {
		return nil
	}
	return &transcriptPromptContractMismatch{
		PromptID: oldPrompt.PromptID,
		Old:      oldContract,
		New:      newContract,
	}
}

func newTranscriptPromptContract(prompt clientui.TranscriptPrompt) transcriptPromptContract {
	var tool *clientui.ToolProvenance
	if prompt.Tool != nil {
		cloned := *prompt.Tool
		tool = &cloned
	}
	return transcriptPromptContract{
		PromptID:  prompt.PromptID,
		SessionID: prompt.SessionID.String(),
		Kind:      prompt.Kind,
		StepID:    prompt.StepID.String(),
		CreatedAt: prompt.CreatedAt,
		Tool:      tool,
	}
}

func equalToolProvenance(left, right *clientui.ToolProvenance) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ToolCallID == right.ToolCallID && left.ToolName == right.ToolName
}

func retainedTranscriptPromptEvent(event askEvent, prompt clientui.TranscriptPrompt) transcriptPromptReconciliationEvent {
	return transcriptPromptReconciliationEvent{
		prompt:   cloneTranscriptPromptForAsk(prompt),
		retained: event,
		owned:    true,
	}
}

func newTranscriptPromptEvent(prompt clientui.TranscriptPrompt) transcriptPromptReconciliationEvent {
	return transcriptPromptReconciliationEvent{
		prompt: cloneTranscriptPromptForAsk(prompt),
		notify: true,
	}
}

func (r transcriptPromptReconciliation) pendingPrompts() []clientui.TranscriptPrompt {
	prompts := make([]clientui.TranscriptPrompt, 0, len(r.events))
	for _, event := range r.events {
		prompts = append(prompts, cloneTranscriptPromptForAsk(event.prompt))
	}
	return prompts
}

func (m *uiModel) snapshotTranscriptPromptOwnership() transcriptPromptOwnershipSnapshot {
	if m == nil {
		return transcriptPromptOwnershipSnapshot{}
	}
	snapshot := transcriptPromptOwnershipSnapshot{}
	if m.ask.hasCurrent() {
		currentPromptID := m.ask.current.promptID()
		snapshot.currentPromptID = &currentPromptID
		switch {
		case m.ask.answerPending || m.ask.activeDelivery != nil:
			snapshot.retention = transcriptPromptRetentionAnswerPending
		case strings.TrimSpace(m.ask.editor.Text()) != "":
			snapshot.retention = transcriptPromptRetentionMeaningfulDraft
		}
		snapshot.events = append(snapshot.events, *m.ask.current)
	}
	snapshot.events = append(snapshot.events, m.ask.queue...)
	return snapshot
}

func (m *uiModel) commitTranscriptPromptReconciliation(reconciliation transcriptPromptReconciliation) {
	if m == nil {
		return
	}
	events := make([]askEvent, 0, len(reconciliation.events))
	for _, planned := range reconciliation.events {
		event := planned.retained
		if !planned.owned {
			event = m.transcriptPromptEvent(planned.prompt)
		}
		event.prompt = cloneTranscriptPromptForAsk(planned.prompt)
		events = append(events, event)
	}
	for _, event := range reconciliation.canceled {
		m.clearApprovalCommentaryAnswer(event.promptID())
		if m.ask.activeDelivery != nil && m.ask.activeDelivery.key.promptID == event.promptID() {
			m.askController().cancelActiveDelivery()
		}
	}
	if reconciliation.activeRetained &&
		len(reconciliation.events) > 0 &&
		!m.ask.answerPending &&
		m.ask.activeDelivery == nil {
		m.normalizeRetainedPromptInteraction(
			reconciliation.events[0].retained.prompt,
			reconciliation.events[0].prompt,
		)
	}
	m.askController().replaceTranscriptPromptEvents(events, reconciliation.activeRetained)
	for _, planned := range reconciliation.events {
		if planned.notify {
			notifyTranscriptPromptFallback(m.promptAttention, planned.prompt, reconciliation.source)
		}
	}
}

func (m *uiModel) normalizeRetainedPromptInteraction(oldPrompt, newPrompt clientui.TranscriptPrompt) {
	if transcriptPromptIsApproval(oldPrompt) {
		decision, selected := selectedApprovalDecision(oldPrompt, m.ask.cursor)
		if selected {
			for index, candidate := range newPrompt.ApprovalOptions {
				if candidate == decision {
					m.ask.cursor = index
					return
				}
			}
		}
		m.ask.cursor = 0
		m.ask.freeform = false
		m.ask.freeformMode = askFreeformModeGeneric
		m.clearAskInput()
		return
	}
	if slices.Equal(oldPrompt.Suggestions, newPrompt.Suggestions) {
		return
	}
	if strings.TrimSpace(m.ask.editor.Text()) != "" {
		m.ask.cursor = len(newPrompt.Suggestions)
		m.ask.freeform = true
		m.ask.freeformMode = askFreeformModeGeneric
		return
	}
	m.ask.cursor = 0
	m.ask.freeform = len(newPrompt.Suggestions) == 0
	m.ask.freeformMode = askFreeformModeGeneric
	m.clearAskInput()
}

func (c uiAskController) replaceTranscriptPromptEvents(events []askEvent, preserveCurrentInteraction bool) {
	m := c.model
	if len(events) == 0 {
		c.cancelActiveDelivery()
		m.ask.current = nil
		m.ask.currentToken = nextNonZeroToken(m.ask.currentToken)
		m.ask.queue = nil
		m.ask.cursor = 0
		m.ask.answerPending = false
		m.clearAskInput()
		m.ask.freeform = false
		m.ask.freeformMode = askFreeformModeGeneric
		m.restorePrimaryInputMode()
		if m.activity == uiActivityQuestion {
			if m.isBusy() {
				m.activity = uiActivityRunning
			} else {
				m.activity = uiActivityIdle
			}
		}
		return
	}

	if preserveCurrentInteraction && m.ask.hasCurrent() {
		m.ask.current.prompt = cloneTranscriptPromptForAsk(events[0].prompt)
	} else {
		c.setActiveAsk(events[0])
	}
	m.ask.queue = append(m.ask.queue[:0], events[1:]...)
	m.activity = uiActivityQuestion
	if m.inputMode() == uiInputModeMain && (m.view.Mode() == "" || m.view.Mode() == tui.ModeOngoing) {
		m.setInputMode(uiInputModeAsk)
	}
}
