package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/transcript"
)

func (e *Engine) overlayLiveStreaming(snapshot *ChatSnapshot) {
	if e == nil || snapshot == nil {
		return
	}
	streaming, streamingErr := e.transcriptRuntimeState().StreamingSnapshot()
	snapshot.Streaming = streaming
	snapshot.StreamingError = streamingErr
}

func (e *Engine) recentTranscriptEntries(limit int) []ChatEntry {
	if e == nil || e.store == nil || limit <= 0 {
		return nil
	}
	window, err := e.store.ReadRecentEvents(limit)
	if err != nil {
		return nil
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{CacheWarningMode: e.cfg.CacheWarningMode})
	for _, evt := range window.Events {
		_ = scan.ApplyPersistedEvent(evt)
	}
	entries := scan.CollectedPageSnapshot().Entries
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

func (e *Engine) RecentTailTranscriptWindow(maxEntries int) TranscriptWindowSnapshot {
	if e == nil {
		return TranscriptWindowSnapshot{}
	}
	window := e.cachedRecentTailWindow(maxEntries)
	total := e.CommittedTranscriptEntryCount()
	window.TotalEntries = total
	if offset := total - len(window.Snapshot.Entries); offset >= 0 {
		window.Offset = offset
	}
	e.overlayLiveStreaming(&window.Snapshot)
	return window
}

func (e *Engine) cachedRecentTailWindow(maxEntries int) TranscriptWindowSnapshot {
	revision := e.TranscriptRevision()
	if cached, ok := e.recentTailCache.get(revision, maxEntries); ok {
		return cached
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{
		TrackRecentTail:  true,
		TailLimit:        maxEntries,
		CacheWarningMode: e.cfg.CacheWarningMode,
	})
	for _, evt := range e.activeListEvents() {
		_ = scan.ApplyPersistedEvent(evt)
	}
	window := scan.RecentTailSnapshot()
	e.recentTailCache.store(revision, maxEntries, window)
	return window
}

func (e *Engine) activeListEvents() []session.Event {
	if e == nil || e.store == nil {
		return nil
	}
	events, err := e.store.ReadEventsBackwardUntil(isCompactionSegmentBoundary)
	if err != nil {
		return nil
	}
	return events
}

type TranscriptSegmentPage struct {
	Snapshot                          ChatSnapshot
	OlderCursor                       int64
	HasMoreAbove                      bool
	NewerCursor                       int64
	HasMoreBelow                      bool
	LastCommittedAssistantFinalAnswer string
}

func isCompactionSegmentBoundary(evt session.Event) bool {
	if evt.Kind != sessionEventHistoryReplaced {
		return false
	}
	_, ignoredLegacy, err := decodePersistedHistoryReplacementPayload(evt.Payload)
	if err != nil {
		return false
	}
	return !ignoredLegacy
}

func TranscriptSegmentPageFromStore(store *session.Store, cursor int64, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	if store == nil {
		return TranscriptSegmentPage{}, nil
	}
	window, err := store.ReadSegmentBackward(cursor, isCompactionSegmentBoundary)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	return segmentPageFromWindow(window, cacheWarningMode)
}

func TranscriptSegmentPageForwardFromStore(store *session.Store, startOffset int64, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	if store == nil {
		return TranscriptSegmentPage{}, nil
	}
	window, err := store.ReadSegmentForward(startOffset, isCompactionSegmentBoundary)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	return segmentPageFromWindow(window, cacheWarningMode)
}

func segmentPageFromWindow(window session.SegmentWindow, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{CacheWarningMode: cacheWarningMode})
	for _, evt := range window.Events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			return TranscriptSegmentPage{}, err
		}
	}
	return TranscriptSegmentPage{
		Snapshot:                          scan.CollectedPageSnapshot(),
		OlderCursor:                       window.StartOffset,
		HasMoreAbove:                      !window.ReachedStart,
		NewerCursor:                       window.EndOffset,
		HasMoreBelow:                      !window.ReachedEnd,
		LastCommittedAssistantFinalAnswer: scan.LastCommittedAssistantFinalAnswer(),
	}, nil
}

