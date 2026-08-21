package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const currentEventLogHeaderMaxBytes = 4096

type currentEventLogMode uint8

const (
	currentEventLogAuthoritative currentEventLogMode = iota + 1
	currentEventLogReadOnly
	currentEventLogPersistedSnapshot
)

type currentEventLog struct {
	path               string
	version            int
	firstEventOffset   int64
	lastSequence       int64
	lastCompleteOffset int64
	boundaryIncomplete bool
	mode               currentEventLogMode
	durabilityObserver DurabilityObserver
}

type currentEventLogAppendTransaction struct {
	prepare  func(startOffset int64, payload []byte) error
	commit   func() error
	rollback func(fp *os.File, startOffset int64, appendErr error) error
}

type EventRecordWindow struct {
	Records      []EventRecord
	StartOffset  int64
	EndOffset    int64
	ReachedStart bool
	ReachedEnd   bool
}

func createCurrentEventLog(path string) (_ *currentEventLog, resultErr error) {
	return createCurrentEventLogVersion(path, EventLogVersionV2)
}

func createCurrentEventLogVersion(path string, version int) (_ *currentEventLog, resultErr error) {
	header, err := encodeEventLogHeader(version)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 0, len(header)+1)
	encoded = append(encoded, header...)
	encoded = append(encoded, '\n')

	fp, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create current event log: %w", err)
	}
	defer func() {
		if fp != nil {
			if closeErr := fp.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close current event log header: %w", closeErr))
			}
		}
	}()
	if _, err := writeAll(fp, encoded); err != nil {
		return nil, err
	}
	if err := fp.Sync(); err != nil {
		return nil, fmt.Errorf("sync current event log header: %w", err)
	}
	if err := fp.Close(); err != nil {
		return nil, fmt.Errorf("close current event log header: %w", err)
	}
	fp = nil

	return &currentEventLog{
		path:             path,
		version:          version,
		firstEventOffset: int64(len(encoded)),
		mode:             currentEventLogAuthoritative,
	}, nil
}

