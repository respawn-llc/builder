package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/transcript"
)

const (
	sessionEventCacheRequestObserved  = "cache_request_observed"
	sessionEventCacheResponseObserved = "cache_response_observed"
	sessionEventCacheWarning          = "cache_warning"
	sessionEventHistoryReplaced       = "history_replaced"
	cacheWarningTranscriptRole        = "cache_warning"
	requestCacheDigestVersion         = 1
)

type persistedCacheRequestObserved struct {
	DigestVersion int                          `json:"digest_version,omitempty"`
	CacheKey      string                       `json:"cache_key"`
	Scope         transcript.CacheWarningScope `json:"scope,omitempty"`
	ChunkCount    int                          `json:"chunk_count"`
	TerminalHash  string                       `json:"terminal_hash"`
}

type persistedCacheResponseObserved struct {
	DigestVersion        int                          `json:"digest_version,omitempty"`
	CacheKey             string                       `json:"cache_key"`
	Scope                transcript.CacheWarningScope `json:"scope,omitempty"`
	ChunkCount           int                          `json:"chunk_count"`
	TerminalHash         string                       `json:"terminal_hash"`
	HasCachedInputTokens bool                         `json:"has_cached_input_tokens,omitempty"`
	CachedInputTokens    int                          `json:"cached_input_tokens,omitempty"`
}

type requestCacheLineage struct {
	request               persistedCacheRequestObserved
	lastResponseHadReuse  bool
	lastCachedInputTokens int
	hasResponse           bool
	pendingCause          transcript.CacheWarningReason
}

type preparedCacheRequestObservation struct {
	request                   persistedCacheRequestObserved
	exactWarning              *transcript.CacheWarning
	previousHadReuse          bool
	previousCachedInputTokens int
	hasPreviousResponse       bool
}

type requestCacheTracker struct {
	mu      sync.Mutex
	lineage map[string]requestCacheLineage
}

func newRequestCacheTracker() *requestCacheTracker {
	return &requestCacheTracker{lineage: make(map[string]requestCacheLineage)}
}

func (t *requestCacheTracker) Prepare(req llm.Request) (preparedCacheRequestObservation, error) {
	cacheKey := strings.TrimSpace(req.PromptCacheKey)
	if cacheKey == "" {
		return preparedCacheRequestObservation{}, nil
	}
	shape, err := summarizePromptCacheRequest(req)
	if err != nil {
		return preparedCacheRequestObservation{}, err
	}
	request := persistedCacheRequestObserved{
		DigestVersion: requestCacheDigestVersion,
		CacheKey:      cacheKey,
		Scope:         req.PromptCacheScope,
		ChunkCount:    shape.chunkCount,
		TerminalHash:  shape.terminalHash,
	}
	observation := preparedCacheRequestObservation{request: request}

	t.mu.Lock()
	defer t.mu.Unlock()
	previous, ok := t.lineage[cacheKey]
	if !ok {
		return observation, nil
	}
	previousRequestDigestVersion := previous.request.DigestVersion
	if previousRequestDigestVersion <= 0 {
		previousRequestDigestVersion = requestCacheDigestVersion
	}
	if previousRequestDigestVersion != requestCacheDigestVersion {
		return observation, nil
	}
	observation.previousHadReuse = previous.lastResponseHadReuse
	observation.previousCachedInputTokens = previous.lastCachedInputTokens
	observation.hasPreviousResponse = previous.hasResponse
	if !shape.HasPrefix(previous.request.ChunkCount, previous.request.TerminalHash) {
		reason := transcript.CacheWarningReasonNonPostfix
		if strings.TrimSpace(string(previous.pendingCause)) != "" {
			reason = previous.pendingCause
		}
		warning := transcript.CacheWarning{Scope: request.Scope, Reason: reason, CacheKey: cacheKey}
		observation.exactWarning = &warning
	}
	return observation, nil
}

