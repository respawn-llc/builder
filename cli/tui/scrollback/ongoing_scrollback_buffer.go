package scrollback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

type OngoingScrollbackBufferImpl struct {
	mu                        sync.Mutex
	cancelWatcher             context.CancelFunc
	closeOnce                 sync.Once
	stableWriter              io.Writer
	liveArea                  *nativeLiveAreaImpl
	queuedSteers              []stableSteerRequest
	heldStableOps             []stableHoldoffOperation
	terminalWidth             int
	terminalHeight            int
	isStreaming               bool
	assistantMarkdownRenderer AssistantMarkdownRenderer
	assistantStreamSource     string
	prepareNormalBuffer       bool
	normalBufferPrepared      bool
	closed                    bool
	normalBufferAvailable     func() bool
	delayedWriteErrorListener func(error)
	turnEndedDuringActiveFlow atomic.Bool
}

type OngoingScrollbackBufferOption func(*OngoingScrollbackBufferImpl)

type AssistantMarkdownRenderer func(source string, width int) []string

func WithAssistantMarkdownRenderer(renderer AssistantMarkdownRenderer) OngoingScrollbackBufferOption {
	return func(buffer *OngoingScrollbackBufferImpl) {
		buffer.assistantMarkdownRenderer = renderer
	}
}

type stableSteerRequest struct {
	line string
}

type stableHoldoffOperationKind uint8

const (
	stableHoldoffSteer stableHoldoffOperationKind = iota + 1
	stableHoldoffAssistantStream
	stableHoldoffFinishAssistantStream
	stableHoldoffDiscardAssistantStream
)

type stableHoldoffOperation struct {
	kind         stableHoldoffOperationKind
	payload      string
	queuedSteers []stableSteerRequest
}

type stableOutputRow struct {
	operation           string
	text                string
	appendTerminalBreak bool
}

var errOngoingScrollbackBufferClosed = errors.New("native scrollback buffer is closed")

func WithNormalBufferAvailability(available func() bool) OngoingScrollbackBufferOption {
	return func(buffer *OngoingScrollbackBufferImpl) {
		buffer.normalBufferAvailable = available
	}
}

func WithDelayedWriteErrorListener(listener func(error)) OngoingScrollbackBufferOption {
	return func(buffer *OngoingScrollbackBufferImpl) {
		buffer.delayedWriteErrorListener = listener
	}
}

func WithNormalBufferPreparation() OngoingScrollbackBufferOption {
	return func(buffer *OngoingScrollbackBufferImpl) {
		buffer.prepareNormalBuffer = true
	}
}

func NewOngoingScrollbackBufferImpl(ctx context.Context, terminalWidth int, terminalHeight int, stableWriter io.Writer, turnEnded <-chan struct{}, options ...OngoingScrollbackBufferOption) *OngoingScrollbackBufferImpl {
	if terminalWidth <= 0 || terminalHeight <= 0 {
		terminalWidth = max(terminalWidth, 1)
		terminalHeight = max(terminalHeight, 2)
	}
	if stableWriter == nil {
		stableWriter = io.Discard
	}
	if ctx == nil {
		ctx = context.Background()
	}
	watcherCtx, cancelWatcher := context.WithCancel(ctx)
	buffer := &OngoingScrollbackBufferImpl{
		cancelWatcher:  cancelWatcher,
		stableWriter:   stableWriter,
		terminalWidth:  terminalWidth,
		terminalHeight: terminalHeight,
	}
	for _, option := range options {
		if option != nil {
			option(buffer)
		}
	}
	if turnEnded != nil {
		go buffer.watchTurnEnded(watcherCtx, turnEnded)
	}
	return buffer
}

func (buffer *OngoingScrollbackBufferImpl) close() {
	if buffer == nil {
		return
	}
	buffer.closeOnce.Do(func() {
		if buffer.cancelWatcher != nil {
			buffer.cancelWatcher()
		}
		var delayedErr error
		buffer.mu.Lock()
		if buffer.liveArea != nil && buffer.normalBufferAvailableLocked() {
			delayedErr = buffer.liveArea.erasePhysicalLocked()
		}
		buffer.closed = true
		buffer.isStreaming = false
		buffer.clearAssistantStreamStateLocked()
		buffer.turnEndedDuringActiveFlow.Store(false)
		buffer.queuedSteers = nil
		buffer.heldStableOps = nil
		buffer.mu.Unlock()
		buffer.notifyDelayedWriteError(delayedErr)
	})
}

