package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"core/server/tools/shell/postprocess"
	xansi "github.com/charmbracelet/x/ansi"
)

const noOutputText = "No output"

type execPresentationKind uint8

const (
	execPresentationRunning execPresentationKind = iota
	execPresentationForegroundCompleted
	execPresentationBackgroundCompleted
)

type visibleShellOutput struct {
	command string
	warning postprocess.Warning
}

func newVisibleShellOutput(command string, warning postprocess.Warning) visibleShellOutput {
	return visibleShellOutput{command: command, warning: warning}
}

func (o visibleShellOutput) Content() string {
	sections := make([]string, 0, 2)
	if o.warning != nil {
		sections = append(sections, o.warning.Text())
	}
	if text := strings.TrimSpace(o.command); text != "" {
		sections = append(sections, text)
	}
	return strings.Join(sections, "\n")
}

func (o visibleShellOutput) HasVisibleContent() bool {
	return strings.TrimSpace(o.Content()) != ""
}

func (o visibleShellOutput) HasCommandContent() bool {
	return strings.TrimSpace(o.command) != ""
}

func (o visibleShellOutput) Warning() postprocess.Warning {
	return o.warning
}

type execPresentation struct {
	kind              execPresentationKind
	sessionID         string
	wallTime          time.Duration
	outputPath        string
	output            visibleShellOutput
	exitCode          int
	movedToBackground bool
}

func projectExecResult(result ExecResult) execPresentation {
	presentation := execPresentation{
		sessionID:         strings.TrimSpace(result.SessionID),
		wallTime:          result.WallTime,
		outputPath:        strings.TrimSpace(result.OutputPath),
		output:            newVisibleShellOutput(result.Output, result.Warning),
		movedToBackground: result.MovedToBackground,
	}
	if result.ExitCode == nil {
		presentation.kind = execPresentationRunning
		return presentation
	}
	presentation.exitCode = *result.ExitCode
	if result.Backgrounded {
		presentation.kind = execPresentationBackgroundCompleted
		return presentation
	}
	presentation.kind = execPresentationForegroundCompleted
	return presentation
}

func NormalizeBackgroundOutputMode(raw string) BackgroundOutputMode {
	switch BackgroundOutputMode(strings.ToLower(strings.TrimSpace(raw))) {
	case BackgroundOutputVerbose:
		return BackgroundOutputVerbose
	case BackgroundOutputConcise:
		return BackgroundOutputConcise
	default:
		return BackgroundOutputDefault
	}
}

type InvalidBackgroundEventError struct {
	EventType EventType
	ProcessID string
	State     string
}

func (e *InvalidBackgroundEventError) Error() string {
	return fmt.Sprintf("terminal background event %q for process %q in state %q is missing completion output", e.EventType, e.ProcessID, e.State)
}

type backgroundInlinePreview struct {
	text string
}

type backgroundNoticeOutputKind uint8

const (
	backgroundNoticeOutputNormal backgroundNoticeOutputKind = iota + 1
	backgroundNoticeOutputInvariantFailure
)

type invariantFailureNotice struct {
	message string
}

type backgroundNoticeOutput struct {
	kind                 backgroundNoticeOutputKind
	source               completionOutputSource
	visible              visibleShellOutput
	inlinePreview        *backgroundInlinePreview
	retainedLogHasOutput bool
	logLineCount         *int
	truncated            bool
	previewRemoved       int
	invariantFailure     *invariantFailureNotice
}

func (o backgroundNoticeOutput) shouldRenderNoOutputCompletion(exitCode *int) bool {
	return exitCode != nil && !o.visible.HasVisibleContent()
}

func (s BackgroundNoticeSummary) RuntimePreview() (string, int) {
	if s.output.inlinePreview == nil {
		return "", s.output.previewRemoved
	}
	return s.output.inlinePreview.text, s.output.previewRemoved
}