func (t *requestCacheTracker) RecordInvalidation(cacheKey string, reason transcript.CacheWarningReason) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" || strings.TrimSpace(string(reason)) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.lineage[cacheKey]
	if !ok {
		return
	}
	state.pendingCause = reason
	t.lineage[cacheKey] = state
}

func (t *requestCacheTracker) Clear(cacheKey string) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.lineage, cacheKey)
}

func (t *requestCacheTracker) RecordResponse(response persistedCacheResponseObserved) {
	cacheKey := strings.TrimSpace(response.CacheKey)
	if cacheKey == "" {
		return
	}
	hadReuse := response.HasCachedInputTokens && response.CachedInputTokens > 0
	cachedInputTokens := 0
	if response.HasCachedInputTokens && response.CachedInputTokens > 0 {
		cachedInputTokens = response.CachedInputTokens
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if response.DigestVersion <= 0 {
		response.DigestVersion = requestCacheDigestVersion
	}
	state := t.lineage[cacheKey]
	state.request = persistedCacheRequestObserved{
		DigestVersion: response.DigestVersion,
		CacheKey:      response.CacheKey,
		Scope:         response.Scope,
		ChunkCount:    response.ChunkCount,
		TerminalHash:  response.TerminalHash,
	}
	state.lastResponseHadReuse = hadReuse
	state.lastCachedInputTokens = cachedInputTokens
	state.hasResponse = true
	state.pendingCause = ""
	t.lineage[cacheKey] = state
}

func (e *Engine) observePromptCacheRequest(stepID string, prepared preparedCacheRequestObservation) error {
	if e == nil || e.modelRequests().RequestCache() == nil || strings.TrimSpace(prepared.request.CacheKey) == "" {
		return nil
	}
	events := make([]session.EventInput, 0, 1)
	events = append(events, session.EventInput{Kind: sessionEventCacheRequestObserved, Payload: prepared.request})
	if _, err := e.store.AppendTurnAtomic(stepID, events); err != nil {
		return err
	}
	return nil
}

func cacheWarningEntryVisibility(mode config.CacheWarningMode) transcript.EntryVisibility {
	if normalized, ok := normalizeCacheWarningMode(mode); ok && normalized == config.CacheWarningModeVerbose {
		return transcript.EntryVisibilityOngoing
	}
	return transcript.EntryVisibilityDetail
}

func (e *Engine) observePromptCacheResponse(stepID string, prepared preparedCacheRequestObservation, usage llm.Usage) error {
	if e == nil || e.modelRequests().RequestCache() == nil || strings.TrimSpace(prepared.request.CacheKey) == "" {
		return nil
	}
	response := persistedCacheResponseObserved{
		DigestVersion:        prepared.request.DigestVersion,
		CacheKey:             prepared.request.CacheKey,
		Scope:                prepared.request.Scope,
		ChunkCount:           prepared.request.ChunkCount,
		TerminalHash:         prepared.request.TerminalHash,
		HasCachedInputTokens: usage.HasCachedInputTokens,
		CachedInputTokens:    usage.CachedInputTokens,
	}
	events := make([]session.EventInput, 0, 3)
	var warning *transcript.CacheWarning
	if prepared.exactWarning != nil && e.cfg.CacheWarningMode != config.CacheWarningModeOff {
		lostInputTokens := lostCachedInputTokens(prepared, usage)
		if lostInputTokens > 0 {
			warning = prepared.exactWarning
			warning.LostInputTokens = lostInputTokens
			events = append(events, session.EventInput{Kind: sessionEventCacheWarning, Payload: warning})
		}
	} else if shouldWarnOnCacheReuseDrop(e.cfg.CacheWarningMode, prepared, usage) {
		lostInputTokens := lostCachedInputTokens(prepared, usage)
		if lostInputTokens > 0 {
			warning = &transcript.CacheWarning{Scope: prepared.request.Scope, Reason: transcript.CacheWarningReasonReuseDropped, CacheKey: prepared.request.CacheKey, LostInputTokens: lostInputTokens}
			events = append(events, session.EventInput{Kind: sessionEventCacheWarning, Payload: warning})
		}
	}
	events = append(events, session.EventInput{Kind: sessionEventCacheResponseObserved, Payload: response})
	if warning != nil {
		if err := e.steer(stepID, steerCacheObservationIntent(events, *warning, cacheWarningEntryVisibility(e.cfg.CacheWarningMode), true)); err != nil {
			return err
		}
	} else if _, err := e.store.AppendTurnAtomic(stepID, events); err != nil {
		return err
	}
	e.modelRequests().RequestCache().RecordResponse(response)
	return nil
}

func shouldWarnOnCacheReuseDrop(mode config.CacheWarningMode, prepared preparedCacheRequestObservation, usage llm.Usage) bool {
	if mode != config.CacheWarningModeVerbose {
		return false
	}
	if prepared.exactWarning != nil || !prepared.hasPreviousResponse || !prepared.previousHadReuse {
		return false
	}
	return usage.HasCachedInputTokens && usage.CachedInputTokens <= 0
}

func lostCachedInputTokens(prepared preparedCacheRequestObservation, usage llm.Usage) int {
	previous := prepared.previousCachedInputTokens
	if previous < 0 {
		previous = 0
	}
	current := 0
	if usage.HasCachedInputTokens && usage.CachedInputTokens > 0 {
		current = usage.CachedInputTokens
	}
	if previous <= current {
		return 0
	}
	return previous - current
}

func (e *Engine) restorePromptCacheRequest(payload []byte) error {
	var request persistedCacheRequestObserved
	if err := json.Unmarshal(payload, &request); err != nil {
		return fmt.Errorf("decode %s event: %w", sessionEventCacheRequestObserved, err)
	}
	return nil
}

func (e *Engine) restorePromptCacheResponse(payload []byte) error {
	var response persistedCacheResponseObserved
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("decode %s event: %w", sessionEventCacheResponseObserved, err)
	}
	if e.modelRequests().RequestCache() != nil {
		e.modelRequests().RequestCache().RecordResponse(response)
	}
	return nil
}