func (e *Engine) TranscriptSegmentPage(cursor int64) (TranscriptSegmentPage, error) {
	if e == nil || e.store == nil {
		return TranscriptSegmentPage{}, nil
	}
	page, err := TranscriptSegmentPageFromStore(e.store, cursor, e.cfg.CacheWarningMode)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	if cursor <= 0 {
		e.overlayLiveStreaming(&page.Snapshot)
	}
	return page, nil
}

func (e *Engine) TranscriptSegmentPageForward(startOffset int64) (TranscriptSegmentPage, error) {
	if e == nil || e.store == nil {
		return TranscriptSegmentPage{}, nil
	}
	page, err := TranscriptSegmentPageForwardFromStore(e.store, startOffset, e.cfg.CacheWarningMode)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	if !page.HasMoreBelow {
		e.overlayLiveStreaming(&page.Snapshot)
	}
	return page, nil
}

func (e *Engine) TranscriptRevision() int64 {
	if e == nil || e.store == nil {
		return 0
	}
	return e.store.Meta().LastSequence
}

func (e *Engine) CommittedTranscriptEntryCount() int {
	if e == nil {
		return 0
	}
	return e.transcriptRuntimeState().CommittedEntryCount()
}

func (e *Engine) ActiveRun() *RunSnapshot {
	if e == nil || e.stepLifecycle == nil {
		return nil
	}
	return e.stepLifecycle.Snapshot()
}

func (e *Engine) LastCommittedAssistantFinalAnswer() string {
	if e == nil {
		return ""
	}
	return e.transcriptRuntimeState().LastCommittedAssistantFinalAnswer()
}

func messagePreservesLastCommittedAssistantFinalAnswer(message llm.Message) bool {
	if message.Role != llm.RoleDeveloper {
		return false
	}
	switch message.MessageType {
	case llm.MessageTypeCompactionSoonReminder, llm.MessageTypeErrorFeedback, llm.MessageTypeGoal, llm.MessageTypeHandoffFutureMessage, llm.MessageTypeReviewerFeedback:
		return true
	default:
		return false
	}
}

func (e *Engine) ContextUsage() ContextUsage {
	window := e.compactionPlannerState().contextWindowTokens(e.compactionPlanningSnapshot())
	used := e.currentTokenUsage()
	cacheHitPercent, hasCacheHitPercentage := e.usageTrackingState().CacheHitSnapshot()
	if used < 0 {
		used = 0
	}
	if window < 0 {
		window = 0
	}
	return ContextUsage{
		UsedTokens:            used,
		WindowTokens:          window,
		CacheHitPercent:       cacheHitPercent,
		HasCacheHitPercentage: hasCacheHitPercentage,
	}
}

func (e *Engine) AppendCommittedEntry(role, text string) error {
	return e.AppendCommittedEntryWithCondensedText(role, text, "")
}

func (e *Engine) AppendCommittedEntryWithVisibility(role, text string, visibility transcript.EntryVisibility) error {
	return e.appendCommittedEntry(storedLocalEntry{
		Visibility: transcript.NormalizeEntryVisibility(visibility),
		Role:       strings.TrimSpace(role),
		Text:       strings.TrimSpace(text),
	})
}

func (e *Engine) AppendCommittedEntryWithNoticeID(role, text, noticeID string) error {
	return e.appendCommittedEntry(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       strings.TrimSpace(role),
		Text:       strings.TrimSpace(text),
		NoticeID:   strings.TrimSpace(noticeID),
	})
}

func (e *Engine) AppendCommittedEntryWithCondensedText(role, text, condensedText string) error {
	return e.appendCommittedEntry(storedLocalEntry{
		Visibility:    transcript.EntryVisibilityAuto,
		Role:          strings.TrimSpace(role),
		Text:          strings.TrimSpace(text),
		CondensedText: strings.TrimSpace(condensedText),
	})
}