func openCurrentEventLog(
	path string,
	mode currentEventLogMode,
) (_ *currentEventLog, resultErr error) {
	switch mode {
	case currentEventLogAuthoritative, currentEventLogReadOnly, currentEventLogPersistedSnapshot:
	default:
		return nil, fmt.Errorf("unsupported current event log mode %d", mode)
	}
	var fp *os.File
	var err error
	if mode == currentEventLogPersistedSnapshot {
		fp, err = openRegularSessionFile(path, "current event log")
	} else {
		flags := os.O_RDONLY
		if mode == currentEventLogAuthoritative {
			flags = os.O_RDWR
		}
		fp, err = os.OpenFile(path, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open current event log: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close current event log: %w", closeErr))
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat current event log: %w", err)
	}
	size := info.Size()
	if size == 0 && mode == currentEventLogPersistedSnapshot {
		return &currentEventLog{
			path:    path,
			version: EventLogVersionV2,
			mode:    mode,
		}, nil
	}
	header, firstEventOffset, err := readCurrentEventLogHeader(fp)
	if err != nil {
		return nil, currentEventLogReadError(mode, err)
	}
	if mode == currentEventLogAuthoritative {
		size, _, err = repairCurrentEventLogTail(fp, size, firstEventOffset)
		if err != nil {
			return nil, err
		}
	}
	lastRecord, lastCompleteOffset, boundaryIncomplete, err := readLastCurrentEventRecord(fp, size, firstEventOffset)
	if err != nil {
		return nil, currentEventLogReadError(mode, err)
	}
	lastSequence := int64(0)
	if lastRecord != nil {
		lastSequence = lastRecord.Seq()
	}
	return &currentEventLog{
		path:               path,
		version:            header.Version,
		firstEventOffset:   firstEventOffset,
		lastSequence:       lastSequence,
		lastCompleteOffset: lastCompleteOffset,
		boundaryIncomplete: boundaryIncomplete,
		mode:               mode,
	}, nil
}

func currentEventLogReadError(mode currentEventLogMode, err error) error {
	if mode != currentEventLogPersistedSnapshot {
		return err
	}
	return wrapEventLogMaterializationError(EventLogMaterializationStageReconciliation, false, false, err)
}

func (l *currentEventLog) appendRecords(records []EventRecord) (endOffset int64, resultErr error) {
	return l.appendRecordsWithTransaction(records, nil)
}

func (l *currentEventLog) appendRecordsWithTransaction(
	records []EventRecord,
	transaction *currentEventLogAppendTransaction,
) (endOffset int64, resultErr error) {
	if l == nil {
		return 0, fmt.Errorf("current event log is required")
	}
	if l.mode != currentEventLogAuthoritative {
		return 0, fmt.Errorf("read-only current event log cannot append records")
	}
	if len(records) == 0 {
		info, err := os.Stat(l.path)
		if err != nil {
			return 0, fmt.Errorf("stat current event log: %w", err)
		}
		return info.Size(), nil
	}
	appendStarted := time.Now()
	defer func() {
		if l.durabilityObserver != nil {
			l.durabilityObserver.ObserveEventLogAppend(EventLogAppendObservation{
				RecordCount: len(records),
				Latency:     time.Since(appendStarted),
				Succeeded:   resultErr == nil,
			})
		}
	}()
	expectedSequence := l.lastSequence + 1
	for index, record := range records {
		if record.Seq() != expectedSequence {
			return 0, fmt.Errorf(
				"append current event record %d sequence %d, want %d after persisted sequence %d",
				index,
				record.Seq(),
				expectedSequence,
				l.lastSequence,
			)
		}
		expectedSequence++
	}

	fp, err := os.OpenFile(l.path, os.O_APPEND|os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("open current event log for append: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close current event log append: %w", closeErr))
		}
	}()
	header, firstEventOffset, err := readCurrentEventLogHeader(fp)
	if err != nil {
		return 0, err
	}
	if firstEventOffset != l.firstEventOffset {
		return 0, fmt.Errorf(
			"current event log header offset changed from %d to %d",
			l.firstEventOffset,
			firstEventOffset,
		)
	}
	if header.Version != l.version {
		return 0, fmt.Errorf("current event log version changed from %d to %d", l.version, header.Version)
	}
	info, err := fp.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat current event log before append: %w", err)
	}
	currentSize, needsSeparator, err := repairCurrentEventLogTail(fp, info.Size(), l.firstEventOffset)
	if err != nil {
		return 0, err
	}
	lastRecord, _, _, err := readLastCurrentEventRecord(fp, currentSize, l.firstEventOffset)
	if err != nil {
		return 0, err
	}
	persistedSequence := int64(0)
	if lastRecord != nil {
		persistedSequence = lastRecord.Seq()
	}
	if persistedSequence != l.lastSequence {
		return 0, fmt.Errorf(
			"current event log append sequence authority changed from %d to %d",
			l.lastSequence,
			persistedSequence,
		)
	}

	payload, err := encodeCurrentEventRecordLines(records, needsSeparator, l.version)
	if err != nil {
		return 0, err
	}
	if transaction != nil && transaction.prepare != nil {
		if err := transaction.prepare(currentSize, payload); err != nil {
			return 0, err
		}
	}
	written, err := writeAll(fp, payload)
	endOffset = currentSize + int64(written)
	if err != nil {
		if transaction != nil && transaction.rollback != nil {
			return endOffset, transaction.rollback(fp, currentSize, err)
		}
		return endOffset, err
	}
	if transaction != nil {
		if err := l.syncAppend(fp); err != nil {
			if transaction.rollback != nil {
				return endOffset, transaction.rollback(fp, currentSize, fmt.Errorf("fsync current event log: %w", err))
			}
			return endOffset, fmt.Errorf("fsync current event log: %w", err)
		}
		if transaction.commit != nil {
			if err := transaction.commit(); err != nil {
				if transaction.rollback != nil {
					return endOffset, transaction.rollback(fp, currentSize, err)
				}
				return endOffset, err
			}
		}
	} else if err := l.syncAppend(fp); err != nil {
		return endOffset, fmt.Errorf("fsync current event log: %w", err)
	}
	l.lastSequence = records[len(records)-1].Seq()
	return endOffset, nil
}

func (l *currentEventLog) syncAppend(fp *os.File) (resultErr error) {
	started := time.Now()
	defer func() {
		if l.durabilityObserver != nil {
			l.durabilityObserver.ObserveEventLogSync(EventLogSyncObservation{
				Latency:   time.Since(started),
				Succeeded: resultErr == nil,
			})
		}
	}()
	return fp.Sync()
}