func copyCacheWarning(in *transcript.CacheWarning) *transcript.CacheWarning {
	if in == nil {
		return nil
	}
	copyWarning := *in
	return &copyWarning
}

type promptCacheRequestSummary struct {
	chunkCount   int
	terminalHash string
	prefixHashes []string
}

func (s promptCacheRequestSummary) HasPrefix(chunkCount int, terminalHash string) bool {
	if chunkCount < 0 || chunkCount >= len(s.prefixHashes) {
		return false
	}
	return s.prefixHashes[chunkCount] == strings.TrimSpace(terminalHash)
}

func summarizePromptCacheRequest(req llm.Request) (promptCacheRequestSummary, error) {
	chunks, err := promptCacheChunks(req)
	if err != nil {
		return promptCacheRequestSummary{}, err
	}
	prefixHashes := make([]string, 1, len(chunks)+1)
	state := make([]byte, sha256.Size)
	prefixHashes[0] = hex.EncodeToString(state)
	for _, chunk := range chunks {
		state = extendPromptCacheDigest(state, chunk)
		prefixHashes = append(prefixHashes, hex.EncodeToString(state))
	}
	return promptCacheRequestSummary{
		chunkCount:   len(chunks),
		terminalHash: prefixHashes[len(prefixHashes)-1],
		prefixHashes: prefixHashes,
	}, nil
}