func (buffer *OngoingScrollbackBufferImpl) Steer(line string) error {
	if err := buffer.validateSteerLineBeforeLock(line); err != nil {
		return err
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("steer")
	}
	if !buffer.normalBufferAvailableLocked() {
		if buffer.isStreaming || len(buffer.queuedSteers) > 0 {
			buffer.queuedSteers = append(buffer.queuedSteers, stableSteerRequest{line: line})
		} else {
			buffer.heldStableOps = append(buffer.heldStableOps, stableHoldoffOperation{kind: stableHoldoffSteer, payload: line})
		}
		buffer.mu.Unlock()
		return nil
	}
	if buffer.isStreaming || len(buffer.queuedSteers) > 0 {
		buffer.queuedSteers = append(buffer.queuedSteers, stableSteerRequest{line: line})
		buffer.mu.Unlock()
		return nil
	}
	if err := buffer.prepareNormalBufferLocked(); err != nil {
		buffer.mu.Unlock()
		return err
	}
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}
	err := buffer.writeStableRowsWithLiveErasedLocked([]stableOutputRow{{operation: "steer", text: line}}, true, false)
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) StreamMarkdownAssistantContent(ansi string) error {
	if err := buffer.validateReadyBeforeLock("streamMarkdownAssistantContent", ansi); err != nil {
		return err
	}
	if buffer.turnEndedDuringActiveFlow.Load() {
		return scrollbackInvariantError("streamMarkdownAssistantContent", "assistant stream continued after model turn ended before finishAssistantStreaming", ansi, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(ansi))
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("streamMarkdownAssistantContent")
	}
	if buffer.turnEndedDuringActiveFlow.Load() {
		buffer.mu.Unlock()
		return scrollbackInvariantError("streamMarkdownAssistantContent", "assistant stream continued after model turn ended before finishAssistantStreaming", ansi, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(ansi))
	}
	if !buffer.isStreaming {
		buffer.isStreaming = true
		buffer.turnEndedDuringActiveFlow.Store(false)
	}
	if !buffer.normalBufferAvailableLocked() {
		buffer.heldStableOps = append(buffer.heldStableOps, stableHoldoffOperation{kind: stableHoldoffAssistantStream, payload: ansi})
		buffer.mu.Unlock()
		return nil
	}
	if err := buffer.prepareNormalBufferLocked(); err != nil {
		buffer.isStreaming = false
		buffer.clearAssistantStreamStateLocked()
		buffer.turnEndedDuringActiveFlow.Store(false)
		buffer.mu.Unlock()
		return err
	}
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}
	err := buffer.writeAssistantStreamPayloadLocked(ansi)
	if err != nil {
		buffer.isStreaming = false
		buffer.clearAssistantStreamStateLocked()
		buffer.turnEndedDuringActiveFlow.Store(false)
	}
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) FinishAssistantStreaming() error {
	if err := buffer.validateReadyBeforeLock("finishAssistantStreaming", ""); err != nil {
		return err
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("finishAssistantStreaming")
	}
	if !buffer.isStreaming {
		buffer.mu.Unlock()
		return nil
	}
	buffer.isStreaming = false
	buffer.turnEndedDuringActiveFlow.Store(false)
	queuedSteers := append([]stableSteerRequest(nil), buffer.queuedSteers...)
	buffer.queuedSteers = nil

	if !buffer.normalBufferAvailableLocked() {
		buffer.heldStableOps = append(buffer.heldStableOps, stableHoldoffOperation{kind: stableHoldoffFinishAssistantStream, queuedSteers: queuedSteers})
		buffer.mu.Unlock()
		return nil
	}
	if err := buffer.prepareNormalBufferLocked(); err != nil {
		buffer.mu.Unlock()
		return err
	}
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}

	err := buffer.finishAssistantStreamingLocked(queuedSteers)
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) DiscardAssistantStreaming() error {
	if err := buffer.validateReadyBeforeLock("discardAssistantStreaming", ""); err != nil {
		return err
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("discardAssistantStreaming")
	}
	if !buffer.isStreaming {
		buffer.mu.Unlock()
		return nil
	}
	if !buffer.normalBufferAvailableLocked() {
		buffer.isStreaming = false
		buffer.turnEndedDuringActiveFlow.Store(false)
		queuedSteers := append([]stableSteerRequest(nil), buffer.queuedSteers...)
		buffer.queuedSteers = nil
		buffer.heldStableOps = append(buffer.heldStableOps, stableHoldoffOperation{kind: stableHoldoffDiscardAssistantStream, queuedSteers: queuedSteers})
		buffer.clearAssistantStreamStateLocked()
		buffer.mu.Unlock()
		return nil
	}
	if err := buffer.prepareNormalBufferLocked(); err != nil {
		buffer.mu.Unlock()
		return err
	}
	buffer.isStreaming = false
	buffer.turnEndedDuringActiveFlow.Store(false)
	queuedSteers := append([]stableSteerRequest(nil), buffer.queuedSteers...)
	buffer.queuedSteers = nil
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}

	err := buffer.discardAssistantStreamingLocked(queuedSteers)
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) RenderLive(frame NativeLiveAreaFrame) error {
	if err := buffer.validateReadyBeforeLock("renderLive", ""); err != nil {
		return err
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("renderLive")
	}
	if buffer.liveArea == nil {
		buffer.liveArea = &nativeLiveAreaImpl{
			buffer:         buffer,
			terminalWidth:  buffer.terminalWidth,
			terminalHeight: buffer.terminalHeight,
		}
	}
	liveArea := buffer.liveArea
	buffer.mu.Unlock()
	return liveArea.Render(frame)
}