func (l *currentEventLog) readSegmentForward(
	startOffset int64,
	chunkBytes int64,
	match func(EventRecord) bool,
) (_ EventRecordWindow, resultErr error) {
	if l == nil {
		return EventRecordWindow{}, fmt.Errorf("current event log is required")
	}
	if chunkBytes <= 0 {
		chunkBytes = activeTailReverseChunkBytes
	}
	fp, err := openRegularSessionFile(l.path, "current event log")
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("open current event log: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close current event log read: %w", closeErr))
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("stat current event log: %w", err)
	}
	size := l.boundedReadSize(info.Size())
	if startOffset <= 0 {
		startOffset = l.firstEventOffset
	}
	if startOffset < l.firstEventOffset {
		return EventRecordWindow{}, fmt.Errorf(
			"current event log cursor %d precedes first event offset %d",
			startOffset,
			l.firstEventOffset,
		)
	}
	if startOffset >= size {
		return EventRecordWindow{
			StartOffset:  size,
			EndOffset:    size,
			ReachedStart: size == l.firstEventOffset,
			ReachedEnd:   true,
		}, nil
	}
	var previousSequence *int64
	if startOffset > l.firstEventOffset {
		previous, err := readCurrentEventRecordBeforeOffset(
			fp,
			startOffset,
			l.firstEventOffset,
			l.version,
		)
		if err != nil {
			return EventRecordWindow{}, err
		}
		if previous == nil {
			return EventRecordWindow{}, fmt.Errorf(
				"current event log cursor %d has no preceding boundary record",
				startOffset,
			)
		}
		sequence := previous.Seq()
		previousSequence = &sequence
	}

	records := make([]EventRecord, 0)
	position := startOffset
	for position < size {
		record, nextOffset, err := readCurrentEventRecordAtOffset(
			fp,
			position,
			size,
			chunkBytes,
			l.version,
		)
		if err != nil {
			return EventRecordWindow{}, err
		}
		if record == nil {
			return EventRecordWindow{
				Records:      records,
				StartOffset:  startOffset,
				EndOffset:    size,
				ReachedStart: startOffset == l.firstEventOffset,
				ReachedEnd:   true,
			}, nil
		}
		if previousSequence != nil && record.Seq() <= *previousSequence {
			return EventRecordWindow{}, fmt.Errorf(
				"current event record sequence regressed at byte %d: previous=%d current=%d",
				position,
				*previousSequence,
				record.Seq(),
			)
		}
		if len(records) > 0 && match != nil && match(*record) {
			return EventRecordWindow{
				Records:      records,
				StartOffset:  startOffset,
				EndOffset:    position,
				ReachedStart: startOffset == l.firstEventOffset,
			}, nil
		}
		records = append(records, *record)
		sequence := record.Seq()
		previousSequence = &sequence
		position = nextOffset
	}
	return EventRecordWindow{
		Records:      records,
		StartOffset:  startOffset,
		EndOffset:    size,
		ReachedStart: startOffset == l.firstEventOffset,
		ReachedEnd:   true,
	}, nil
}

func (l *currentEventLog) readNewestSegmentBackward(
	chunkBytes int64,
	match func(EventRecord) bool,
) (EventRecordWindow, error) {
	if l == nil {
		return EventRecordWindow{}, fmt.Errorf("current event log is required")
	}
	info, err := os.Stat(l.path)
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("stat current event log: %w", err)
	}
	return l.readSegmentBackward(l.boundedReadSize(info.Size()), chunkBytes, match)
}

func (l *currentEventLog) readActiveSegment() (EventRecordWindow, error) {
	var matchErr error
	window, err := l.readNewestSegmentBackward(
		activeTailReverseChunkBytes,
		func(record EventRecord) bool {
			kind, err := record.Kind()
			if err != nil {
				matchErr = err
				return true
			}
			return kind == EventKindHistoryReplace
		},
	)
	if err != nil {
		return EventRecordWindow{}, err
	}
	if matchErr != nil {
		return EventRecordWindow{}, matchErr
	}
	return window, nil
}

