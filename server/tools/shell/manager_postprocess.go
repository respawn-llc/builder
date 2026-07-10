package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"core/server/tools/shell/postprocess"
	"core/shared/toolspec"
	"core/shared/valuecopy"
)

// ErrOutputLogExceedsFullReadLimit is returned when a captured output log is
// larger than the permitted full-read byte limit. Callers and tests match this
// with errors.Is rather than comparing rendered message text.
var ErrOutputLogExceedsFullReadLimit = errors.New("output log exceeds full-read limit")

func (m *Manager) applyPostprocessing(ctx context.Context, entry *processEntry, output string, exitCode *int, backgrounded bool, maxOutputChars int) (postprocess.Result, error) {
	if m == nil || m.postprocessor == nil {
		return postprocess.Result{Output: output}, nil
	}
	return m.postprocessor.Apply(ctx, postprocess.Request{
		ToolName:        toolspec.ToolExecCommand,
		CommandText:     entry.command,
		Workdir:         entry.workdir,
		OwnerSessionID:  entry.ownerSessionID,
		ExitCode:        valuecopy.Pointer(exitCode),
		Raw:             entry.raw,
		Output:          output,
		MaxDisplayChars: maxOutputChars,
		Backgrounded:    backgrounded,
	})
}

func readOutputFileLimited(path string, maxBytes int64) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("output log path is empty")
	}
	file, err := os.Open(trimmed)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	var reader io.Reader = file
	if maxBytes > 0 {
		reader = io.LimitReader(file, maxBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return "", fmt.Errorf("%w %d", ErrOutputLogExceedsFullReadLimit, maxBytes)
	}
	return string(data), nil
}