func (e *Engine) appendCommittedEntry(entry storedLocalEntry) error {
	if entry.Role == "" || entry.Text == "" {
		return nil
	}
	return e.steer("", steerLocalEntryIntent(entry))
}

func (e *Engine) SetStreamingError(text string) {
	newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).SetStreamingError(text)
	_ = e.steer("", steerEventIntent(Event{Kind: EventStreamingErrorUpdated}))
}

func (e *Engine) ReportPromptHistoryPersistError(reason string) {
	if e == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	_ = e.steer("", steerEventIntent(Event{Kind: EventPromptHistoryPersistFailed, Error: reason}))
}

func (e *Engine) ClearStreamingError() {
	newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).ClearStreamingError()
	_ = e.steer("", steerEventIntent(Event{Kind: EventStreamingErrorUpdated}))
}

func (e *Engine) SetSessionName(name string) error {
	return e.store.SetName(name)
}

func (e *Engine) SetThinkingLevel(level string) error {
	normalized, ok := NormalizeThinkingLevel(level)
	if !ok {
		return fmt.Errorf("invalid thinking level %q (expected low|medium|high|xhigh)", strings.TrimSpace(level))
	}
	e.mu.Lock()
	e.cfg.ThinkingLevel = normalized
	e.mu.Unlock()
	e.markCurrentRequestShapeDirty()
	return nil
}

