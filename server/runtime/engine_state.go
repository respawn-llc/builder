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
	"core/server/workflow"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"
)

func (e *Engine) overlayLiveStreaming(snapshot *ChatSnapshot) {
	if e == nil || snapshot == nil {
		return
	}
	streaming, streamingErr, streamingMetadata := e.transcriptRuntimeState().StreamingSnapshot()
	snapshot.Streaming = streaming
	snapshot.StreamingMetadata = streamingMetadata
	snapshot.StreamingError = streamingErr
}

type TranscriptSegmentPage struct {
	Snapshot                          ChatSnapshot
	OlderCursor                       int64
	HasMoreAbove                      bool
	NewerCursor                       int64
	HasMoreBelow                      bool
	LatestRollbackCandidate           *rollbacktarget.CandidateLocator
	LastCommittedAssistantFinalAnswer *string
}

func TranscriptSegmentPageFromEventLog(eventLog session.MaterializedEventLog, cursor int64, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	if cursor <= 0 {
		return TranscriptSegmentPage{}, fmt.Errorf("transcript segment cursor must be positive, got %d", cursor)
	}
	var matchErr error
	window, err := eventLog.ReadSegmentBackward(cursor, compactionBoundaryMatcher(&matchErr))
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	if matchErr != nil {
		return TranscriptSegmentPage{}, matchErr
	}
	return segmentPageFromWindow(window, cacheWarningMode)
}

func TranscriptNewestSegmentPageFromEventLog(eventLog session.MaterializedEventLog, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	var matchErr error
	window, err := eventLog.ReadNewestSegmentBackward(compactionBoundaryMatcher(&matchErr))
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	if matchErr != nil {
		return TranscriptSegmentPage{}, matchErr
	}
	page, err := segmentPageFromWindow(window, cacheWarningMode)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	page.LatestRollbackCandidate, err = rollbackCandidateLocatorFromActiveWindow(window)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	return page, nil
}

func TranscriptSegmentPageForwardFromEventLog(eventLog session.MaterializedEventLog, startOffset int64, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	var matchErr error
	window, err := eventLog.ReadSegmentForward(
		startOffset,
		compactionBoundaryMatcher(&matchErr),
	)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	if matchErr != nil {
		return TranscriptSegmentPage{}, matchErr
	}
	return segmentPageFromWindow(window, cacheWarningMode)
}

