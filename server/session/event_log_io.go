package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

const (
	eventLogScanChunkSize             = int64(4096)
	activeTailReverseChunkBytes       = int64(1 << 20)
	currentEventLogTailRepairMaxBytes = int64(16 << 20)
)

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

func syncAndClose(fp *os.File, err error) error {
	if fp == nil {
		return errors.Join(err, os.ErrInvalid)
	}
	return errors.Join(err, fp.Sync(), fp.Close())
}

func lastNewlineOffset(fp *os.File, fileSize int64) (int64, error) {
	return lastNewlineOffsetWithin(fp, fileSize, fileSize)
}

func lastNewlineOffsetWithin(fp *os.File, fileSize int64, maxBytes int64) (int64, error) {
	if fileSize == 0 {
		return -1, nil
	}
	if maxBytes < 0 {
		return -1, fmt.Errorf("newline scan byte limit must be non-negative")
	}
	position := fileSize
	lowerBound := fileSize - maxBytes
	if lowerBound < 0 {
		lowerBound = 0
	}
	for position > lowerBound {
		chunkSize := eventLogScanChunkSize
		if position-lowerBound < chunkSize {
			chunkSize = position - lowerBound
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