func (e *Engine) SetFastModeEnabled(enabled bool) (bool, error) {
	if enabled && !e.FastModeAvailable() {
		return false, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	if state := e.fastModeState(); state != nil {
		changed := state.SetEnabled(enabled)
		if changed {
			e.markCurrentRequestShapeDirty()
		}
		return changed, nil
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed := e.localFastModeEnabledChange(enabled)
	e.applyFastModeEnabled(enabled)
	return changed, nil
}

func (e *Engine) SetFastModeEnabledWithCommittedFeedback(enabled bool, feedback func(changed bool) string) (bool, error) {
	if feedback == nil {
		return false, errors.New("committed feedback builder is required")
	}
	if enabled && !e.FastModeAvailable() {
		return false, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	if state := e.fastModeState(); state != nil {
		changed, err := state.SetEnabledWithTransaction(enabled, func(changed bool) error {
			text := strings.TrimSpace(feedback(changed))
			if text == "" {
				return errors.New("committed feedback text is required")
			}
			return e.AppendCommittedEntry("system", text)
		})
		if err != nil {
			return false, err
		}
		if changed {
			e.markCurrentRequestShapeDirty()
		}
		return changed, nil
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed := e.localFastModeEnabledChange(enabled)
	text := strings.TrimSpace(feedback(changed))
	if text == "" {
		return false, errors.New("committed feedback text is required")
	}
	if err := e.AppendCommittedEntry("system", text); err != nil {
		return false, err
	}
	e.applyFastModeEnabled(enabled)
	return changed, nil
}

func (e *Engine) fastModeState() *FastModeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.FastModeState
}

func (e *Engine) localFastModeEnabledChange(enabled bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.FastModeEnabled != enabled
}

func (e *Engine) applyFastModeEnabled(enabled bool) bool {
	e.mu.Lock()
	changed := false
	if e.cfg.FastModeEnabled != enabled {
		e.cfg.FastModeEnabled = enabled
		changed = true
	}
	e.mu.Unlock()
	if changed {
		e.markCurrentRequestShapeDirty()
	}
	return changed
}

func (e *Engine) SetAutoCompactionEnabled(enabled bool) (bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	current := true
	if e.cfg.AutoCompactionEnabled != nil {
		current = *e.cfg.AutoCompactionEnabled
	}
	if current == enabled {
		return false, current
	}
	if e.cfg.AutoCompactionEnabled == nil {
		e.cfg.AutoCompactionEnabled = new(bool)
	}
	*e.cfg.AutoCompactionEnabled = enabled
	return true, enabled
}

func (e *Engine) QuestionsEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.QuestionsEnabled == nil {
		return true
	}
	return *e.cfg.QuestionsEnabled
}

func (e *Engine) SetQuestionsEnabled(enabled bool) (bool, bool) {
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, current := e.questionsEnabledChange(enabled)
	if changed {
		e.applyQuestionsEnabled(enabled)
		current = enabled
	}
	return changed, current
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, error) {
	if feedback == nil {
		return false, e.QuestionsEnabled(), errors.New("committed feedback builder is required")
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, current := e.questionsEnabledChange(enabled)
	resultEnabled := current
	if changed {
		resultEnabled = enabled
	}
	text := strings.TrimSpace(feedback(resultEnabled, changed))
	if text == "" {
		return false, current, errors.New("committed feedback text is required")
	}
	if err := e.AppendCommittedEntry("system", text); err != nil {
		return false, current, err
	}
	if changed {
		e.applyQuestionsEnabled(enabled)
	}
	return changed, resultEnabled, nil
}

func (e *Engine) questionsEnabledChange(enabled bool) (bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	current := true
	if e.cfg.QuestionsEnabled != nil {
		current = *e.cfg.QuestionsEnabled
	}
	return current != enabled, current
}

func (e *Engine) applyQuestionsEnabled(enabled bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	current := true
	if e.cfg.QuestionsEnabled != nil {
		current = *e.cfg.QuestionsEnabled
	}
	if current == enabled {
		return false
	}
	if e.cfg.QuestionsEnabled == nil {
		e.cfg.QuestionsEnabled = new(bool)
	}
	*e.cfg.QuestionsEnabled = enabled
	return true
}

func (e *Engine) SetReviewerEnabled(enabled bool) (bool, string, error) {
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, mode, err := e.reviewerEnabledChange(enabled)
	if err != nil {
		return false, mode, err
	}
	e.applyReviewerEnabled(enabled, mode)
	return changed, mode, nil
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), errors.New("committed feedback builder is required")
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, mode, err := e.reviewerEnabledChange(enabled)
	if err != nil {
		return false, mode, err
	}
	text := strings.TrimSpace(feedback(mode != "off", mode, changed))
	if text == "" {
		return false, mode, errors.New("committed feedback text is required")
	}
	if err := e.AppendCommittedEntry("system", text); err != nil {
		return false, mode, err
	}
	e.applyReviewerEnabled(enabled, mode)
	return changed, mode, nil
}

func (e *Engine) reviewerEnabledChange(enabled bool) (bool, string, error) {
	e.mu.Lock()
	current, ok := NormalizeReviewerFrequency(e.cfg.Reviewer.Frequency)
	if !ok {
		current = "off"
	}
	if enabled {
		if current != "off" {
			e.mu.Unlock()
			return false, current, nil
		}
		e.mu.Unlock()
		if err := e.initReviewerClient(); err != nil {
			return false, current, err
		}
		e.mu.Lock()
		reviewerState := e.reviewerRuntimeStateLocked()
		target := reviewerState.ResumeFrequency("edits")
		e.mu.Unlock()
		return true, target, nil
	}

	if current == "off" {
		e.mu.Unlock()
		return false, current, nil
	}
	e.mu.Unlock()
	return true, "off", nil
}

func (e *Engine) applyReviewerEnabled(enabled bool, targetMode string) (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	current, ok := NormalizeReviewerFrequency(e.cfg.Reviewer.Frequency)
	if !ok {
		current = "off"
	}
	reviewerState := e.reviewerRuntimeStateLocked()
	if current != "off" {
		reviewerState.RecordResumeFrequency(current)
	}

	if enabled {
		if current != "off" {
			return false, current
		}
		target, ok := NormalizeReviewerFrequency(targetMode)
		if !ok || target == "off" {
			target = reviewerState.ResumeFrequency("edits")
		}
		e.cfg.Reviewer.Frequency = target
		return true, target
	}

	if current == "off" {
		return false, current
	}
	e.cfg.Reviewer.Frequency = "off"
	return true, "off"
}

func (e *Engine) ThinkingLevel() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.TrimSpace(e.cfg.ThinkingLevel)
}

func (e *Engine) FastModeEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.FastModeState != nil {
		return e.cfg.FastModeState.Enabled()
	}
	return e.cfg.FastModeEnabled
}

