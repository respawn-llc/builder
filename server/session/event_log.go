package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const eventLogScanChunkSize = int64(4096)

type parsedEvents struct {
	events             []Event
	totalBytes         int64
	lastSequence       int64
	droppedTrailingEOF bool
}

func (s *Store) bootstrapEventLogStateLocked() error {
	if !s.persisted {
		return nil
	}
	info, err := os.Stat(s.eventsFP)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if writeErr := os.WriteFile(s.eventsFP, nil, 0o644); writeErr != nil {
				return fmt.Errorf("initialize missing events file: %w", writeErr)
			}
			s.eventsFileSizeBytes = 0
			s.pendingFsyncWrites = 0
			s.conversationFreshness = ConversationFreshnessFresh
			if s.meta.LastSequence != 0 {
				s.meta.LastSequence = 0
				s.meta.UpdatedAt = time.Now().UTC()
				if _, persistErr := s.persistMetaLocked(); persistErr != nil {
					return persistErr
				}
			}
			return nil
		}
		return err
	}
	s.eventsFileSizeBytes = info.Size()
	s.pendingFsyncWrites = 0

	window, err := readRecentEventsBackwardFile(s.eventsFP, 0, bootstrapTailRecoveryEvents, activeTailReverseChunkBytes)
	if err != nil {
		return err
	}

	established := s.meta.ConversationEstablished
	if !established {
		for _, evt := range window.Events {
			if hasVisibleUserMessageEvent(evt.Kind, evt.Payload) {
				established = true
				break
			}
		}
	}
	if established {
		s.conversationFreshness = ConversationFreshnessEstablished
	} else {
		s.conversationFreshness = ConversationFreshnessFresh
	}

	lastSequence := int64(0)
	if n := len(window.Events); n > 0 {
		lastSequence = window.Events[n-1].Seq
	}

	metaChanged := false
	if lastSequence != s.meta.LastSequence {
		s.meta.LastSequence = lastSequence
		metaChanged = true
	}
	if established && !s.meta.ConversationEstablished {
		s.meta.ConversationEstablished = true
		metaChanged = true
	}
	if runs := ProjectRuns(window.Events); len(runs) > 0 {
		latest := runs[len(runs)-1]
		current := s.meta.LatestRun
		if current == nil || current.RunID != latest.RunID || current.Status != latest.Status ||
			!current.StartedAt.Equal(latest.StartedAt) || !current.FinishedAt.Equal(latest.FinishedAt) {
			runCopy := latest
			s.meta.LatestRun = &runCopy
			metaChanged = true
		}
	}
	if metaChanged {
		s.meta.UpdatedAt = time.Now().UTC()
		if _, err := s.persistMetaLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) appendEventsLogLocked(events []Event) (int64, error) {
	fp, err := os.OpenFile(s.eventsFP, os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open events file for append: %w", err)
	}
	defer func() { _ = fp.Close() }()

	fileInfo, err := fp.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat events file: %w", err)
	}
	currentSize := fileInfo.Size()
	s.eventsFileSizeBytes = currentSize

	needsSeparator, err := s.repairTrailingLineLocked(fp, currentSize)
	if err != nil {
		return 0, err
	}

	fileInfo, err = fp.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat events file after repair: %w", err)
	}
	currentSize = fileInfo.Size()
	s.eventsFileSizeBytes = currentSize

	payload, err := encodeEventLines(events, needsSeparator)
	if err != nil {
		return 0, err
	}
	writtenBytes, err := writeAll(fp, payload)
	if err != nil {
		return 0, err
	}
	if err := s.maybeSyncEventsFileLocked(fp); err != nil {
		return 0, err
	}

	written := int64(writtenBytes)
	s.eventsFileSizeBytes += written
	return written, nil
}

func (s *Store) maybeSyncEventsFileLocked(fp *os.File) error {
	switch s.options.eventLog.fsyncPolicy {
	case EventLogFSyncNever:
		return nil
	case EventLogFSyncAlways:
		if err := fp.Sync(); err != nil {
			return fmt.Errorf("fsync events file: %w", err)
		}
		s.pendingFsyncWrites = 0
		return nil
	case EventLogFSyncPeriodic:
		s.pendingFsyncWrites++
		if s.pendingFsyncWrites < s.options.eventLog.fsyncIntervalWrites {
			return nil
		}
		if err := fp.Sync(); err != nil {
			return fmt.Errorf("fsync events file: %w", err)
		}
		s.pendingFsyncWrites = 0
		return nil
	default:
		return nil
	}
}

