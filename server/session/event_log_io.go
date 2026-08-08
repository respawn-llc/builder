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
	position := fileSize
	for position > 0 {
		chunkSize := eventLogScanChunkSize
		if position < chunkSize {
			chunkSize = position
		}
		start := position - chunkSize
		chunk := make([]byte, chunkSize)
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