func (buffer *OngoingScrollbackBufferImpl) AssistantStreaming() bool {
	if buffer == nil {
		return false
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.isStreaming
}

func (buffer *OngoingScrollbackBufferImpl) AssistantStreamTailLines() []string {
	return nil
}

func (buffer *OngoingScrollbackBufferImpl) FlushHoldoff() error {
	return buffer.flushHoldoff()
}

func (buffer *OngoingScrollbackBufferImpl) flushHoldoff() error {
	if err := buffer.validateReadyBeforeLock("flushHoldoff", ""); err != nil {
		return err
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("flushHoldoff")
	}
	if !buffer.normalBufferAvailableLocked() {
		buffer.mu.Unlock()
		return nil
	}
	_, err := buffer.flushHoldoffLocked()
	buffer.mu.Unlock()
	if err != nil {
		buffer.notifyDelayedWriteError(err)
	}
	return err
}

func (buffer *OngoingScrollbackBufferImpl) watchTurnEnded(ctx context.Context, turnEnded <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-turnEnded:
			if !ok {
				return
			}
			buffer.markTurnEnded()
		}
	}
}

func (buffer *OngoingScrollbackBufferImpl) markTurnEnded() {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.isStreaming {
		buffer.turnEndedDuringActiveFlow.Store(true)
	}
}