func (e *Engine) FastModeAvailable() bool {
	caps, err := e.providerCapabilities(context.Background())
	if err != nil {
		return false
	}
	return llm.SupportsFastModeProvider(caps)
}

func (e *Engine) ReviewerFrequency() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	normalized, ok := NormalizeReviewerFrequency(e.cfg.Reviewer.Frequency)
	if !ok {
		return "off"
	}
	return normalized
}

func (e *Engine) reviewerMetaTimestamp() time.Time {
	if e == nil || e.store == nil {
		return time.Now().UTC()
	}
	if createdAt := e.store.Meta().CreatedAt; !createdAt.IsZero() {
		return createdAt.UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) ReviewerEnabled() bool {
	return e.ReviewerFrequency() != "off"
}

func (e *Engine) AutoCompactionEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.AutoCompactionEnabled == nil {
		return true
	}
	return *e.cfg.AutoCompactionEnabled
}

func (e *Engine) CompactionMode() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compactionPlannerState().mode(e.cfg.CompactionMode)
}

func (e *Engine) initReviewerClient() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reviewerRuntimeStateLocked().EnsureClient(e.cfg.Reviewer.ClientFactory)
}

func (e *Engine) reviewerTurnConfigSnapshot() (string, llm.Client) {
	e.mu.Lock()
	reviewerState := e.reviewerRuntimeStateLocked()
	normalized, ok := NormalizeReviewerFrequency(e.cfg.Reviewer.Frequency)
	if !ok {
		normalized = "off"
	}
	e.mu.Unlock()
	return normalized, reviewerState.Client()
}

func (e *Engine) reviewerRequestConfigSnapshot() reviewerRequestConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return reviewerRequestConfig{
		Model:             strings.TrimSpace(e.cfg.Reviewer.Model),
		ThinkingLevel:     strings.TrimSpace(e.cfg.Reviewer.ThinkingLevel),
		ModelCapabilities: e.cfg.Reviewer.ModelCapabilities,
	}
}

func (e *Engine) SessionName() string {
	return strings.TrimSpace(e.store.Meta().Name)
}

func (e *Engine) SessionID() string {
	return strings.TrimSpace(e.store.Meta().SessionID)
}

func conversationPromptCacheKey(sessionID string, compactionCount int) string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return ""
	}
	if compactionCount <= 0 {
		return trimmed
	}
	return fmt.Sprintf("%s/compact-%d", trimmed, compactionCount)
}

func (e *Engine) ParentSessionID() string {
	return strings.TrimSpace(e.store.Meta().ParentSessionID)
}

func (e *Engine) SetTranscriptWorkingDir(workdir string) {
	if e == nil {
		return
	}
	e.transcriptRuntimeState().SetWorkingDir(workdir)
}

func (e *Engine) transcriptWorkingDir() string {
	if e == nil {
		return ""
	}
	return e.transcriptRuntimeState().WorkingDir()
}

func transcriptWorkingDir(primary string, fallback string) string {
	if trimmed := strings.TrimSpace(primary); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}