func SummarizeBackgroundEvent(evt Event, opts BackgroundNoticeOptions) (BackgroundNoticeSummary, error) {
	if (evt.Type == EventCompleted || evt.Type == EventKilled) && evt.completion == nil {
		return BackgroundNoticeSummary{}, &InvalidBackgroundEventError{
			EventType: evt.Type,
			ProcessID: evt.Snapshot.ID,
			State:     evt.Snapshot.State,
		}
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = defaultOutputTokenCap * 4
	}
	mode := effectiveBackgroundOutputMode(evt.Snapshot.ExitCode, opts.SuccessOutputMode)
	output, err := projectBackgroundNoticeOutput(evt, maxChars, mode)
	if err != nil {
		return BackgroundNoticeSummary{}, err
	}
	state := strings.TrimSpace(evt.Snapshot.State)
	if state == "" {
		state = strings.TrimSpace(string(evt.Type))
	}
	if state == "" {
		state = "completed"
	}
	detail := []string{fmt.Sprintf("Background shell %s %s.", evt.Snapshot.ID, state)}
	if evt.Snapshot.ExitCode != nil {
		detail = append(detail, fmt.Sprintf("Exit code: %d", *evt.Snapshot.ExitCode))
	}
	if strings.TrimSpace(evt.Snapshot.LogPath) != "" && output.logLineCount != nil {
		lineCountText := fmt.Sprintf("%d lines", *output.logLineCount)
		if *output.logLineCount == 1 {
			lineCountText = "1 line"
		}
		detail = append(detail, fmt.Sprintf("Output file (%s): %s", lineCountText, evt.Snapshot.LogPath))
	}
	if warning := output.visible.Warning(); warning != nil {
		detail = append(detail, warning.Text())
	}
	if output.inlinePreview != nil {
		detail = append(detail, "Output:")
		detail = append(detail, output.inlinePreview.text)
	}
	if output.shouldRenderNoOutputCompletion(evt.Snapshot.ExitCode) {
		detail = append(detail, fmt.Sprintf("Exit code %d, no output.", *evt.Snapshot.ExitCode))
	}
	summary := fmt.Sprintf("Background shell %s %s", evt.Snapshot.ID, state)
	if evt.Snapshot.ExitCode != nil {
		summary = fmt.Sprintf("%s (exit %d)", summary, *evt.Snapshot.ExitCode)
	}
	return BackgroundNoticeSummary{
		DetailText:    strings.Join(detail, "\n"),
		CondensedText: summary,
		LineCount:     valueOrZero(output.logLineCount),
		Truncated:     output.truncated,
		LogPath:       evt.Snapshot.LogPath,
		output:        output,
	}, nil
}

func effectiveBackgroundOutputMode(exitCode *int, successMode BackgroundOutputMode) BackgroundOutputMode {
	mode := NormalizeBackgroundOutputMode(string(successMode))
	if exitCode == nil {
		return BackgroundOutputDefault
	}
	if *exitCode == 0 {
		return mode
	}
	if mode == BackgroundOutputVerbose {
		return BackgroundOutputVerbose
	}
	return BackgroundOutputDefault
}

func projectBackgroundNoticeOutput(evt Event, maxChars int, mode BackgroundOutputMode) (backgroundNoticeOutput, error) {
	if evt.completion == nil {
		return backgroundNoticeOutput{}, nil
	}
	completion := evt.completion
	output := backgroundNoticeOutput{
		kind:           backgroundNoticeOutputNormal,
		source:         completion.source,
		visible:        completion.output,
		truncated:      completion.removed > 0,
		previewRemoved: completion.removed,
	}
	path := strings.TrimSpace(evt.Snapshot.LogPath)
	if path != "" {
		scanMode := mode
		if completion.source == completionOutputFinalized {
			scanMode = BackgroundOutputConcise
		}
		preview, lineCount, truncated, err := readBackgroundSummaryFromFile(path, maxChars, scanMode, !evt.Snapshot.RawOutput)
		if err != nil {
			warning, warningErr := postprocess.NewWarning(fmt.Sprintf("failed to read output preview: %v", err))
			if warningErr != nil {
				return backgroundNoticeOutput{}, warningErr
			}
			output.visible.warning = postprocess.MergeWarnings(output.visible.warning, warning)
		} else {
			if lineCount > 0 {
				output.retainedLogHasOutput = true
				output.logLineCount = &lineCount
			}
			if completion.source == completionOutputFallback {
				output.visible.command = preview
				output.truncated = output.truncated || truncated
				if truncated && output.previewRemoved == 0 {
					output.previewRemoved = 1
				}
			}
		}
	}
	if mode == BackgroundOutputConcise {
		return output, nil
	}
	command := output.visible.command
	if strings.TrimSpace(command) == "" {
		return output, nil
	}
	if mode == BackgroundOutputVerbose {
		output.inlinePreview = &backgroundInlinePreview{text: strings.TrimSpace(command)}
		return output, nil
	}
	preview, truncated, _ := truncateWithTemplate(strings.TrimSpace(command), maxChars, backgroundTruncationBannerTemplate)
	output.inlinePreview = &backgroundInlinePreview{text: preview}
	output.truncated = output.truncated || truncated
	if truncated && output.previewRemoved == 0 {
		output.previewRemoved = 1
	}
	return output, nil
}

