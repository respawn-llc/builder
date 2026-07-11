package analyzer

import (
	"fmt"
	"io"
	"sync"
)

type Writer struct {
	mu      sync.Mutex
	out     io.Writer
	nextSeq int
	pending []pendingMarker
}

type pendingMarker struct {
	kind     Kind
	windowID *WindowID
}

func NewWriter(out io.Writer) *Writer {
	return &Writer{out: out}
}

func (writer *Writer) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	if writer == nil || writer.out == nil {
		return 0, io.ErrClosedPipe
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	for len(writer.pending) > 0 {
		pending := writer.pending[0]
		marker, err := writer.nextMarker(pending.kind, pending.windowID)
		if err != nil {
			return 0, fmt.Errorf("encode queued checkpoint: %w", err)
		}
		if err := writeFull(writer.out, marker); err != nil {
			return 0, fmt.Errorf("write queued checkpoint: %w", err)
		}
		writer.pending = writer.pending[1:]
	}
	return writer.out.Write(payload)
}

func (writer *Writer) QueueBeforeNextWrite(kind Kind, windowID *WindowID) error {
	if writer == nil || writer.out == nil {
		return io.ErrClosedPipe
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := (Marker{Sequence: 1, Kind: kind, WindowID: windowID}).Validate(); err != nil {
		return err
	}
	var windowIDCopy *WindowID
	if windowID != nil {
		value := *windowID
		windowIDCopy = &value
	}
	writer.pending = append(writer.pending, pendingMarker{kind: kind, windowID: windowIDCopy})
	return nil
}

func (writer *Writer) Emit(kind Kind, windowID *WindowID) error {
	if writer == nil || writer.out == nil {
		return io.ErrClosedPipe
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	encoded, err := writer.nextMarker(kind, windowID)
	if err != nil {
		return err
	}
	if err := writeFull(writer.out, encoded); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}

func (writer *Writer) nextMarker(kind Kind, windowID *WindowID) ([]byte, error) {
	writer.nextSeq++
	encoded, err := Encode(Marker{
		Sequence: writer.nextSeq,
		Kind:     kind,
		WindowID: windowID,
	})
	if err != nil {
		writer.nextSeq--
		return nil, err
	}
	return encoded, nil
}

func writeFull(writer io.Writer, payload []byte) error {
	written, err := writer.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return fmt.Errorf("short checkpoint write: wrote=%d expected=%d: %w", written, len(payload), io.ErrShortWrite)
	}
	return nil
}
