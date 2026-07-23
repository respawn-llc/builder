// Package boundedio provides bounded in-memory writers for process output.
package boundedio

import (
	"bytes"
	"errors"
)

var errNonpositiveLimit = errors.New("bounded writer limit must be positive")

type Writer struct {
	limit         int
	buffer        bytes.Buffer
	overflowBytes int64
}

func NewWriter(limit int) (*Writer, error) {
	if limit <= 0 {
		return nil, errNonpositiveLimit
	}
	return &Writer{limit: limit}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		if len(p) > 0 {
			w.overflowBytes += int64(len(p))
		}
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.buffer.Write(p[:remaining])
		w.overflowBytes += int64(len(p) - remaining)
		return len(p), nil
	}
	_, _ = w.buffer.Write(p)
	return len(p), nil
}

func (w *Writer) Bytes() []byte {
	return append([]byte(nil), w.buffer.Bytes()...)
}

func (w *Writer) String() string {
	return string(w.buffer.Bytes())
}

func (w *Writer) Overflow() bool {
	return w.overflowBytes > 0
}

func (w *Writer) OverflowBytes() int64 {
	return w.overflowBytes
}
