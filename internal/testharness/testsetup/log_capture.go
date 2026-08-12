package testsetup

import (
	"bytes"
	"log/slog"
	"testing"
)

func CaptureSlog(t testing.TB) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}