func (l *currentEventLog) readRecentRecords(
	maxRecords int,
	chunkBytes int64,
) (_ EventRecordWindow, resultErr error) {
	if l == nil {
		return EventRecordWindow{}, fmt.Errorf("current event log is required")
	}
	info, err := os.Stat(l.path)
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("stat current event log: %w", err)
	}
	size := l.boundedReadSize(info.Size())
	if maxRecords <= 0 || size == l.firstEventOffset {
		return EventRecordWindow{
			StartOffset:  l.firstEventOffset,
			EndOffset:    size,
			ReachedStart: true,
			ReachedEnd:   true,
		}, nil
	}
	seen := 0
	window, err := l.readSegmentBackward(size, chunkBytes, func(EventRecord) bool {
		seen++
		return seen == maxRecords
	})
	if err != nil {
		return EventRecordWindow{}, err
	}
	if window.ReachedStart || len(window.Records) == 0 {
		return window, nil
	}

	fp, err := openRegularSessionFile(l.path, "current event log")
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("open current event log: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close current event log read: %w", closeErr))
		}
	}()
	older, err := readCurrentEventRecordBeforeOffset(
		fp,
		window.StartOffset,
		l.firstEventOffset,
		l.version,
	)
	if err != nil {
		return EventRecordWindow{}, err
	}
	if older == nil {
		return EventRecordWindow{}, fmt.Errorf(
			"recent current event window at byte %d has no preceding boundary record",
			window.StartOffset,
		)
	}
	if older.Seq() >= window.Records[0].Seq() {
		return EventRecordWindow{}, fmt.Errorf(
			"current event record sequence regressed across byte %d: older=%d newer=%d",
			window.StartOffset,
			older.Seq(),
			window.Records[0].Seq(),
		)
	}
	return window, nil
}

func (l *currentEventLog) readSegmentBackward(
	endOffset int64,
	chunkBytes int64,
	match func(EventRecord) bool,
) (_ EventRecordWindow, resultErr error) {
	if l == nil {
		return EventRecordWindow{}, fmt.Errorf("current event log is required")
	}
	if chunkBytes <= 0 {
		chunkBytes = activeTailReverseChunkBytes
	}
	fp, err := openRegularSessionFile(l.path, "current event log")
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("open current event log: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close current event log read: %w", closeErr))
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return EventRecordWindow{}, fmt.Errorf("stat current event log: %w", err)
	}
	size := l.boundedReadSize(info.Size())
	if endOffset > size {
		endOffset = size
	}
	if endOffset < l.firstEventOffset {
		return EventRecordWindow{}, fmt.Errorf(
			"current event log cursor %d precedes first event offset %d",
			endOffset,
			l.firstEventOffset,
		)
	}
	if endOffset == l.firstEventOffset {
		return EventRecordWindow{
			StartOffset:  l.firstEventOffset,
			EndOffset:    l.firstEventOffset,
			ReachedStart: true,
			ReachedEnd:   endOffset == size,
		}, nil
	}
	atEOF := endOffset == size
	var newerSequence *int64
	if endOffset < size {
		newer, _, err := readCurrentEventRecordAtOffset(fp, endOffset, size, chunkBytes, l.version)
		if err != nil {
			return EventRecordWindow{}, err
		}
		if newer == nil {
			return EventRecordWindow{}, fmt.Errorf(
				"current event log cursor %d does not identify a complete newer record",
				endOffset,
			)
		}
		sequence := newer.Seq()
		newerSequence = &sequence
	}

	reversed := make([]currentEventRecordAtOffset, 0)
	position := endOffset
	for position > l.firstEventOffset {
		line, recordOffset, terminated, err := readPreviousCurrentEventLine(
			fp,
			position,
			l.firstEventOffset,
		)
		if err != nil {
			return EventRecordWindow{}, err
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if !terminated && atEOF && position == endOffset {
				position = recordOffset
				continue
			}
			return EventRecordWindow{}, fmt.Errorf(
				"current event log contains an empty event line at byte %d",
				recordOffset,
			)
		}
		record, err := decodeEventRecordForVersion(l.version, trimmed)
		if err != nil {
			if !terminated && atEOF && position == endOffset && !json.Valid(trimmed) {
				position = recordOffset
				continue
			}
			return EventRecordWindow{}, fmt.Errorf(
				"decode current event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		if newerSequence != nil && record.Seq() >= *newerSequence {
			return EventRecordWindow{}, fmt.Errorf(
				"current event record sequence regressed across byte %d: older=%d newer=%d",
				position,
				record.Seq(),
				*newerSequence,
			)
		}
		reversed = append(reversed, currentEventRecordAtOffset{
			record: record,
			offset: recordOffset,
		})
		sequence := record.Seq()
		newerSequence = &sequence
		position = recordOffset
		if match != nil && match(record) {
			break
		}
	}

	records := make([]EventRecord, len(reversed))
	for index := range reversed {
		records[len(reversed)-1-index] = reversed[index].record
	}
	startOffset := l.firstEventOffset
	if len(reversed) > 0 {
		startOffset = reversed[len(reversed)-1].offset
	}
	return EventRecordWindow{
		Records:      records,
		StartOffset:  startOffset,
		EndOffset:    endOffset,
		ReachedStart: startOffset == l.firstEventOffset,
		ReachedEnd:   atEOF,
	}, nil
}

