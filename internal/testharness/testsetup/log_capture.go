package testsetup

import (
	"bytes"
	"log/slog"
	"testing"
)

// CaptureSlog redirects the process logger for one test and restores it during
// cleanup. Tests retain ownership of the captured output and assertions.
func CaptureSlog(t testing.TB) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return &output
}