func promptCacheChunks(req llm.Request) ([][]byte, error) {
	var structuredOutput *promptCacheStructuredOutput
	if req.StructuredOutput != nil {
		structuredOutput = &promptCacheStructuredOutput{
			Name:        req.StructuredOutput.Name,
			Description: req.StructuredOutput.Description,
			Strict:      req.StructuredOutput.Strict,
			Schema:      compactJSONRaw(req.StructuredOutput.Schema),
		}
	}
	metadata, err := json.Marshal(promptCacheMetadata{
		SystemPrompt:     req.SystemPrompt,
		Tools:            promptCacheTools(req.Tools),
		StructuredOutput: structuredOutput,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal prompt cache metadata: %w", err)
	}
	chunks := make([][]byte, 0, len(req.Items)+1)
	chunks = append(chunks, metadata)
	for _, item := range req.Items {
		encoded, err := json.Marshal(promptCacheItemFromResponse(item))
		if err != nil {
			return nil, fmt.Errorf("marshal prompt cache item: %w", err)
		}
		chunks = append(chunks, encoded)
	}
	return chunks, nil
}

type promptCacheMetadata struct {
	SystemPrompt     string                       `json:"system_prompt,omitempty"`
	Tools            []promptCacheTool            `json:"tools,omitempty"`
	StructuredOutput *promptCacheStructuredOutput `json:"structured_output,omitempty"`
}

type promptCacheTool struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema,omitempty"`
}

type promptCacheStructuredOutput struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Strict      bool   `json:"strict,omitempty"`
	Schema      string `json:"schema,omitempty"`
}

type promptCacheItem struct {
	Type             llm.ResponseItemType `json:"type,omitempty"`
	Role             llm.Role             `json:"role,omitempty"`
	MessageType      llm.MessageType      `json:"message_type,omitempty"`
	SourcePath       string               `json:"source_path,omitempty"`
	Phase            llm.MessagePhase     `json:"phase,omitempty"`
	ID               string               `json:"id,omitempty"`
	Name             string               `json:"name,omitempty"`
	CallID           string               `json:"call_id,omitempty"`
	Content          string               `json:"content,omitempty"`
	CompactContent   string               `json:"compact_content,omitempty"`
	ToolPresentation string               `json:"tool_presentation,omitempty"`
	Arguments        string               `json:"arguments,omitempty"`
	Output           string               `json:"output,omitempty"`
	ReasoningSummary []llm.ReasoningEntry `json:"reasoning_summary,omitempty"`
	EncryptedContent string               `json:"encrypted_content,omitempty"`
	Raw              string               `json:"raw,omitempty"`
}

func promptCacheTools(tools []llm.Tool) []promptCacheTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]promptCacheTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, promptCacheTool{
			Name:        tool.Name,
			Description: tool.Description,
			Schema:      compactJSONRaw(tool.Schema),
		})
	}
	return out
}

func promptCacheItemFromResponse(item llm.ResponseItem) promptCacheItem {
	return promptCacheItem{
		Type:             item.Type,
		Role:             item.Role,
		MessageType:      item.MessageType,
		SourcePath:       item.SourcePath,
		Phase:            item.Phase,
		ID:               item.ID,
		Name:             item.Name,
		CallID:           item.CallID,
		Content:          item.Content,
		CompactContent:   item.CompactContent,
		ToolPresentation: compactJSONRaw(item.ToolPresentation),
		Arguments:        compactJSONRaw(item.Arguments),
		Output:           compactJSONRaw(item.Output),
		ReasoningSummary: append([]llm.ReasoningEntry(nil), item.ReasoningSummary...),
		EncryptedContent: item.EncryptedContent,
		Raw:              compactJSONRaw(item.Raw),
	}
}

func compactJSONRaw(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err == nil {
		return compact.String()
	}
	return string(trimmed)
}

func extendPromptCacheDigest(previous []byte, chunk []byte) []byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte{requestCacheDigestVersion})
	_, _ = hash.Write(previous)
	length := make([]byte, 8)
	binary.BigEndian.PutUint64(length, uint64(len(chunk)))
	_, _ = hash.Write(length)
	_, _ = hash.Write(chunk)
	return hash.Sum(nil)
}
