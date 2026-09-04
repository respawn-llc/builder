package runtime

import (
	"container/list"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/shared/transcript"
)

type TranscriptLiveToolStart struct {
	StepID       string
	ToolCallID   string
	ToolName     string
	Presentation *transcript.ToolCallMeta
}

type transcriptLiveToolLedger struct {
	mu       sync.Mutex
	inFlight map[string]TranscriptLiveToolStart
	order    *list.List
	nodes    map[string]*list.Element
}

func newTranscriptLiveToolLedger() *transcriptLiveToolLedger {
	return &transcriptLiveToolLedger{
		inFlight: make(map[string]TranscriptLiveToolStart),
		order:    list.New(),
		nodes:    make(map[string]*list.Element),
	}
}

func (l *transcriptLiveToolLedger) RecordStart(start TranscriptLiveToolStart) error {
	if l == nil {
		return nil
	}
	normalized, err := normalizeTranscriptLiveToolStart(start)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight == nil {
		l.inFlight = make(map[string]TranscriptLiveToolStart)
	}
	if l.order == nil {
		l.order = list.New()
	}
	if l.nodes == nil {
		l.nodes = make(map[string]*list.Element)
	}
	if _, ok := l.inFlight[normalized.ToolCallID]; ok {
		return nil
	}
	l.inFlight[normalized.ToolCallID] = normalized
	l.nodes[normalized.ToolCallID] = l.order.PushBack(normalized.ToolCallID)
	return nil
}

func (l *transcriptLiveToolLedger) Complete(callID string) {
	if l == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	l.mu.Lock()
	delete(l.inFlight, callID)
	if node := l.nodes[callID]; node != nil {
		l.order.Remove(node)
		delete(l.nodes, callID)
	}
	l.mu.Unlock()
}

func (l *transcriptLiveToolLedger) Snapshot() []TranscriptLiveToolStart {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.order == nil || l.order.Len() == 0 {
		return nil
	}
	out := make([]TranscriptLiveToolStart, 0, l.order.Len())
	for node := l.order.Front(); node != nil; node = node.Next() {
		callID := node.Value.(string)
		if start, ok := l.inFlight[callID]; ok {
			out = append(out, cloneTranscriptLiveToolStart(start))
		}
	}
	return out
}

func (l *transcriptLiveToolLedger) Lookup(callID string) (TranscriptLiveToolStart, bool) {
	if l == nil {
		return TranscriptLiveToolStart{}, false
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return TranscriptLiveToolStart{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	start, ok := l.inFlight[callID]
	if !ok {
		return TranscriptLiveToolStart{}, false
	}
	return cloneTranscriptLiveToolStart(start), true
}

func (l *transcriptLiveToolLedger) Seed(starts []TranscriptLiveToolStart) {
	if l == nil || len(starts) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight == nil {
		l.inFlight = make(map[string]TranscriptLiveToolStart, len(starts))
	}
	if l.order == nil {
		l.order = list.New()
	}
	if l.nodes == nil {
		l.nodes = make(map[string]*list.Element)
	}
	for _, start := range starts {
		normalized, err := normalizeTranscriptLiveToolStart(start)
		if err != nil {
			continue
		}
		if _, exists := l.inFlight[normalized.ToolCallID]; !exists {
			l.inFlight[normalized.ToolCallID] = normalized
			l.nodes[normalized.ToolCallID] = l.order.PushBack(normalized.ToolCallID)
		}
	}
}

func normalizeTranscriptLiveToolStart(start TranscriptLiveToolStart) (TranscriptLiveToolStart, error) {
	normalized := TranscriptLiveToolStart{
		StepID:       strings.TrimSpace(start.StepID),
		ToolCallID:   strings.TrimSpace(start.ToolCallID),
		ToolName:     strings.TrimSpace(start.ToolName),
		Presentation: cloneTranscriptToolCallMeta(start.Presentation),
	}
	if normalized.ToolCallID == "" {
		return TranscriptLiveToolStart{}, ErrMissingProviderToolCallID
	}
	return normalized, nil
}

func transcriptLiveToolStartFromCall(stepID string, call llm.ToolCall) TranscriptLiveToolStart {
	start, err := transcriptLiveToolStartFromCallChecked(stepID, call)
	if err != nil {
		panic(err)
	}
	return start
}

func transcriptLiveToolStartFromCallChecked(stepID string, call llm.ToolCall) (TranscriptLiveToolStart, error) {
	presentation, err := transcriptToolCallMetaChecked(call, "")
	if err != nil {
		return TranscriptLiveToolStart{}, fmt.Errorf("project live tool start: %w", err)
	}
	return TranscriptLiveToolStart{
		StepID:       strings.TrimSpace(stepID),
		ToolCallID:   strings.TrimSpace(call.ID),
		ToolName:     strings.TrimSpace(call.Name),
		Presentation: presentation,
	}, nil
}

func cloneTranscriptLiveToolStart(start TranscriptLiveToolStart) TranscriptLiveToolStart {
	return TranscriptLiveToolStart{
		StepID:       start.StepID,
		ToolCallID:   start.ToolCallID,
		ToolName:     start.ToolName,
		Presentation: cloneTranscriptToolCallMeta(start.Presentation),
	}
}

func cloneThinkingStatusState(state *TranscriptThinkingStatusState) *TranscriptThinkingStatusState {
	if state == nil {
		return nil
	}
	return &TranscriptThinkingStatusState{
		StepID: state.StepID,
		Text:   state.Text,
	}
}

func cloneTranscriptReasoningTraceState(state *TranscriptReasoningTraceState) TranscriptReasoningTraceState {
	if state == nil {
		return TranscriptReasoningTraceState{}
	}
	out := TranscriptReasoningTraceState{
		StepID:    state.StepID,
		Source:    *llm.CloneReasoningSourceCoordinate(&state.Source),
		Text:      state.Text,
		startedAt: state.startedAt,
	}
	out.Identity.Provider = llm.CloneReasoningItemIdentity(state.Identity.Provider)
	out.ProviderMetadata = llm.CloneReasoningItemIdentity(state.ProviderMetadata)
	if state.Identity.Kent != nil {
		id := *state.Identity.Kent
		out.Identity.Kent = &id
	}
	return out
}

func cloneTranscriptReasoningTraceIdentity(identity *TranscriptReasoningTraceIdentity) *TranscriptReasoningTraceIdentity {
	if identity == nil {
		return nil
	}
	out := &TranscriptReasoningTraceIdentity{
		Provider: llm.CloneReasoningItemIdentity(identity.Provider),
	}
	if identity.Kent != nil {
		id := *identity.Kent
		out.Kent = &id
	}
	return out
}

func cloneTranscriptToolCallMeta(meta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	normalized := transcript.NormalizeToolCallMeta(*meta)
	return clonePersistedToolCallMeta(&normalized)
}