func segmentPageFromWindow(window session.EventRecordWindow, cacheWarningMode config.CacheWarningMode) (TranscriptSegmentPage, error) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{CacheWarningMode: cacheWarningMode})
	for _, record := range window.Records {
		if err := scan.ApplyPersistedEvent(record); err != nil {
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
	page, err := TranscriptSegmentPageFromEventLog(e.eventLog, cursor, e.cfg.CacheWarningMode)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	page.LatestRollbackCandidate = e.transcriptRuntimeState().LatestRollbackCandidate()
	return page, nil
}

func (e *Engine) TranscriptNewestSegmentPage() (TranscriptSegmentPage, error) {
	if e == nil || e.store == nil {
		return TranscriptSegmentPage{}, nil
	}
	page, err := TranscriptNewestSegmentPageFromEventLog(e.eventLog, e.cfg.CacheWarningMode)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	page.LatestRollbackCandidate = e.transcriptRuntimeState().LatestRollbackCandidate()
	e.overlayLiveStreaming(&page.Snapshot)
	return page, nil
}

func (e *Engine) TranscriptSegmentPageForward(startOffset int64) (TranscriptSegmentPage, error) {
	if e == nil || e.store == nil {
		return TranscriptSegmentPage{}, nil
	}
	page, err := TranscriptSegmentPageForwardFromEventLog(e.eventLog, startOffset, e.cfg.CacheWarningMode)
	if err != nil {
		return TranscriptSegmentPage{}, err
	}
	page.LatestRollbackCandidate = e.transcriptRuntimeState().LatestRollbackCandidate()
	if !page.HasMoreBelow {
		e.overlayLiveStreaming(&page.Snapshot)
	}
	return page, nil
}

func (e *Engine) TranscriptRevision() (int64, error) {
	if e == nil || e.store == nil {
		return 0, errors.New("runtime engine is required")
	}
	return e.eventLog.Revision()
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

func (e *Engine) ActiveStepSnapshot() *RunSnapshot {
	return e.ActiveRun()
}

var ErrActiveStepInactive = errors.New("originating model step is no longer active")

func (e *Engine) ApplyForActiveStep(stepID string, apply func() error) error {
	if e == nil || e.stepLifecycle == nil {
		return ErrActiveStepInactive
	}
	return e.stepLifecycle.ApplyForActiveStep(stepID, apply)
}

func (e *Engine) LastCommittedAssistantFinalAnswer() *string {
	if e == nil {
		return nil
	}
	return e.transcriptRuntimeState().LastCommittedAssistantFinalAnswer()
}

func messagePreservesLastCommittedAssistantFinalAnswer(message llm.Message) bool {
	if message.Role != llm.RoleDeveloper {
		return false
	}
	if message.MessageType == nil {
		return false
	}
	switch *message.MessageType {
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
		Visibility: normalizeRuntimeEntryVisibility(visibility),
		Role:       strings.TrimSpace(role),
		Text:       strings.TrimSpace(text),
	})
}

func (e *Engine) AppendCommittedEntryWithNoticeID(role, text, noticeID string) error {
	return e.appendCommittedEntry(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       strings.TrimSpace(role),
		Text:       strings.TrimSpace(text),
		NoticeID:   textutil.OptionalTrimmedString(noticeID),
	})
}

func (e *Engine) AppendCommittedEntryWithCondensedText(role, text, condensedText string) error {
	return e.appendCommittedEntry(storedLocalEntry{
		Visibility:    transcript.EntryVisibilityAuto,
		Role:          strings.TrimSpace(role),
		Text:          strings.TrimSpace(text),
		CondensedText: textutil.OptionalTrimmedString(condensedText),
	})
}

func (e *Engine) appendCommittedEntry(entry storedLocalEntry) error {
	_, err := e.appendCommittedEntryWithCommitReceipt(entry)
	return err
}

func (e *Engine) SetStreamingError(text string) {
	e.transcriptRuntimeState().SetStreamingError(text)
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
	e.transcriptRuntimeState().ClearStreamingError()
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
	return e.setThinkingValue(normalized)
}

// SetWorkflowThinkingValue applies a workflow-owned thinking value. Workflow
// values may be standard Kent levels or provider-specific values, so they do
// not use the operator-config normalization contract.
func (e *Engine) SetWorkflowThinkingValue(value workflow.ThinkingValue) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return e.setThinkingValue(string(value))
}

// ClearWorkflowThinkingValue removes a workflow-owned thinking override while
// preserving the current prompt-cache lineage and contract generation.
func (e *Engine) ClearWorkflowThinkingValue() error {
	return e.setThinkingValue("")
}

func (e *Engine) setThinkingValue(value string) error {
	e.mu.Lock()
	e.cfg.ThinkingLevel = strings.TrimSpace(value)
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
	enabled := e.cfg.FastModeEnabled
	if e.cfg.FastModeState != nil {
		enabled = e.cfg.FastModeState.Enabled()
	}
	e.mu.Unlock()
	if !enabled {
		return false
	}
	// The shared state stores the user's preference; callers need the
	// provider-supported effective state so status and requests stay valid.
	return e.FastModeAvailable()
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

func (e *Engine) ContinuationAgentRole() *string {
	if e == nil || e.store == nil {
		return nil
	}
	return session.ContinuationAgentRole(e.store.Meta())
}

func conversationPromptCacheKey(sessionID string, compactionCount int) string {
	return conversationPromptCacheKeyForLineage(sessionID, 0, compactionCount)
}

// stablePromptCacheContractVersion namespaces provider cache entries by the
// stable prompt prefix that precedes the conversation transcript.
const stablePromptCacheContractVersion = 2

func conversationPromptCacheKeyForLineage(sessionID string, lineageGeneration, compactionCount int) string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return ""
	}
	trimmed = fmt.Sprintf("%s/prompt-contract-%d", trimmed, stablePromptCacheContractVersion)
	if lineageGeneration > 0 {
		trimmed = fmt.Sprintf("%s/contract-%d", trimmed, lineageGeneration)
	}
	if compactionCount <= 0 {
		return trimmed
	}
	return fmt.Sprintf("%s/compact-%d", trimmed, compactionCount)
}