func (buffer *OngoingScrollbackBufferImpl) attachLiveArea(liveArea *nativeLiveAreaImpl) {
	if err := buffer.validateReadyBeforeLock("attachLiveArea", ""); err != nil {
		return
	}
	if liveArea == nil {
		return
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.closed {
		return
	}
	if buffer.liveArea != nil {
		return
	}
	buffer.liveArea = liveArea
}

func (buffer *OngoingScrollbackBufferImpl) normalBufferAvailableLocked() bool {
	available := true
	if buffer.normalBufferAvailable != nil {
		available = buffer.normalBufferAvailable()
	}
	if !available {
		buffer.normalBufferPrepared = false
	}
	return available
}

func InvalidateNormalBufferPreparation(buffer *OngoingScrollbackBufferImpl) {
	if buffer == nil {
		return
	}
	buffer.invalidateNormalBufferPreparation()
}

func (buffer *OngoingScrollbackBufferImpl) invalidateNormalBufferPreparation() {
	if buffer == nil {
		return
	}
	buffer.mu.Lock()
	buffer.normalBufferPrepared = false
	if buffer.liveArea != nil {
		buffer.liveArea.pendingPhysicalRender = true
	}
	buffer.mu.Unlock()
}

func (buffer *OngoingScrollbackBufferImpl) flushHoldoffLocked() (bool, error) {
	if !buffer.normalBufferAvailableLocked() {
		return false, nil
	}
	flushed, firstErr := buffer.flushHeldStableOpsLocked()
	if !buffer.isStreaming && buffer.liveArea != nil && buffer.liveArea.pendingPhysicalRender {
		if err := buffer.prepareNormalBufferLocked(); firstErr == nil && err != nil {
			firstErr = err
		} else if err := buffer.liveArea.erasePhysicalLocked(); firstErr == nil && err != nil {
			firstErr = err
		} else if err == nil {
			if renderErr := buffer.liveArea.renderPhysicalLocked(); firstErr == nil {
				firstErr = renderErr
			}
		}
		flushed = true
	}
	return flushed, firstErr
}

func (buffer *OngoingScrollbackBufferImpl) flushHeldStableOpsLocked() (bool, error) {
	if !buffer.normalBufferAvailableLocked() || len(buffer.heldStableOps) == 0 {
		return false, nil
	}
	if err := buffer.prepareNormalBufferLocked(); err != nil {
		return false, err
	}
	operations := append([]stableHoldoffOperation(nil), buffer.heldStableOps...)
	buffer.heldStableOps = nil
	return true, buffer.writeStableHoldoffOperationsLocked(operations)
}

func (buffer *OngoingScrollbackBufferImpl) writeStableHoldoffOperationsLocked(operations []stableHoldoffOperation) error {
	var firstErr error
	for _, operation := range operations {
		err := error(nil)
		switch operation.kind {
		case stableHoldoffSteer:
			err = buffer.writeStableRowsWithLiveErasedLocked([]stableOutputRow{{operation: "steer", text: operation.payload}}, true, false)
		case stableHoldoffAssistantStream:
			err = buffer.writeAssistantStreamPayloadLocked(operation.payload)
		case stableHoldoffFinishAssistantStream:
			err = buffer.finishAssistantStreamingLocked(operation.queuedSteers)
		case stableHoldoffDiscardAssistantStream:
			err = buffer.discardAssistantStreamingLocked(operation.queuedSteers)
		default:
			err = scrollbackInvariantError("flushHoldoff", "unknown stable holdoff operation kind", operation.payload, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(operation.payload))
		}
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}
	return firstErr
}

func (buffer *OngoingScrollbackBufferImpl) finishAssistantStreamingLocked(queuedSteers []stableSteerRequest) error {
	buffer.clearAssistantStreamStateLocked()
	if len(queuedSteers) == 0 {
		return nil
	}
	stableRows := make([]stableOutputRow, 0, len(queuedSteers))
	for _, request := range queuedSteers {
		stableRows = append(stableRows, stableOutputRow{operation: "steer", text: request.line})
	}
	return buffer.writeStableRowsWithLiveErasedLocked(stableRows, true, true)
}

func (buffer *OngoingScrollbackBufferImpl) discardAssistantStreamingLocked(queuedSteers []stableSteerRequest) error {
	buffer.clearAssistantStreamStateLocked()
	stableRows := make([]stableOutputRow, 0, len(queuedSteers))
	for _, request := range queuedSteers {
		stableRows = append(stableRows, stableOutputRow{operation: "steer", text: request.line})
	}
	return buffer.writeStableRowsWithLiveErasedLocked(stableRows, true, true)
}

func (buffer *OngoingScrollbackBufferImpl) writeAssistantStreamPayloadLocked(delta string) error {
	buffer.assistantStreamSource += delta
	payload := buffer.assistantStreamTerminalPayload(delta)
	if payload == "" {
		return nil
	}
	return buffer.writeStableRowsWithLiveErasedLocked([]stableOutputRow{{
		operation: "streamMarkdownAssistantContent",
		text:      payload,
	}}, false, false)
}

func (buffer *OngoingScrollbackBufferImpl) assistantStreamTerminalPayload(delta string) string {
	if buffer == nil || buffer.assistantMarkdownRenderer == nil {
		return nativeAssistantStreamTerminalPayload(delta)
	}
	rows := buffer.assistantMarkdownRenderer(delta, buffer.terminalWidth)
	if len(rows) == 0 {
		return ""
	}
	return nativeAssistantStreamTerminalPayload(strings.Join(rows, "\n"))
}

func (buffer *OngoingScrollbackBufferImpl) clearAssistantStreamStateLocked() {
	buffer.assistantStreamSource = ""
}

func (buffer *OngoingScrollbackBufferImpl) writeStableRowsWithLiveErasedLocked(rows []stableOutputRow, restoreLive bool, continueAfterError bool) error {
	err := error(nil)
	shouldRestoreLive := false
	if liveArea := buffer.liveArea; liveArea != nil {
		if liveArea.renderedLines > 0 {
			err = liveArea.erasePhysicalLocked()
			shouldRestoreLive = err == nil
		} else if liveArea.pendingPhysicalRender && len(liveArea.frame.Lines) > 0 {
			shouldRestoreLive = true
		}
	}
	if liveArea := buffer.liveArea; liveArea != nil && shouldRestoreLive {
		liveArea.pendingPhysicalRender = true
	}
	if err == nil && len(rows) > 0 {
		err = buffer.writeStableRowsDirectLocked(rows, continueAfterError)
	}
	if liveArea := buffer.liveArea; liveArea != nil && shouldRestoreLive && restoreLive {
		if restoreErr := liveArea.renderPhysicalLocked(); err == nil {
			err = restoreErr
		}
	}
	return err
}

func (buffer *OngoingScrollbackBufferImpl) writeStableRowsDirectLocked(rows []stableOutputRow, continueAfterError bool) error {
	var firstErr error
	for _, row := range rows {
		stablePayload := row.text
		if row.appendTerminalBreak {
			stablePayload += terminalLineBreak
		}
		written, writeErr := io.WriteString(buffer.stableWriter, stablePayload)
		if err := buffer.stableWriteResult(row.operation, stablePayload, written, writeErr); firstErr == nil && err != nil {
			firstErr = err
			if !continueAfterError {
				break
			}
		}
	}
	return firstErr
}

func stableOutputRows(operation string, rows []string) []stableOutputRow {
	out := make([]stableOutputRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, stableOutputRow{operation: operation, text: row, appendTerminalBreak: true})
	}
	return out
}

