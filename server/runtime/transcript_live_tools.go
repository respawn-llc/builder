package runtime

import (
	"strings"
	"sync"

	"core/server/llm"
	"core/shared/transcript"
)

type TranscriptLiveToolStart struct {
	ToolCallID   string
	ToolName     string
	Presentation *transcript.ToolCallMeta
}

type transcriptLiveToolLedger struct {
	mu       sync.Mutex
	inFlight map[string]TranscriptLiveToolStart
}

func newTranscriptLiveToolLedger() *transcriptLiveToolLedger {
	return &transcriptLiveToolLedger{inFlight: make(map[string]TranscriptLiveToolStart)}
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
	if _, ok := l.inFlight[normalized.ToolCallID]; ok {
		return nil
	}
	l.inFlight[normalized.ToolCallID] = normalized
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
	l.mu.Unlock()
}

func (l *transcriptLiveToolLedger) AbortAll() []TranscriptLiveToolStart {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.inFlight) == 0 {
		return nil
	}
	out := make([]TranscriptLiveToolStart, 0, len(l.inFlight))
	for callID, start := range l.inFlight {
		out = append(out, cloneTranscriptLiveToolStart(start))
		delete(l.inFlight, callID)
	}
	return out
}

func (l *transcriptLiveToolLedger) Snapshot() []TranscriptLiveToolStart {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.inFlight) == 0 {
		return nil
	}
	out := make([]TranscriptLiveToolStart, 0, len(l.inFlight))
	for _, start := range l.inFlight {
		out = append(out, cloneTranscriptLiveToolStart(start))
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
	for _, start := range starts {
		normalized, err := normalizeTranscriptLiveToolStart(start)
		if err != nil {
			continue
		}
		if _, exists := l.inFlight[normalized.ToolCallID]; !exists {
			l.inFlight[normalized.ToolCallID] = normalized
		}
	}
}

func normalizeTranscriptLiveToolStart(start TranscriptLiveToolStart) (TranscriptLiveToolStart, error) {
	normalized := TranscriptLiveToolStart{
		ToolCallID:   strings.TrimSpace(start.ToolCallID),
		ToolName:     strings.TrimSpace(start.ToolName),
		Presentation: cloneTranscriptToolCallMeta(start.Presentation),
	}
	if normalized.ToolCallID == "" {
		return TranscriptLiveToolStart{}, ErrMissingProviderToolCallID
	}
	return normalized, nil
}

func transcriptLiveToolStartFromCall(call llm.ToolCall) TranscriptLiveToolStart {
	return TranscriptLiveToolStart{
		ToolCallID:   strings.TrimSpace(call.ID),
		ToolName:     strings.TrimSpace(call.Name),
		Presentation: decodeToolCallMeta(call),
	}
}

func cloneTranscriptLiveToolStart(start TranscriptLiveToolStart) TranscriptLiveToolStart {
	return TranscriptLiveToolStart{
		ToolCallID:   start.ToolCallID,
		ToolName:     start.ToolName,
		Presentation: cloneTranscriptToolCallMeta(start.Presentation),
	}
}

func cloneTranscriptToolCallMeta(meta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	normalized := transcript.NormalizeToolCallMeta(*meta)
	return &normalized
}
