package testsetup

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
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

type CapturedSlogRecord struct {
	Level  slog.Level
	Fields map[string]any
}

type SlogRecords struct {
	mu      sync.Mutex
	records []CapturedSlogRecord
}

func CaptureSlogRecords(t testing.TB) *SlogRecords {
	t.Helper()
	records := &SlogRecords{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capturingSlogHandler{records: records}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return records
}

func (r *SlogRecords) Records() []CapturedSlogRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CapturedSlogRecord(nil), r.records...)
}

type capturingSlogHandler struct{ records *SlogRecords }

func (capturingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h capturingSlogHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make(map[string]any, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		fields[attr.Key] = attr.Value.Resolve().Any()
		return true
	})
	h.records.mu.Lock()
	h.records.records = append(h.records.records, CapturedSlogRecord{Level: record.Level, Fields: fields})
	h.records.mu.Unlock()
	return nil
}
func (h capturingSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h capturingSlogHandler) WithGroup(string) slog.Handler      { return h }