func (buffer *OngoingScrollbackBufferImpl) notifyDelayedWriteError(err error) {
	if err == nil || buffer == nil || buffer.delayedWriteErrorListener == nil {
		return
	}
	buffer.delayedWriteErrorListener(err)
}

func (buffer *OngoingScrollbackBufferImpl) validateSteerLineBeforeLock(line string) error {
	if err := buffer.validateReadyBeforeLock("steer", line); err != nil {
		return err
	}
	visualWidth := lipgloss.Width(line)
	if strings.ContainsAny(line, "\r\n") {
		return scrollbackInvariantError("steer", "line contains CR or LF and is not exactly one terminal line", line, buffer.terminalWidth, buffer.terminalHeight, visualWidth)
	}
	if visualWidth > buffer.terminalWidth {
		return scrollbackInvariantError("steer", "line exceeds one visual terminal line", line, buffer.terminalWidth, buffer.terminalHeight, visualWidth)
	}
	return nil
}

func (buffer *OngoingScrollbackBufferImpl) validateReadyBeforeLock(operation string, payload string) error {
	if buffer == nil {
		return scrollbackInvariantError(operation, "nil OngoingScrollbackBufferImpl receiver", payload, 0, 0, lipgloss.Width(payload))
	}
	if buffer.terminalWidth <= 0 || buffer.terminalHeight <= 0 {
		return scrollbackInvariantError(operation, "terminal dimensions must be positive", payload, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(payload))
	}
	if buffer.stableWriter == nil {
		return scrollbackInvariantError(operation, "stable writer is required", payload, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(payload))
	}
	return nil
}

