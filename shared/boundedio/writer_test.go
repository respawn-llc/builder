package boundedio

import "testing"

func TestWriterPreservesAnExactLimitWrite(t *testing.T) {
	writer, err := NewWriter(4)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	written, err := writer.Write([]byte("four"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	if written != 4 {
		t.Fatalf("written = %d, want 4", written)
	}
	if got := string(writer.Bytes()); got != "four" {
		t.Fatalf("bytes = %q, want %q", got, "four")
	}
	if writer.Overflow() {
		t.Fatal("exact limit write must not overflow")
	}
}

func TestWriterAccountsForOverflowBytes(t *testing.T) {
	writer, err := NewWriter(4)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	written, err := writer.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	if written != 6 {
		t.Fatalf("written = %d, want 6", written)
	}
	if got := string(writer.Bytes()); got != "abcd" {
		t.Fatalf("bytes = %q, want %q", got, "abcd")
	}
	if !writer.Overflow() {
		t.Fatal("overflow write must report overflow")
	}
	if got := writer.OverflowBytes(); got != 2 {
		t.Fatalf("overflow bytes = %d, want 2", got)
	}
}

func TestWriterReturnsCopySafeBytesAndStringSnapshots(t *testing.T) {
	writer, err := NewWriter(8)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatalf("write initial output: %v", err)
	}

	bytesSnapshot := writer.Bytes()
	stringSnapshot := writer.String()
	bytesSnapshot[0] = 'X'
	if _, err := writer.Write([]byte("ing")); err != nil {
		t.Fatalf("write later output: %v", err)
	}

	if got := string(writer.Bytes()); got != "firsting" {
		t.Fatalf("writer bytes = %q, want %q", got, "firsting")
	}
	if got := stringSnapshot; got != "first" {
		t.Fatalf("string snapshot = %q, want %q", got, "first")
	}
}

func TestNewWriterRejectsNonpositiveLimits(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run("limit", func(t *testing.T) {
			writer, err := NewWriter(limit)
			if err == nil {
				t.Fatalf("NewWriter(%d) unexpectedly succeeded with %#v", limit, writer)
			}
			if writer != nil {
				t.Fatalf("NewWriter(%d) writer = %#v, want nil", limit, writer)
			}
		})
	}
}

func TestWriterBoundsMultichunkWrites(t *testing.T) {
	writer, err := NewWriter(5)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	for _, chunk := range [][]byte{[]byte("ab"), []byte("cdef"), []byte("gh")} {
		written, err := writer.Write(chunk)
		if err != nil {
			t.Fatalf("write %q: %v", chunk, err)
		}
		if written != len(chunk) {
			t.Fatalf("write %q reported %d bytes, want %d", chunk, written, len(chunk))
		}
	}

	if got := string(writer.Bytes()); got != "abcde" {
		t.Fatalf("bytes = %q, want %q", got, "abcde")
	}
	if got := writer.OverflowBytes(); got != 3 {
		t.Fatalf("overflow bytes = %d, want 3", got)
	}
}
