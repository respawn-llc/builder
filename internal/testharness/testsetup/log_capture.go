package testsetup

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
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
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(capturingSlogHandler{records: records}))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return records
}

func (r *SlogRecords) Records() []CapturedSlogRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]CapturedSlogRecord, len(r.records))
	for index, record := range r.records {
		fields := make(map[string]any, len(record.Fields))
		for key, value := range record.Fields {
			fields[key] = value
		}
		result[index] = CapturedSlogRecord{Level: record.Level, Fields: fields}
	}
	return result
}

type capturingSlogHandler struct {
	records *SlogRecords
	attrs   []slog.Attr
	group   string
}

func (h capturingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h capturingSlogHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make(map[string]any, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		addCapturedSlogAttr(fields, h.group, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addCapturedSlogAttr(fields, h.group, attr)
		return true
	})
	h.records.mu.Lock()
	h.records.records = append(h.records.records, CapturedSlogRecord{Level: record.Level, Fields: fields})
	h.records.mu.Unlock()
	return nil
}

func (h capturingSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return h
}

func (h capturingSlogHandler) WithGroup(name string) slog.Handler {
	if h.group == "" {
		h.group = name
	} else if name != "" {
		h.group += "." + name
	}
	return h
}

func addCapturedSlogAttr(fields map[string]any, group string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	key := attr.Key
	if group != "" {
		key = group + "." + key
	}
	fields[key] = attr.Value.Any()
}