func (s *Store) repairTrailingLineLocked(fp *os.File, fileSize int64) (bool, error) {
	if fileSize == 0 {
		return false, nil
	}
	lastByte := [1]byte{}
	if _, err := fp.ReadAt(lastByte[:], fileSize-1); err != nil {
		return false, fmt.Errorf("read events file tail: %w", err)
	}
	if lastByte[0] == '\n' {
		return false, nil
	}

	lastNewlineOffset, err := lastNewlineOffset(fp, fileSize)
	if err != nil {
		return false, err
	}
	tailStart := lastNewlineOffset + 1
	tailLength := fileSize - tailStart
	if tailLength <= 0 {
		return false, nil
	}
	tail := make([]byte, tailLength)
	if _, err := fp.ReadAt(tail, tailStart); err != nil {
		return false, fmt.Errorf("read events tail line: %w", err)
	}

	trimmedTail := bytes.TrimSpace(tail)
	if len(trimmedTail) > 0 {
		var parsed Event
		if err := json.Unmarshal(trimmedTail, &parsed); err == nil {
			return true, nil
		}
	}
	if err := fp.Truncate(tailStart); err != nil {
		return false, fmt.Errorf("truncate events file tail: %w", err)
	}
	if _, err := fp.Seek(0, io.SeekEnd); err != nil {
		return false, fmt.Errorf("seek events file end: %w", err)
	}
	s.eventsFileSizeBytes = tailStart
	return false, nil
}

func walkEventsFile(path string, visit func(Event) error) (parsedEvents, error) {
	fp, err := openRegularSessionFile(path, "events file")
	if err != nil {
		return parsedEvents{}, fmt.Errorf("open events file: %w", err)
	}
	defer func() { _ = fp.Close() }()
	return walkEventsFromReader(bufio.NewReader(fp), visit)
}