func (e *Engine) conversationPromptCacheKey(sessionID string) string {
	if e == nil || e.store == nil {
		return ""
	}
	meta := e.store.Meta()
	return conversationPromptCacheKeyForLineage(sessionID, meta.PromptCacheLineageGeneration, e.compactionRuntimeState().Count())
}

func (e *Engine) PreviousSessionID() *runtimeids.SessionID {
	if e == nil || e.store == nil {
		return nil
	}
	meta := e.store.Meta()
	return textutil.Pointer(meta.PreviousSessionID)
}

func (e *Engine) ParentAgentSessionID() *runtimeids.SessionID {
	if e == nil || e.store == nil {
		return nil
	}
	meta := e.store.Meta()
	return textutil.Pointer(meta.ParentAgentSessionID)
}

func (e *Engine) NavigationTargetSessionID() *runtimeids.SessionID {
	if e == nil || e.store == nil {
		return nil
	}
	return session.NavigationTargetSessionID(e.store.Meta())
}

func (e *Engine) SetTranscriptWorkingDir(workdir string) {
	if e == nil {
		return
	}
	e.transcriptRuntimeState().SetWorkingDir(workdir)
}

func (e *Engine) TranscriptWorkingDir() string {
	return e.transcriptWorkingDir()
}

func (e *Engine) WorktreeReminderState() *session.WorktreeReminderState {
	if e == nil {
		return nil
	}
	state := e.store.Meta().WorktreeReminder
	if state == nil {
		return nil
	}
	copyState := *state
	return &copyState
}

