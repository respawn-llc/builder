package scrollback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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
	assistantStreamOpenLine   bool
	assistantStreamTail       string
	closed                    bool
	normalBufferAvailable     func() bool
	delayedWriteErrorListener func(error)
	turnEndedDuringActiveFlow atomic.Bool
}

type OngoingScrollbackBufferOption func(*OngoingScrollbackBufferImpl)

type stableSteerRequest struct {
	line string
}

type stableHoldoffOperationKind uint8

const (
	stableHoldoffSteer stableHoldoffOperationKind = iota + 1
	stableHoldoffAssistantStream
	stableHoldoffFinishAssistantStream
)

type stableHoldoffOperation struct {
	kind         stableHoldoffOperationKind
	payload      string
	queuedSteers []stableSteerRequest
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

func NewOngoingScrollbackBufferImpl(ctx context.Context, terminalWidth int, terminalHeight int, stableWriter io.Writer, turnEnded <-chan struct{}, options ...OngoingScrollbackBufferOption) *OngoingScrollbackBufferImpl {
	if terminalWidth <= 0 || terminalHeight <= 0 {
		panicScrollbackInvariant("NewOngoingScrollbackBufferImpl", "terminal dimensions must be positive", "", terminalWidth, terminalHeight, 0)
	}
	if stableWriter == nil {
		panicScrollbackInvariant("NewOngoingScrollbackBufferImpl", "stable writer is required", "", terminalWidth, terminalHeight, 0)
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
		buffer.assistantStreamOpenLine = false
		buffer.assistantStreamTail = ""
		buffer.turnEndedDuringActiveFlow.Store(false)
		buffer.queuedSteers = nil
		buffer.heldStableOps = nil
		buffer.mu.Unlock()
		buffer.notifyDelayedWriteError(delayedErr)
	})
}

func (buffer *OngoingScrollbackBufferImpl) Steer(line string) error {
	buffer.validateSteerLineBeforeLock(line)

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
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}
	err := buffer.withLiveErasedForStableLocked(func() error {
		payload := line + terminalLineBreak
		written, writeErr := io.WriteString(buffer.stableWriter, payload)
		return buffer.stableWriteResult("steer", payload, written, writeErr)
	})
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) StreamMarkdownAssistantContent(ansi string) error {
	buffer.validateReadyBeforeLock("streamMarkdownAssistantContent", ansi)
	if buffer.turnEndedDuringActiveFlow.Load() {
		panicScrollbackInvariant(
			"streamMarkdownAssistantContent",
			"assistant stream continued after model turn ended before finishAssistantStreaming",
			ansi,
			buffer.terminalWidth,
			buffer.terminalHeight,
			lipgloss.Width(ansi),
		)
	}

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("streamMarkdownAssistantContent")
	}
	if buffer.turnEndedDuringActiveFlow.Load() {
		buffer.mu.Unlock()
		panicScrollbackInvariant(
			"streamMarkdownAssistantContent",
			"assistant stream continued after model turn ended before finishAssistantStreaming",
			ansi,
			buffer.terminalWidth,
			buffer.terminalHeight,
			lipgloss.Width(ansi),
		)
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
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}
	err := buffer.writeAssistantStreamPayloadLocked(ansi)
	if err != nil {
		buffer.isStreaming = false
		buffer.assistantStreamOpenLine = false
		buffer.assistantStreamTail = ""
		buffer.turnEndedDuringActiveFlow.Store(false)
	}
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) FinishAssistantStreaming() error {
	buffer.validateReadyBeforeLock("finishAssistantStreaming", "")

	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return buffer.closedError("finishAssistantStreaming")
	}
	if !buffer.isStreaming {
		buffer.mu.Unlock()
		panicScrollbackInvariant(
			"finishAssistantStreaming",
			"finishAssistantStreaming called without an active assistant stream",
			"",
			buffer.terminalWidth,
			buffer.terminalHeight,
			0,
		)
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
	delayedErr := error(nil)
	if _, err := buffer.flushHeldStableOpsLocked(); err != nil {
		delayedErr = err
	}

	err := buffer.finishAssistantStreamingLocked(queuedSteers)
	buffer.mu.Unlock()
	buffer.notifyDelayedWriteError(delayedErr)
	return err
}