func (l *currentEventLog) boundedReadSize(actual int64) int64 {
	if l.mode == currentEventLogPersistedSnapshot && l.lastCompleteOffset < actual {
		return l.lastCompleteOffset
	}
	return actual
}

type currentEventRecordAtOffset struct {
	record EventRecord
	offset int64
}

func readCurrentEventRecordAtOffset(
	fp *os.File,
	offset int64,
	size int64,
	chunkBytes int64,
	version int,
) (*EventRecord, int64, error) {
	if offset >= size {
		return nil, size, nil
	}
	line := make([]byte, 0, chunkBytes)
	position := offset
	for position < size {
		chunk := chunkBytes
		if chunk > size-position {
			chunk = size - position
		}
		piece := make([]byte, chunk)
		if _, err := fp.ReadAt(piece, position); err != nil && !errors.Is(err, io.EOF) {
			return nil, offset, fmt.Errorf("read current event record at byte %d: %w", offset, err)
		}
		if newline := bytes.IndexByte(piece, '\n'); newline >= 0 {
			line = append(line, piece[:newline]...)
			record, err := decodeCompleteCurrentEventLine(line, offset, version)
			if err != nil {
				return nil, offset, err
			}
			return &record, position + int64(newline) + 1, nil
		}
		line = append(line, piece...)
		position += chunk
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, size, nil
	}
	record, err := decodeEventRecordForVersion(version, trimmed)
	if err != nil {
		if json.Valid(trimmed) {
			return nil, size, fmt.Errorf(
				"decode current event record at byte %d: %w",
				offset,
				err,
			)
		}
		return nil, size, nil
	}
	return &record, size, nil
}

func readCurrentEventRecordBeforeOffset(
	fp *os.File,
	endOffset int64,
	firstEventOffset int64,
	version int,
) (*EventRecord, error) {
	line, startOffset, _, err := readPreviousCurrentEventLine(
		fp,
		endOffset,
		firstEventOffset,
	)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, nil
	}
	record, err := decodeCompleteCurrentEventLine(line, startOffset, version)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func decodeCompleteCurrentEventLine(line []byte, offset int64, version int) (EventRecord, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return EventRecord{}, fmt.Errorf(
			"current event log contains an empty event line at byte %d",
			offset,
		)
	}
	record, err := decodeEventRecordForVersion(version, trimmed)
	if err != nil {
		return EventRecord{}, fmt.Errorf(
			"decode current event record at byte %d: %w",
			offset,
			err,
		)
	}
	return record, nil
}