func (e *Engine) ConversationFreshness() session.ConversationFreshness {
	return e.store.ConversationFreshness()
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

type storedLocalEntry struct {
	Visibility    transcript.EntryVisibility `json:"visibility,omitempty"`
	Role          string                     `json:"role"`
	Text          string                     `json:"text"`
	CondensedText string                     `json:"condensed_text,omitempty"`
	DiagnosticKey string                     `json:"diagnostic_key,omitempty"`
	NoticeID      string                     `json:"notice_id,omitempty"`
}

type historyReplacementPayload struct {
	Engine string `json:"engine"`
	Mode   string `json:"mode"`
	// WorkflowRunID records the workflow run whose runtime committed this history
	// replacement, when the engine runs under a workflow run. It is the durable,
	// single-write provenance of a compaction: resume reconstructs it from this
	// event so a workflow run never recompacts a continuation it already committed.
	WorkflowRunID                     string             `json:"workflow_run_id,omitempty"`
	CompactionNumber                  int                `json:"compaction_number,omitempty"`
	PendingHandoffFutureMessage       string             `json:"pending_handoff_future_message,omitempty"`
	LastCommittedAssistantFinalAnswer string             `json:"last_committed_assistant_final_answer,omitempty"`
	Items                             []llm.ResponseItem `json:"items"`
}

func (e *Engine) setLastUsage(usage llm.Usage) {
	baselineEstimate := 0
	if e != nil {
		baselineEstimate = e.transcriptRuntimeState().EstimatedProviderTokens()
	}
	normalizedUsage, totalInputTokens, totalCachedInputTokens := e.usageTrackingState().Next(usage)
	e.applyUsageTrackingState(normalizedUsage, baselineEstimate, totalInputTokens, totalCachedInputTokens)
}

func (e *Engine) recordLastUsage(usage llm.Usage) error {
	baselineEstimate := 0
	if e != nil {
		baselineEstimate = e.transcriptRuntimeState().EstimatedProviderTokens()
	}
	normalizedUsage, totalInputTokens, totalCachedInputTokens := e.usageTrackingState().Next(usage)
	if e != nil && e.store != nil {
		if err := e.store.SetUsageState(&session.UsageState{
			InputTokens:             normalizedUsage.InputTokens,
			OutputTokens:            normalizedUsage.OutputTokens,
			WindowTokens:            normalizedUsage.WindowTokens,
			CachedInputTokens:       normalizedUsage.CachedInputTokens,
			HasCachedInputTokens:    normalizedUsage.HasCachedInputTokens,
			EstimatedProviderTokens: baselineEstimate,
			TotalInputTokens:        totalInputTokens,
			TotalCachedInputTokens:  totalCachedInputTokens,
		}); err != nil {
			return err
		}
	}
	e.applyUsageTrackingState(normalizedUsage, baselineEstimate, totalInputTokens, totalCachedInputTokens)
	return nil
}

func (e *Engine) restorePersistedUsageState(state *session.UsageState) {
	if e == nil || state == nil {
		return
	}
	normalized := normalizePersistedUsageTrackingState(*state)
	e.applyUsageTrackingState(
		llm.Usage{
			InputTokens:          normalized.InputTokens,
			OutputTokens:         normalized.OutputTokens,
			WindowTokens:         normalized.WindowTokens,
			CachedInputTokens:    normalized.CachedInputTokens,
			HasCachedInputTokens: normalized.HasCachedInputTokens,
		},
		normalized.EstimatedProviderTokens,
		normalized.TotalInputTokens,
		normalized.TotalCachedInputTokens,
	)
}

func (e *Engine) applyUsageTrackingState(usage llm.Usage, baselineEstimate, totalInputTokens, totalCachedInputTokens int) {
	if baselineEstimate < 0 {
		baselineEstimate = 0
	}
	e.usageTrackingState().Apply(usage, totalInputTokens, totalCachedInputTokens)
	if e.modelRequests().TokenUsage() != nil {
		e.modelRequests().TokenUsage().storeUsageBaseline(usage.InputTokens, baselineEstimate)
	}
}

func (e *Engine) usageTrackingState() *usageTrackingState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.usageState == nil {
		e.usageState = newUsageTrackingState()
	}
	return e.usageState
}

func (e *Engine) reviewerRuntimeState() *reviewerRuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reviewerRuntimeStateLocked()
}

func (e *Engine) reviewerRuntimeStateLocked() *reviewerRuntimeState {
	if e.reviewerState == nil {
		e.reviewerState = newReviewerRuntimeState(e.cfg.Reviewer.Client)
	}
	return e.reviewerState
}