func InvariantFailureBackgroundNotice(evt Event, cause error) BackgroundNoticeSummary {
	state := strings.TrimSpace(evt.Snapshot.State)
	if state == "" {
		state = strings.TrimSpace(string(evt.Type))
	}
	message := fmt.Sprintf("Background shell completion internal error: %v", cause)
	detail := []string{fmt.Sprintf("Background shell %s %s.", evt.Snapshot.ID, state)}
	if evt.Snapshot.ExitCode != nil {
		detail = append(detail, fmt.Sprintf("Exit code: %d", *evt.Snapshot.ExitCode))
	}
	detail = append(detail, message)
	summary := fmt.Sprintf("Background shell %s %s", evt.Snapshot.ID, state)
	if evt.Snapshot.ExitCode != nil {
		summary = fmt.Sprintf("%s (exit %d)", summary, *evt.Snapshot.ExitCode)
	}
	return BackgroundNoticeSummary{
		DetailText:    strings.Join(detail, "\n"),
		CondensedText: summary,
		LogPath:       evt.Snapshot.LogPath,
		output: backgroundNoticeOutput{
			kind: backgroundNoticeOutputInvariantFailure,
			invariantFailure: &invariantFailureNotice{
				message: message,
			},
		},
	}
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func readBackgroundSummaryFromFile(path string, maxChars int, mode BackgroundOutputMode, sanitize bool) (string, int, bool, error) {
	fp, err := os.Open(path)
	if err != nil {
		return "", 0, false, err
	}
	defer fp.Close()
	builder := newBackgroundPreviewBuilder(maxChars, mode, sanitize)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := fp.Read(buf)
		if n > 0 {
			builder.WriteRaw(buf[:n])
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			builder.Finish()
			return builder.Preview(), builder.LineCount(), builder.Truncated(), nil
		}
		return "", 0, false, readErr
	}
}

type backgroundPreviewBuilder struct {
	maxChars    int
	mode        BackgroundOutputMode
	sanitize    bool
	carry       []byte
	prevCR      bool
	totalBytes  int
	lineCount   int
	hasContent  bool
	lastNewline bool
	fullMode    bool
	full        []byte
	head        []byte
	tail        []byte
}

func newBackgroundPreviewBuilder(maxChars int, mode BackgroundOutputMode, sanitize bool) *backgroundPreviewBuilder {
	if maxChars <= 0 {
		maxChars = defaultOutputTokenCap * 4
	}
	mode = NormalizeBackgroundOutputMode(string(mode))
	return &backgroundPreviewBuilder{
		maxChars: maxChars,
		mode:     mode,
		sanitize: sanitize,
		fullMode: mode == BackgroundOutputVerbose,
		full:     make([]byte, 0, min(maxChars, 4096)),
		head:     make([]byte, 0, headTailSize),
		tail:     make([]byte, 0, headTailSize),
	}
}

func (b *backgroundPreviewBuilder) WriteRaw(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if !b.sanitize {
		b.emitBytes(chunk)
		return
	}
	data := append(append([]byte(nil), b.carry...), chunk...)
	processUpTo := len(data)
	if start, ok := trailingIncompleteANSIStart(data); ok {
		processUpTo = start
		b.carry = append(b.carry[:0], data[start:]...)
	} else {
		b.carry = b.carry[:0]
	}
	if processUpTo == 0 {
		return
	}
	b.writeSanitized(xansi.Strip(string(data[:processUpTo])))
}

func (b *backgroundPreviewBuilder) Finish() {
	if len(b.carry) == 0 {
		return
	}
	if !b.sanitize {
		b.emitBytes(b.carry)
		b.carry = b.carry[:0]
		return
	}
	b.writeSanitized(xansi.Strip(string(b.carry)))
	b.carry = b.carry[:0]
}

func (b *backgroundPreviewBuilder) writeSanitized(text string) {
	if text == "" {
		return
	}
	for _, r := range text {
		switch {
		case r == '\r':
			b.emitBytes([]byte{'\n'})
			b.prevCR = true
		case r == '\n':
			if b.prevCR {
				b.prevCR = false
				continue
			}
			b.emitBytes([]byte{'\n'})
		case r == '\t' || !unicode.IsControl(r):
			b.prevCR = false
			var buf [4]byte
			n := utf8EncodeRune(buf[:], r)
			b.emitBytes(buf[:n])
		default:
			b.prevCR = false
		}
	}
}

func (b *backgroundPreviewBuilder) emitBytes(data []byte) {
	if len(data) == 0 {
		return
	}
	b.hasContent = true
	b.totalBytes += len(data)
	if b.fullMode {
		b.full = append(b.full, data...)
	} else if len(b.full) < b.maxChars {
		remaining := b.maxChars - len(b.full)
		if remaining > len(data) {
			remaining = len(data)
		}
		b.full = append(b.full, data[:remaining]...)
	}
	if len(b.head) < headTailSize {
		remaining := headTailSize - len(b.head)
		if remaining > len(data) {
			remaining = len(data)
		}
		b.head = append(b.head, data[:remaining]...)
	}
	b.tail = append(b.tail, data...)
	if len(b.tail) > headTailSize {
		b.tail = append([]byte(nil), b.tail[len(b.tail)-headTailSize:]...)
	}
	for _, v := range data {
		if v == '\n' {
			b.lineCount++
			b.lastNewline = true
			continue
		}
		b.lastNewline = false
	}
}

