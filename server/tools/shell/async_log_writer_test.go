package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAsyncLogWriterFlushesBufferedOutputOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shell.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	writer := newAsyncLogWriter(file, nil)
	chunks := [][]byte{
		[]byte("alpha\n"),
		bytes.Repeat([]byte("b"), logWriterFlushBytes/2),
		[]byte("omega\n"),
	}
	want := bytes.Join(chunks, nil)
	for _, chunk := range chunks {
		if err := writer.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("log content mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestAsyncLogWriterWriteAfterCloseReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shell.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	writer := newAsyncLogWriter(file, nil)
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := writer.Write([]byte("late")); err == nil {
		t.Fatal("expected write after close error")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