func (e *Engine) transcriptRuntimeState() *transcriptRuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.transcriptState == nil {
		e.transcriptState = newTranscriptRuntimeState(transcriptWorkingDir(e.cfg.TranscriptWorkingDir, e.store.Meta().WorkspaceRoot))
	}
	return e.transcriptState
}

func (e *Engine) lockedContractState() *lockedContractState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lockedState == nil {
		e.lockedState = newLockedContractState()
	}
	return e.lockedState
}

func (e *Engine) modelRequests() *modelRequestRuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.modelRequestsState == nil {
		e.modelRequestsState = newModelRequestRuntimeState()
	}
	return e.modelRequestsState
}

func (e *Engine) emitRaw(evt Event) {
	evt.TranscriptRevision = e.TranscriptRevision()
	carriesCommittedRange := eventShouldCarryCommittedEntryCount(evt)
	if !carriesCommittedRange {
		evt.CommittedEntryCount = 0
		evt.CommittedEntryStart = 0
		evt.CommittedEntryStartSet = false
	}
	if evt.CommittedEntryCount == 0 && carriesCommittedRange {
		evt.CommittedEntryCount = e.CommittedTranscriptEntryCount()
	}
	if evt.ContextUsage == nil && eventShouldCarryContextUsage(evt) {
		usage := e.ContextUsage()
		evt.ContextUsage = &usage
	}
	if !evt.CommittedEntryStartSet && eventMayInferCommittedEntryStart(evt.Kind) {
		entries := TranscriptEntriesFromEvent(evt)
		if len(entries) > 0 {
			start := evt.CommittedEntryCount - len(entries)
			if start < 0 {
				start = 0
			}
			evt.CommittedEntryStart = start
			evt.CommittedEntryStartSet = true
		}
	}
	if e.cfg.OnEvent != nil {
		e.cfg.OnEvent(evt)
	}
}

func eventShouldCarryContextUsage(evt Event) bool {
	switch evt.Kind {
	case EventModelResponse, EventUserMessageFlushed, EventCompactionCompleted, EventCompactionFailed:
		return true
	case EventAssistantMessage, EventToolCallStarted, EventToolCallCompleted, EventLocalEntryAdded, EventCacheWarning, EventConversationUpdated:
		return evt.CommittedTranscriptChanged
	default:
		return false
	}
}

func eventShouldCarryCommittedEntryCount(evt Event) bool {
	switch evt.Kind {
	case EventBackgroundUpdated:
		return false
	default:
		return true
	}
}

func eventMayInferCommittedEntryStart(kind EventKind) bool {
	switch kind {
	case EventCompactionCompleted, EventCompactionFailed, EventBackgroundUpdated:
		return false
	default:
		return true
	}
}

func (e *Engine) rememberPendingToolCallStarts(starts map[string]int) {
	if e == nil {
		return
	}
	e.pendingToolCallStartStore().Remember(starts)
}

func (e *Engine) pendingToolCallStart(callID string) (int, bool) {
	if e == nil {
		return 0, false
	}
	return e.pendingToolCallStartStore().Lookup(callID)
}

func (e *Engine) forgetPendingToolCallStart(callID string) {
	if e == nil {
		return
	}
	e.pendingToolCallStartStore().Forget(callID)
}

func (e *Engine) pendingToolCallStartStore() *pendingToolCallStartStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.toolCallStarts == nil {
		e.toolCallStarts = newPendingToolCallStartStore()
	}
	return e.toolCallStarts
}

// LastCompactionWorkflowRunID reports the workflow run that committed the most
// recent history replacement in this session, reconstructed from the
// history_replaced event on restore. Empty when no compaction has run under a
// workflow run. Workflow continuation gating reads this to compact exactly once.
func (e *Engine) LastCompactionWorkflowRunID() string {
	return e.compactionRuntimeState().LastWorkflowRunID()
}

func (e *Engine) compactionRuntimeState() *compactionRuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.compactionState == nil {
		e.compactionState = newCompactionRuntimeState()
	}
	return e.compactionState
}