func (buffer *OngoingScrollbackBufferImpl) stableWriteResult(operation string, payload string, written int, err error) error {
	if err != nil {
		return fmt.Errorf("%s stable write failed: %s: %w", operation, stableWriteDiagnostics(payload, buffer.terminalWidth, buffer.terminalHeight, written), err)
	}
	if written != len(payload) {
		return fmt.Errorf("%s stable write short write: %s: %w", operation, stableWriteDiagnostics(payload, buffer.terminalWidth, buffer.terminalHeight, written), io.ErrShortWrite)
	}
	return nil
}

func (buffer *OngoingScrollbackBufferImpl) closedError(operation string) error {
	return fmt.Errorf("%s: %w", operation, errOngoingScrollbackBufferClosed)
}

func (buffer *OngoingScrollbackBufferImpl) prepareNormalBufferLocked() error {
	if buffer == nil || !buffer.prepareNormalBuffer || buffer.normalBufferPrepared {
		return nil
	}
	payload := normalBufferPreparationSequence()
	written, err := io.WriteString(buffer.stableWriter, payload)
	if err != nil {
		return fmt.Errorf("prepare normal buffer failed: %s: %w", stableWriteDiagnostics(payload, buffer.terminalWidth, buffer.terminalHeight, written), err)
	}
	if written != len(payload) {
		return fmt.Errorf("prepare normal buffer short write: %s: %w", stableWriteDiagnostics(payload, buffer.terminalWidth, buffer.terminalHeight, written), io.ErrShortWrite)
	}
	buffer.normalBufferPrepared = true
	return nil
}

func normalBufferPreparationSequence() string {
	return xansi.ResetModeAltScreenSaveCursor + xansi.SaveCursor + "\x1b[?6l" + "\x1b[r" + xansi.RestoreCursor
}

func stableWriteDiagnostics(payload string, terminalWidth int, terminalHeight int, written int) string {
	return fmt.Sprintf(
		"terminal_width=%d terminal_height=%d visual_width=%d byte_len=%d written=%d payload_quoted=%q payload_raw_hex=% x",
		terminalWidth,
		terminalHeight,
		lipgloss.Width(payload),
		len(payload),
		written,
		payload,
		[]byte(payload),
	)
}

const terminalLineBreak = "\r\n"

func nativeAssistantStreamTerminalPayload(payload string) string {
	stripped := xansi.Strip(payload)
	var clean strings.Builder
	clean.Grow(len(stripped))
	for _, char := range stripped {
		switch {
		case char == '\t':
			clean.WriteString("    ")
		case char == '\n':
			clean.WriteRune(char)
		case char == '\r':
			continue
		case char >= 0 && char < ' ':
			continue
		case char == 0x7f:
			continue
		default:
			clean.WriteRune(char)
		}
	}
	return normalizeTerminalLineBreaks(clean.String())
}

func normalizeTerminalLineBreaks(payload string) string {
	if !strings.Contains(payload, "\n") {
		return payload
	}
	var out strings.Builder
	out.Grow(len(payload) + strings.Count(payload, "\n"))
	previousWasCarriageReturn := false
	for _, char := range payload {
		if char == '\n' && !previousWasCarriageReturn {
			out.WriteByte('\r')
		}
		out.WriteRune(char)
		previousWasCarriageReturn = char == '\r'
	}
	return out.String()
}

func scrollbackInvariantError(operation string, reason string, payload string, terminalWidth int, terminalHeight int, visualWidth int) error {
	return fmt.Errorf(
		"NativeOngoingSurface invalid operation: operation=%s reason=%s terminal_width=%d terminal_height=%d visual_width=%d byte_len=%d payload_quoted=%q payload_raw_hex=% x",
		operation,
		reason,
		terminalWidth,
		terminalHeight,
		visualWidth,
		len(payload),
		payload,
		[]byte(payload),
	)
}