func (buffer *OngoingScrollbackBufferImpl) RenderLive(frame NativeLiveAreaFrame) error {
	buffer.validateReadyBeforeLock("renderLive", "")

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
	if buffer == nil {
		return nil
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.assistantStreamTail == "" || xansi.StringWidth(buffer.assistantStreamTail) == 0 {
		return nil
	}
	return []string{buffer.assistantStreamTail}
}

func (buffer *OngoingScrollbackBufferImpl) FlushHoldoff() error {
	return buffer.flushHoldoff()
}

func (buffer *OngoingScrollbackBufferImpl) flushHoldoff() error {
	buffer.validateReadyBeforeLock("flushHoldoff", "")

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
	buffer.validateReadyBeforeLock("attachLiveArea", "")
	if liveArea == nil {
		panicScrollbackInvariant("attachLiveArea", "live area is required", "", buffer.terminalWidth, buffer.terminalHeight, 0)
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.closed {
		panicScrollbackInvariant("attachLiveArea", "stable buffer is closed", "", buffer.terminalWidth, buffer.terminalHeight, 0)
	}
	if buffer.liveArea != nil {
		panicScrollbackInvariant("attachLiveArea", "live area already attached", "", buffer.terminalWidth, buffer.terminalHeight, 0)
	}
	buffer.liveArea = liveArea
}

func (buffer *OngoingScrollbackBufferImpl) normalBufferAvailableLocked() bool {
	if buffer.normalBufferAvailable == nil {
		return true
	}
	return buffer.normalBufferAvailable()
}

func (buffer *OngoingScrollbackBufferImpl) flushHoldoffLocked() (bool, error) {
	if !buffer.normalBufferAvailableLocked() {
		return false, nil
	}
	flushed, firstErr := buffer.flushHeldStableOpsLocked()
	if !buffer.isStreaming && buffer.liveArea != nil && buffer.liveArea.pendingPhysicalRender {
		if err := buffer.liveArea.erasePhysicalLocked(); firstErr == nil && err != nil {
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
	operations := append([]stableHoldoffOperation(nil), buffer.heldStableOps...)
	buffer.heldStableOps = nil
	if buffer.isStreaming {
		return true, buffer.writeStableHoldoffOperationsLocked(operations)
	}
	return true, buffer.withLiveErasedForStableLocked(func() error {
		return buffer.writeStableHoldoffOperationsLocked(operations)
	})
}

func (buffer *OngoingScrollbackBufferImpl) writeStableHoldoffOperationsLocked(operations []stableHoldoffOperation) error {
	var firstErr error
	for _, operation := range operations {
		err := error(nil)
		switch operation.kind {
		case stableHoldoffSteer:
			err = buffer.writeSteerPayloadLocked(operation.payload)
		case stableHoldoffAssistantStream:
			err = buffer.writeAssistantStreamPayloadLocked(operation.payload)
		case stableHoldoffFinishAssistantStream:
			err = buffer.writeAssistantStreamTerminatorAndQueuedSteersLocked(operation.queuedSteers)
		default:
			panicScrollbackInvariant("flushHoldoff", "unknown stable holdoff operation kind", operation.payload, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(operation.payload))
		}
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}
	return firstErr
}

func (buffer *OngoingScrollbackBufferImpl) finishAssistantStreamingLocked(queuedSteers []stableSteerRequest) error {
	return buffer.withLiveErasedForStableLocked(func() error {
		return buffer.writeAssistantStreamTerminatorAndQueuedSteersLocked(queuedSteers)
	})
}

func (buffer *OngoingScrollbackBufferImpl) writeAssistantStreamTerminatorAndQueuedSteersLocked(queuedSteers []stableSteerRequest) error {
	firstErr := buffer.writeAssistantStreamTerminatorLocked()
	if err := buffer.writeQueuedSteersLocked(queuedSteers); firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (buffer *OngoingScrollbackBufferImpl) writeAssistantStreamTerminatorLocked() error {
	if !buffer.assistantStreamOpenLine && buffer.assistantStreamTail == "" {
		return nil
	}
	if buffer.assistantStreamTail != "" && xansi.StringWidth(buffer.assistantStreamTail) == 0 {
		buffer.assistantStreamOpenLine = false
		buffer.assistantStreamTail = ""
		return nil
	}
	payload := buffer.assistantStreamTail + terminalLineBreak
	written, writeErr := io.WriteString(buffer.stableWriter, payload)
	err := buffer.stableWriteResult("finishAssistantStreaming", payload, written, writeErr)
	if err == nil {
		buffer.assistantStreamOpenLine = false
		buffer.assistantStreamTail = ""
	}
	return err
}

func (buffer *OngoingScrollbackBufferImpl) writeQueuedSteersLocked(queuedSteers []stableSteerRequest) error {
	var firstErr error
	for _, request := range queuedSteers {
		if err := buffer.writeSteerPayloadLocked(request.line); firstErr == nil && err != nil {
			firstErr = err
		}
	}
	return firstErr
}

func (buffer *OngoingScrollbackBufferImpl) writeSteerPayloadLocked(line string) error {
	payload := line + terminalLineBreak
	written, writeErr := io.WriteString(buffer.stableWriter, payload)
	return buffer.stableWriteResult("steer", payload, written, writeErr)
}

func (buffer *OngoingScrollbackBufferImpl) writeAssistantStreamPayloadLocked(ansi string) error {
	payload := normalizeTerminalLineBreaks(ansi)
	rows := buffer.appendAssistantStreamPayloadRowsLocked(payload)
	if len(rows) == 0 {
		buffer.assistantStreamOpenLine = buffer.assistantStreamTail != ""
		return nil
	}
	return buffer.withLiveErasedForAssistantStreamLocked(func() error {
		var firstErr error
		for _, row := range rows {
			stablePayload := row + terminalLineBreak
			written, writeErr := io.WriteString(buffer.stableWriter, stablePayload)
			err := buffer.stableWriteResult("streamMarkdownAssistantContent", stablePayload, written, writeErr)
			if firstErr == nil && err != nil {
				firstErr = err
			}
		}
		if firstErr == nil {
			buffer.assistantStreamOpenLine = buffer.assistantStreamTail != ""
		}
		return firstErr
	})
}

func (buffer *OngoingScrollbackBufferImpl) appendAssistantStreamPayloadRowsLocked(payload string) []string {
	if payload == "" {
		return nil
	}
	rows := []string{}
	for index := 0; index < len(payload); {
		if sequence, nextIndex, ok := readCSISequence(payload, index); ok {
			buffer.assistantStreamTail += sequence
			index = nextIndex
			continue
		}
		char, size := utf8.DecodeRuneInString(payload[index:])
		if char == utf8.RuneError && size == 0 {
			break
		}
		index += size
		switch char {
		case '\r':
			continue
		case '\n':
			if buffer.assistantStreamTail == "" || xansi.StringWidth(buffer.assistantStreamTail) > 0 {
				rows = append(rows, buffer.assistantStreamTail)
			}
			buffer.assistantStreamTail = ""
		default:
			buffer.assistantStreamTail += string(char)
			for xansi.StringWidth(buffer.assistantStreamTail) >= buffer.terminalWidth {
				prefix, suffix := splitVisualPrefix(buffer.assistantStreamTail, buffer.terminalWidth)
				rows = append(rows, prefix)
				buffer.assistantStreamTail = suffix
			}
		}
	}
	return rows
}

func (buffer *OngoingScrollbackBufferImpl) withLiveErasedForStableLocked(writeStable func() error) error {
	err := error(nil)
	liveErased := false
	if liveArea := buffer.liveArea; liveArea != nil {
		err = liveArea.erasePhysicalLocked()
		liveErased = err == nil
	}
	if err == nil && writeStable != nil {
		err = writeStable()
	}
	if liveArea := buffer.liveArea; liveArea != nil && liveErased {
		if restoreErr := liveArea.renderPhysicalLocked(); err == nil {
			err = restoreErr
		}
	}
	return err
}

func (buffer *OngoingScrollbackBufferImpl) withLiveErasedForAssistantStreamLocked(writeStable func() error) error {
	err := error(nil)
	if liveArea := buffer.liveArea; liveArea != nil {
		err = liveArea.erasePhysicalLocked()
		if err == nil {
			liveArea.pendingPhysicalRender = true
		}
	}
	if err == nil && writeStable != nil {
		err = writeStable()
	}
	return err
}

func (buffer *OngoingScrollbackBufferImpl) notifyDelayedWriteError(err error) {
	if err == nil || buffer == nil || buffer.delayedWriteErrorListener == nil {
		return
	}
	buffer.delayedWriteErrorListener(err)
}

func (buffer *OngoingScrollbackBufferImpl) validateSteerLineBeforeLock(line string) {
	buffer.validateReadyBeforeLock("steer", line)
	visualWidth := lipgloss.Width(line)
	if strings.ContainsAny(line, "\r\n") {
		panicScrollbackInvariant("steer", "line contains CR or LF and is not exactly one terminal line", line, buffer.terminalWidth, buffer.terminalHeight, visualWidth)
	}
	if visualWidth > buffer.terminalWidth {
		panicScrollbackInvariant("steer", "line exceeds one visual terminal line", line, buffer.terminalWidth, buffer.terminalHeight, visualWidth)
	}
}

func (buffer *OngoingScrollbackBufferImpl) validateReadyBeforeLock(operation string, payload string) {
	if buffer == nil {
		panicScrollbackInvariant(operation, "nil OngoingScrollbackBufferImpl receiver", payload, 0, 0, lipgloss.Width(payload))
	}
	if buffer.terminalWidth <= 0 || buffer.terminalHeight <= 0 {
		panicScrollbackInvariant(operation, "terminal dimensions must be positive", payload, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(payload))
	}
	if buffer.stableWriter == nil {
		panicScrollbackInvariant(operation, "stable writer is required", payload, buffer.terminalWidth, buffer.terminalHeight, lipgloss.Width(payload))
	}
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

func splitVisualPrefix(text string, width int) (string, string) {
	if width <= 0 || text == "" {
		return "", text
	}
	visualWidth := xansi.StringWidth(text)
	if visualWidth < width {
		return text, ""
	}
	prefix := xansi.Cut(text, 0, width)
	suffix := xansi.Cut(text, width, visualWidth)
	if prefix != "" {
		if activeStyle := activeSGRPrefixAtVisualWidth(text, width); activeStyle != "" {
			suffix = activeStyle + suffix
		}
		return prefix, suffix
	}
	fallbackVisualWidth := 0
	end := 0
	for index, char := range text {
		nextWidth := fallbackVisualWidth + lipgloss.Width(string(char))
		if nextWidth > width {
			break
		}
		fallbackVisualWidth = nextWidth
		end = index + len(string(char))
		if fallbackVisualWidth == width {
			break
		}
	}
	if end == 0 {
		_, size := utf8.DecodeRuneInString(text)
		return text[:size], text[size:]
	}
	return text[:end], text[end:]
}

func activeSGRPrefixAtVisualWidth(text string, width int) string {
	if width <= 0 || text == "" {
		return ""
	}
	var active strings.Builder
	visualWidth := 0
	for index := 0; index < len(text) && visualWidth < width; {
		if sequence, nextIndex, ok := readCSISequence(text, index); ok {
			if csiSequenceIsSGR(sequence) {
				if csiSGRResetsAll(sequence) {
					active.Reset()
				}
				if !csiSGRIsPureReset(sequence) {
					active.WriteString(sequence)
				}
			}
			index = nextIndex
			continue
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		visualWidth += lipgloss.Width(string(r))
		index += size
	}
	return active.String()
}

func readCSISequence(text string, index int) (string, int, bool) {
	if index < 0 || index+2 >= len(text) || text[index] != '\x1b' || text[index+1] != '[' {
		return "", index, false
	}
	for cursor := index + 2; cursor < len(text); cursor++ {
		if text[cursor] >= 0x40 && text[cursor] <= 0x7e {
			return text[index : cursor+1], cursor + 1, true
		}
	}
	return "", index, false
}

func csiSequenceIsSGR(sequence string) bool {
	return len(sequence) >= 3 && sequence[len(sequence)-1] == 'm'
}

func csiSGRResetsAll(sequence string) bool {
	params := csiSGRParams(sequence)
	if params == "" {
		return true
	}
	for _, param := range strings.FieldsFunc(params, csiSGRParamSeparator) {
		if param == "" || param == "0" {
			return true
		}
	}
	return false
}

func csiSGRIsPureReset(sequence string) bool {
	params := csiSGRParams(sequence)
	if params == "" {
		return true
	}
	for _, param := range strings.FieldsFunc(params, csiSGRParamSeparator) {
		if param != "" && param != "0" {
			return false
		}
	}
	return true
}

func csiSGRParams(sequence string) string {
	if len(sequence) <= len("\x1b[m") {
		return ""
	}
	return sequence[2 : len(sequence)-1]
}

func csiSGRParamSeparator(r rune) bool {
	return r == ';' || r == ':'
}

func panicScrollbackInvariant(operation string, reason string, payload string, terminalWidth int, terminalHeight int, visualWidth int) {
	panic(fmt.Sprintf(
		"NativeOngoingSurface invariant violation\noperation=%s\nreason=%s\nterminal_width=%d\nterminal_height=%d\nvisual_width=%d\nbyte_len=%d\npayload_quoted=%q\npayload_raw_hex=% x\nstack:\n%s",
		operation,
		reason,
		terminalWidth,
		terminalHeight,
		visualWidth,
		len(payload),
		payload,
		[]byte(payload),
		debug.Stack(),
	))
}