func walkEventsFromReader(reader *bufio.Reader, visit func(Event) error) (parsedEvents, error) {
	totalBytes := int64(0)
	droppedTrailingEOF := false
	lastSequence := int64(0)
	for {
		line, readErr := reader.ReadString('\n')
		totalBytes += int64(len(line))
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var evt Event
			if err := json.Unmarshal([]byte(trimmed), &evt); err != nil {
				if errors.Is(readErr, io.EOF) && !strings.HasSuffix(line, "\n") {
					droppedTrailingEOF = true
					break
				}
				return parsedEvents{}, fmt.Errorf("parse event line: %w", err)
			}
			lastSequence = evt.Seq
			if visit != nil {
				if err := visit(evt); err != nil {
					return parsedEvents{}, err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return parsedEvents{}, fmt.Errorf("read events line: %w", readErr)
		}
	}
	return parsedEvents{totalBytes: totalBytes, lastSequence: lastSequence, droppedTrailingEOF: droppedTrailingEOF}, nil
}

const activeTailReverseChunkBytes = int64(1 << 20)

const bootstrapTailRecoveryEvents = 512

type eventAtOffset struct {
	event  Event
	offset int64
}

type SegmentWindow struct {
	Events       []Event
	StartOffset  int64
	EndOffset    int64
	ReachedStart bool
	ReachedEnd   bool
}

func readSegmentBackwardFile(path string, endOffset int64, chunkBytes int64, match func(Event) bool) (SegmentWindow, error) {
	if chunkBytes <= 0 {
		chunkBytes = activeTailReverseChunkBytes
	}
	fp, err := openRegularSessionFile(path, "events file")
	if err != nil {
		return SegmentWindow{}, fmt.Errorf("open events file: %w", err)
	}
	defer func() { _ = fp.Close() }()
	size, err := fp.Seek(0, io.SeekEnd)
	if err != nil {
		return SegmentWindow{}, fmt.Errorf("seek events file: %w", err)
	}
	if endOffset <= 0 || endOffset > size {
		endOffset = size
	}
	if endOffset == 0 {
		return SegmentWindow{ReachedStart: true, ReachedEnd: true}, nil
	}
	atEOF := endOffset == size
	var buffer []byte
	pos := endOffset
	for pos > 0 {
		chunk := chunkBytes
		if chunk > pos {
			chunk = pos
		}
		pos -= chunk
		tmp := make([]byte, chunk)
		if _, err := fp.ReadAt(tmp, pos); err != nil && !errors.Is(err, io.EOF) {
			return SegmentWindow{}, fmt.Errorf("read events file: %w", err)
		}
		next := make([]byte, 0, len(tmp)+len(buffer))
		next = append(next, tmp...)
		next = append(next, buffer...)
		buffer = next
		window, done, err := segmentFromBuffer(buffer, pos, pos == 0, atEOF, match)
		if err != nil {
			return SegmentWindow{}, err
		}
		if done {
			window.EndOffset = endOffset
			window.ReachedEnd = atEOF
			return window, nil
		}
	}
	window, _, err := segmentFromBuffer(buffer, 0, true, atEOF, match)
	if err != nil {
		return SegmentWindow{}, err
	}
	window.EndOffset = endOffset
	window.ReachedEnd = atEOF
	return window, nil
}

func readSegmentForwardFile(path string, startOffset int64, chunkBytes int64, match func(Event) bool) (SegmentWindow, error) {
	if chunkBytes <= 0 {
		chunkBytes = activeTailReverseChunkBytes
	}
	fp, err := openRegularSessionFile(path, "events file")
	if err != nil {
		return SegmentWindow{}, fmt.Errorf("open events file: %w", err)
	}
	defer func() { _ = fp.Close() }()
	size, err := fp.Seek(0, io.SeekEnd)
	if err != nil {
		return SegmentWindow{}, fmt.Errorf("seek events file: %w", err)
	}
	if startOffset < 0 {
		startOffset = 0
	}
	if startOffset >= size {
		return SegmentWindow{StartOffset: size, EndOffset: size, ReachedStart: size == 0, ReachedEnd: true}, nil
	}
	var buffer []byte
	pos := startOffset
	for pos < size {
		chunk := chunkBytes
		if chunk > size-pos {
			chunk = size - pos
		}
		tmp := make([]byte, chunk)
		if _, err := fp.ReadAt(tmp, pos); err != nil && !errors.Is(err, io.EOF) {
			return SegmentWindow{}, fmt.Errorf("read events file: %w", err)
		}
		buffer = append(buffer, tmp...)
		pos += chunk
		atEOF := pos == size
		window, done, err := forwardSegmentFromBuffer(buffer, startOffset, atEOF, match)
		if err != nil {
			return SegmentWindow{}, err
		}
		if done {
			return window, nil
		}
	}
	window, _, err := forwardSegmentFromBuffer(buffer, startOffset, true, match)
	return window, err
}

func readRecentEventsBackwardFile(path string, endOffset int64, maxEvents int, chunkBytes int64) (SegmentWindow, error) {
	if maxEvents <= 0 {
		return SegmentWindow{ReachedStart: true}, nil
	}
	if chunkBytes <= 0 {
		chunkBytes = activeTailReverseChunkBytes
	}
	fp, err := openRegularSessionFile(path, "events file")
	if err != nil {
		return SegmentWindow{}, fmt.Errorf("open events file: %w", err)
	}
	defer func() { _ = fp.Close() }()
	size, err := fp.Seek(0, io.SeekEnd)
	if err != nil {
		return SegmentWindow{}, fmt.Errorf("seek events file: %w", err)
	}
	if endOffset <= 0 || endOffset > size {
		endOffset = size
	}
	if endOffset == 0 {
		return SegmentWindow{ReachedStart: true}, nil
	}
	atEOF := endOffset == size
	var buffer []byte
	pos := endOffset
	for pos > 0 {
		chunk := chunkBytes
		if chunk > pos {
			chunk = pos
		}
		pos -= chunk
		tmp := make([]byte, chunk)
		if _, err := fp.ReadAt(tmp, pos); err != nil && !errors.Is(err, io.EOF) {
			return SegmentWindow{}, fmt.Errorf("read events file: %w", err)
		}
		next := make([]byte, 0, len(tmp)+len(buffer))
		next = append(next, tmp...)
		next = append(next, buffer...)
		buffer = next
		window, done, err := recentWindowFromBuffer(buffer, pos, pos == 0, atEOF, maxEvents)
		if err != nil {
			return SegmentWindow{}, err
		}
		if done {
			return window, nil
		}
	}
	window, _, err := recentWindowFromBuffer(buffer, 0, true, atEOF, maxEvents)
	return window, err
}

func recentWindowFromBuffer(buffer []byte, baseOffset int64, atStart, atEOF bool, maxEvents int) (SegmentWindow, bool, error) {
	parsed, err := completeEventsWithOffsets(buffer, baseOffset, atStart, atEOF)
	if err != nil {
		return SegmentWindow{}, false, err
	}
	if len(parsed) >= maxEvents {
		seg := parsed[len(parsed)-maxEvents:]
		return SegmentWindow{
			Events:       eventsOfOffsets(seg),
			StartOffset:  seg[0].offset,
			ReachedStart: seg[0].offset == 0,
		}, true, nil
	}
	if atStart {
		start := int64(0)
		if len(parsed) > 0 {
			start = parsed[0].offset
		}
		return SegmentWindow{Events: eventsOfOffsets(parsed), StartOffset: start, ReachedStart: true}, true, nil
	}
	return SegmentWindow{}, false, nil
}

func segmentFromBuffer(buffer []byte, baseOffset int64, atStart, atEOF bool, match func(Event) bool) (SegmentWindow, bool, error) {
	parsed, err := completeEventsWithOffsets(buffer, baseOffset, atStart, atEOF)
	if err != nil {
		return SegmentWindow{}, false, err
	}
	last := -1
	for i := range parsed {
		if match != nil && match(parsed[i].event) {
			last = i
		}
	}
	if last >= 0 {
		seg := parsed[last:]
		return SegmentWindow{
			Events:       eventsOfOffsets(seg),
			StartOffset:  seg[0].offset,
			ReachedStart: seg[0].offset == 0,
		}, true, nil
	}
	if atStart {
		start := int64(0)
		if len(parsed) > 0 {
			start = parsed[0].offset
		}
		return SegmentWindow{Events: eventsOfOffsets(parsed), StartOffset: start, ReachedStart: true}, true, nil
	}
	return SegmentWindow{}, false, nil
}

func forwardSegmentFromBuffer(buffer []byte, startOffset int64, atEOF bool, match func(Event) bool) (SegmentWindow, bool, error) {
	parsed, err := completeEventsWithOffsets(buffer, startOffset, true, atEOF)
	if err != nil {
		return SegmentWindow{}, false, err
	}
	for i := range parsed {
		if parsed[i].offset > startOffset && match != nil && match(parsed[i].event) {
			seg := parsed[:i]
			return SegmentWindow{
				Events:       eventsOfOffsets(seg),
				StartOffset:  startOffset,
				EndOffset:    parsed[i].offset,
				ReachedStart: startOffset == 0,
				ReachedEnd:   false,
			}, true, nil
		}
	}
	if atEOF {
		end := startOffset
		if len(buffer) > 0 {
			end = startOffset + int64(len(buffer))
		}
		return SegmentWindow{
			Events:       eventsOfOffsets(parsed),
			StartOffset:  startOffset,
			EndOffset:    end,
			ReachedStart: startOffset == 0,
			ReachedEnd:   true,
		}, true, nil
	}
	return SegmentWindow{}, false, nil
}

func completeEventsWithOffsets(buffer []byte, baseOffset int64, atStart, atEOF bool) ([]eventAtOffset, error) {
	start := 0
	if !atStart {
		nl := bytes.IndexByte(buffer, '\n')
		if nl < 0 {
			return nil, nil
		}
		start = nl + 1
	}
	out := make([]eventAtOffset, 0)
	for i := start; i < len(buffer); {
		nl := bytes.IndexByte(buffer[i:], '\n')
		torn := nl < 0
		lineEnd := len(buffer)
		if !torn {
			lineEnd = i + nl
		}
		if torn && !atEOF {
			break
		}
		trimmed := bytes.TrimSpace(buffer[i:lineEnd])
		if len(trimmed) > 0 {
			var evt Event
			if err := json.Unmarshal(trimmed, &evt); err != nil {
				if !(torn && atEOF) {
					return nil, fmt.Errorf("parse event line: %w", err)
				}
			} else {
				out = append(out, eventAtOffset{event: evt, offset: baseOffset + int64(i)})
			}
		}
		if torn {
			break
		}
		i = lineEnd + 1
	}
	return out, nil
}

func eventsOfOffsets(items []eventAtOffset) []Event {
	events := make([]Event, len(items))
	for i := range items {
		events[i] = items[i].event
	}
	return events
}

func encodeEventLines(events []Event, hasExistingContent bool) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	if hasExistingContent {
		buf.WriteByte('\n')
	}
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("marshal event line: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
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