func encodeCurrentEventRecordLines(records []EventRecord, needsSeparator bool, version int) ([]byte, error) {
	buffer := bytes.NewBuffer(nil)
	if needsSeparator {
		buffer.WriteByte('\n')
	}
	for _, record := range records {
		line, err := encodeEventRecordForVersion(version, record)
		if err != nil {
			return nil, err
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

func repairCurrentEventLogTail(
	fp *os.File,
	size int64,
	firstEventOffset int64,
) (repairedSize int64, needsSeparator bool, resultErr error) {
	if size < firstEventOffset {
		return 0, false, fmt.Errorf(
			"current event log size %d precedes first event offset %d",
			size,
			firstEventOffset,
		)
	}
	if size == firstEventOffset {
		return size, false, nil
	}
	lastByte := [1]byte{}
	if _, err := fp.ReadAt(lastByte[:], size-1); err != nil {
		return 0, false, fmt.Errorf("read current event log tail: %w", err)
	}
	if lastByte[0] == '\n' {
		return size, false, nil
	}
	lastNewline, err := lastNewlineOffset(fp, size)
	if err != nil {
		return 0, false, err
	}
	tailStart := lastNewline + 1
	if tailStart < firstEventOffset {
		return 0, false, fmt.Errorf(
			"current event log tail starts before first event offset: tail=%d first=%d",
			tailStart,
			firstEventOffset,
		)
	}
	tail := make([]byte, size-tailStart)
	if _, err := fp.ReadAt(tail, tailStart); err != nil {
		return 0, false, fmt.Errorf("read current event log tail record: %w", err)
	}
	trimmedTail := bytes.TrimSpace(tail)
	header, _, headerErr := readCurrentEventLogHeader(fp)
	if headerErr != nil {
		return 0, false, headerErr
	}
	if _, err := decodeEventRecordForVersion(header.Version, trimmedTail); err == nil {
		return size, true, nil
	} else if json.Valid(trimmedTail) {
		return 0, false, fmt.Errorf(
			"decode complete current event log tail at byte %d: %w",
			tailStart,
			err,
		)
	}
	if err := fp.Truncate(tailStart); err != nil {
		return 0, false, fmt.Errorf("truncate torn current event log tail: %w", err)
	}
	if err := fp.Sync(); err != nil {
		return tailStart, false, fmt.Errorf("sync repaired current event log: %w", err)
	}
	return tailStart, false, nil
}

func readLastCurrentEventRecord(
	fp *os.File,
	size int64,
	firstEventOffset int64,
) (*EventRecord, int64, bool, error) {
	endOffset := size
	tornTail := false
	for endOffset > firstEventOffset {
		line, startOffset, terminated, err := readPreviousCurrentEventLine(
			fp,
			endOffset,
			firstEventOffset,
		)
		if err != nil {
			return nil, 0, false, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			if terminated {
				return nil, 0, false, fmt.Errorf(
					"current event log contains an empty event line at byte %d",
					startOffset,
				)
			}
			tornTail = true
			endOffset = startOffset
			continue
		}
		trimmedLine := bytes.TrimSpace(line)
		header, _, headerErr := readCurrentEventLogHeader(fp)
		if headerErr != nil {
			return nil, 0, false, headerErr
		}
		record, err := decodeEventRecordForVersion(header.Version, trimmedLine)
		if err == nil {
			return &record, endOffset, tornTail, nil
		}
		if terminated || json.Valid(trimmedLine) {
			return nil, 0, false, fmt.Errorf(
				"decode current event record at byte %d: %w",
				startOffset,
				err,
			)
		}
		tornTail = true
		endOffset = startOffset
	}
	return nil, firstEventOffset, tornTail, nil
}

func encodeEventRecordForVersion(version int, record EventRecord) ([]byte, error) {
	switch version {
	case EventLogVersionV1:
		return encodeEventRecordV1(record)
	case EventLogVersionV2:
		return encodeEventRecordV2(record)
	default:
		return nil, fmt.Errorf("unsupported event log version %d", version)
	}
}

func decodeEventRecordForVersion(version int, line []byte) (EventRecord, error) {
	switch version {
	case EventLogVersionV1:
		return decodeEventRecordV1(line)
	case EventLogVersionV2:
		return decodeEventRecordV2(line)
	default:
		return EventRecord{}, fmt.Errorf("unsupported event log version %d", version)
	}
}

func readPreviousCurrentEventLine(
	fp *os.File,
	endOffset int64,
	firstEventOffset int64,
) (line []byte, startOffset int64, terminated bool, err error) {
	startOffset, lineEnd, terminated, err := previousCurrentEventLineRange(
		fp,
		endOffset,
		firstEventOffset,
	)
	if err != nil {
		return nil, 0, false, err
	}
	if lineEnd <= startOffset {
		return nil, startOffset, terminated, nil
	}
	line = make([]byte, lineEnd-startOffset)
	if _, err := fp.ReadAt(line, startOffset); err != nil {
		return nil, 0, false, fmt.Errorf("read current event line: %w", err)
	}
	return line, startOffset, terminated, nil
}

func readCurrentEventLogHeader(fp *os.File) (EventLogHeader, int64, error) {
	info, err := fp.Stat()
	if err != nil {
		return EventLogHeader{}, 0, fmt.Errorf("stat current event log: %w", err)
	}
	readBytes := info.Size()
	if readBytes > currentEventLogHeaderMaxBytes+1 {
		readBytes = currentEventLogHeaderMaxBytes + 1
	}
	if readBytes <= 0 {
		return EventLogHeader{}, 0, fmt.Errorf("current event log header is required")
	}
	buffer := make([]byte, readBytes)
	if _, err := fp.ReadAt(buffer, 0); err != nil {
		return EventLogHeader{}, 0, fmt.Errorf("read current event log header: %w", err)
	}
	newline := bytes.IndexByte(buffer, '\n')
	if newline < 0 {
		if info.Size() > currentEventLogHeaderMaxBytes {
			return EventLogHeader{}, 0, fmt.Errorf(
				"current event log header exceeds %d bytes",
				currentEventLogHeaderMaxBytes,
			)
		}
		return EventLogHeader{}, 0, fmt.Errorf("current event log header is incomplete")
	}
	header, err := decodeEventLogHeader(buffer[:newline])
	if err != nil {
		return EventLogHeader{}, 0, err
	}
	return header, int64(newline + 1), nil
}