func (b *backgroundPreviewBuilder) Preview() string {
	if b.mode == BackgroundOutputConcise {
		return ""
	}
	if b.fullMode {
		return string(b.full)
	}
	if b.totalBytes <= b.maxChars {
		return string(b.full)
	}
	headLen, tailLen := truncationSegmentLengths(b.totalBytes, b.maxChars)
	removed := b.totalBytes - headLen - tailLen
	head := string(b.head[:headLen])
	tail := string(b.tail[len(b.tail)-tailLen:])
	return fmt.Sprintf("%s%s%s", head, fmt.Sprintf(backgroundTruncationBannerTemplate, removed), tail)
}

func (b *backgroundPreviewBuilder) LineCount() int {
	if !b.hasContent {
		return 0
	}
	if b.lastNewline {
		return b.lineCount
	}
	return b.lineCount + 1
}

func (b *backgroundPreviewBuilder) Truncated() bool {
	if b.mode == BackgroundOutputConcise || b.fullMode {
		return false
	}
	return b.totalBytes > b.maxChars
}

func trailingIncompleteANSIStart(data []byte) (int, bool) {
	lastESC := bytes.LastIndexByte(data, 0x1b)
	if lastESC < 0 {
		return 0, false
	}
	for i := lastESC + 1; i < len(data); i++ {
		if data[i] == 0x07 || data[i] >= 0x40 && data[i] <= 0x7e {
			return 0, false
		}
	}
	return lastESC, true
}

func utf8EncodeRune(dst []byte, r rune) int {
	if r < 0x80 {
		dst[0] = byte(r)
		return 1
	}
	return copy(dst, []byte(string(r)))
}

func formatExecResponse(result ExecResult) string {
	return renderExecPresentation(projectExecResult(result))
}

func renderExecPresentation(presentation execPresentation) string {
	output := presentation.output.Content()
	switch presentation.kind {
	case execPresentationRunning:
		sections := make([]string, 0, 2)
		if presentation.movedToBackground {
			sections = append(sections, formatBackgroundTransitionLine(presentation.sessionID, presentation.output.HasCommandContent()))
		} else if presentation.sessionID != "" {
			sections = append(sections, fmt.Sprintf("Process running with session ID %s", presentation.sessionID))
		}
		if output == "" {
			sections = append(sections, noOutputText)
		} else {
			sections = append(sections, output)
		}
		return strings.Join(sections, "\n")
	case execPresentationForegroundCompleted:
		if output == "" {
			return fmt.Sprintf("Exit code %d, no output.", presentation.exitCode)
		}
		if presentation.exitCode == 0 {
			return output
		}
		return fmt.Sprintf("Exit code %d, output:\n%s", presentation.exitCode, output)
	case execPresentationBackgroundCompleted:
		if output == "" {
			return fmt.Sprintf("Exit code %d, no output.", presentation.exitCode)
		}
		sections := []string{fmt.Sprintf("Exit code %d, output:", presentation.exitCode)}
		sections = append(sections, fmt.Sprintf("Wall time: %.4f seconds", presentation.wallTime.Seconds()))
		if presentation.outputPath != "" {
			sections = append(sections, fmt.Sprintf("Log file: %s", presentation.outputPath))
		}
		sections = append(sections, output)
		return strings.Join(sections, "\n")
	default:
		panic(fmt.Sprintf("unknown exec presentation kind %d", presentation.kind))
	}
}

func formatToolError(warning postprocess.Warning, toolError string) string {
	sections := make([]string, 0, 2)
	if warning != nil {
		sections = append(sections, warning.Text())
	}
	if text := strings.TrimSpace(toolError); text != "" {
		sections = append(sections, text)
	}
	return strings.Join(sections, "\n")
}

func formatBackgroundTransitionLine(sessionID string, hasOutput bool) string {
	sessionID = strings.TrimSpace(sessionID)
	switch {
	case sessionID != "" && hasOutput:
		return fmt.Sprintf("Process moved to background with ID %s. Output:", sessionID)
	case sessionID != "":
		return fmt.Sprintf("Process moved to background with ID %s.", sessionID)
	case hasOutput:
		return "Process moved to background. Output:"
	default:
		return "Process moved to background."
	}
}
