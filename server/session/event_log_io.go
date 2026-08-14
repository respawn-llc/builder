package session

import (
	"bytes"
	"fmt"
	"os"
)

const (
	eventLogScanChunkSize       = int64(4096)
	activeTailReverseChunkBytes = int64(1 << 20)
)

type eventLogReconciliationObservation struct {
	reconciliation PersistedEventLogReconciliation
	version        uint64
}

func writeAll(fp *os.File, payload []byte) (int, error) {
	offset := 0
	for offset < len(payload) {
		written, err := fp.Write(payload[offset:])
		if err != nil {
			return offset, fmt.Errorf("append events file: %w", err)
		}
		if written == 0 {
			return offset, fmt.Errorf("append events file: wrote 0 bytes")
		}
		offset += written
	}
	return offset, nil
}

func lastNewlineOffset(fp *os.File, fileSize int64) (int64, error) {
	if fileSize == 0 {
		return -1, nil
	}
	buffer := make([]byte, eventLogScanChunkSize)
	position := fileSize
	for position > 0 {
		chunkSize := eventLogScanChunkSize
		if position < chunkSize {
			chunkSize = position
		}
		start := position - chunkSize
		chunk := buffer[:chunkSize]
		if _, err := fp.ReadAt(chunk, start); err != nil {
			return -1, fmt.Errorf("scan events file for newline: %w", err)
		}
		if idx := bytes.LastIndexByte(chunk, '\n'); idx >= 0 {
			return start + int64(idx), nil
		}
		position = start
	}
	return -1, nil
}

func previousCurrentEventLineRange(
	fp *os.File,
	endOffset int64,
	firstEventOffset int64,
) (startOffset int64, lineEnd int64, terminated bool, err error) {
	if endOffset <= firstEventOffset {
		return firstEventOffset, firstEventOffset, false, nil
	}
	lineEnd = endOffset
	lastByte := [1]byte{}
	if _, err := fp.ReadAt(lastByte[:], endOffset-1); err != nil {
		return 0, 0, false, fmt.Errorf("read current event line end: %w", err)
	}
	if lastByte[0] == '\n' {
		lineEnd--
		terminated = true
	}
	if lineEnd <= firstEventOffset {
		return firstEventOffset, firstEventOffset, terminated, nil
	}
	previousNewline, err := lastNewlineOffset(fp, lineEnd)
	if err != nil {
		return 0, 0, false, err
	}
	startOffset = previousNewline + 1
	if startOffset < firstEventOffset {
		startOffset = firstEventOffset
	}
	return startOffset, lineEnd, terminated, nil
}