func (e *Engine) SetWorktreeReminderState(state *session.WorktreeReminderState) error {
	if e == nil {
		return ErrEngineClosed
	}
	if e.closed.Load() {
		return ErrEngineClosed
	}
	return e.store.SetWorktreeReminderState(state)
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

func (e *Engine) ConversationFreshness() (session.ConversationFreshness, error) {
	if e == nil || e.store == nil {
		return session.ConversationFreshnessFresh, errors.New("runtime engine is required")
	}
	return e.eventLog.ConversationFreshness()
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

type storedLocalEntry struct {
	Visibility       transcript.EntryVisibility         `json:"visibility,omitempty"`
	Role             string                             `json:"role"`
	Text             string                             `json:"text"`
	DurationMs       *int64                             `json:"duration_ms,omitempty"`
	CondensedText    *string                            `json:"condensed_text,omitempty"`
	DiagnosticKey    *string                            `json:"diagnostic_key,omitempty"`
	NoticeID         *string                            `json:"notice_id,omitempty"`
	ToolOutputRepair *transcript.ToolOutputRepairNotice `json:"tool_output_repair,omitempty"`
	// AfterToolCallID keeps atomically persisted operator feedback visually
	// attached after the tool result that caused it.
	AfterToolCallID *string `json:"after_tool_call_id,omitempty"`
}

type historyReplacementPayload struct {
	Engine                            string                           `json:"engine"`
	Mode                              string                           `json:"mode"`
	CompactionNumber                  *int                             `json:"compaction_number,omitempty"`
	CommittedEntryStart               *int                             `json:"committed_entry_start,omitempty"`
	PendingHandoffFutureMessage       *string                          `json:"pending_handoff_future_message,omitempty"`
	LastCommittedAssistantFinalAnswer *string                          `json:"last_committed_assistant_final_answer,omitempty"`
	LatestRollbackCandidate           *rollbacktarget.CandidateLocator `json:"latest_rollback_candidate,omitempty"`
	Items                             []llm.ResponseItem               `json:"items"`
}

func (e *Engine) setLastUsage(usage llm.Usage) {
	baselineEstimate := 0
	if e != nil {
		baselineEstimate = e.transcriptRuntimeState().EstimatedProviderTokens()
	}
	normalizedUsage, totalInputTokens, totalCachedInputTokens := e.usageTrackingState().Next(usage)
	e.applyUsageTrackingState(normalizedUsage, baselineEstimate, totalInputTokens, totalCachedInputTokens)
}

func (e *Engine) recordLastUsage(usage llm.Usage) (session.CommitReceipt, error) {
	baselineEstimate := 0
	if e != nil {
		baselineEstimate = e.transcriptRuntimeState().EstimatedProviderTokens()
	}
	normalizedUsage, totalInputTokens, totalCachedInputTokens := e.usageTrackingState().Next(usage)
	receipt := session.CommitReceipt{Committed: true}
	var persistenceErr error
	if e != nil && e.store != nil {
		cachedInputTokens, hasCachedInputTokens := textutil.OptionalValue(normalizedUsage.CachedInputTokens)
		receipt, persistenceErr = e.store.SetUsageState(&session.UsageState{
			InputTokens:             normalizedUsage.InputTokens,
			OutputTokens:            normalizedUsage.OutputTokens,
			WindowTokens:            normalizedUsage.WindowTokens,
			CachedInputTokens:       cachedInputTokens,
			HasCachedInputTokens:    hasCachedInputTokens,
			EstimatedProviderTokens: baselineEstimate,
			TotalInputTokens:        totalInputTokens,
			TotalCachedInputTokens:  totalCachedInputTokens,
		})
		if !receipt.Committed {
			return receipt, persistenceErr
		}
	}
	e.applyUsageTrackingState(normalizedUsage, baselineEstimate, totalInputTokens, totalCachedInputTokens)
	return receipt, persistenceErr
}

func (e *Engine) restorePersistedUsageState(state *session.UsageState) {
	if e == nil || state == nil {
		return
	}
	normalized := normalizePersistedUsageTrackingState(*state)
	var cachedInputTokens *int
	if normalized.HasCachedInputTokens {
		cachedInputTokens = textutil.Value(normalized.CachedInputTokens)
	}
	e.applyUsageTrackingState(
		llm.Usage{
			InputTokens:       normalized.InputTokens,
			OutputTokens:      normalized.OutputTokens,
			WindowTokens:      normalized.WindowTokens,
			CachedInputTokens: cachedInputTokens,
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

func (e *Engine) emitRaw(evt Event) error {
	if evt.Kind == EventToolCallStarted && e.liveRun != nil {
		e.liveRun.recordToolStart(evt.StepID)
	}
	revision, err := e.TranscriptRevision()
	if err != nil {
		return err
	}
	return e.emitRawAtRevision(evt, revision)
}

func (e *Engine) emitRawAtRevision(evt Event, revision int64) error {
	evt.TranscriptRevision = revision
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
	return nil
}

func (e *Engine) publishLiveRunFinished(result LiveRunResult) {
	if result.Status == RunStatusCompleted && result.ResultKind != LiveRunResultAssistantFinalAnswer {
		switch result.NoFinalReason {
		case LiveRunNoFinalAnswerReasonUserShell, LiveRunNoFinalAnswerReasonBackground:
			return
		}
	}
	if result.Status == RunStatusInterrupted || errors.Is(result.Error, context.Canceled) {
		return
	}
	copyResult := result
	e.emitRaw(Event{
		Kind:          EventLiveRunFinished,
		StepID:        result.StepID.String(),
		LiveRunResult: &copyResult,
	})
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

func (e *Engine) compactionRuntimeState() *compactionRuntimeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.compactionState == nil {
		e.compactionState = newCompactionRuntimeState()
	}
	return e.compactionState
}
