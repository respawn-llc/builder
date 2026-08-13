package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type QuestionHistoryCursor struct {
	fp               *os.File
	version          int
	firstEventOffset int64
	position         int64
	initialSize      int64
	maxHandoffs      int
	historyWindows   int
	historyOmitted   bool
	done             bool
}

func OpenQuestionHistoryCursor(
	sessionDir string,
	maxHandoffs int,
) (_ *QuestionHistoryCursor, resultErr error) {
	if maxHandoffs < 1 {
		return nil, fmt.Errorf("maximum handoffs must be positive: %d", maxHandoffs)
	}
	path := filepath.Join(sessionDir, eventsFile)
	fp, err := openRegularSessionFile(path, "Question-history event log")
	if err != nil {
		return nil, fmt.Errorf("open Question-history event log: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, fp.Close())
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat Question-history event log: %w", err)
	}
	if info.Size() == 0 {
		return nil, errors.New("Question-history event log is empty")
	}
	header, firstEventOffset, err := readCurrentEventLogHeader(fp)
	if err != nil {
		return nil, err
	}
	return &QuestionHistoryCursor{
		fp:               fp,
		version:          header.Version,
		firstEventOffset: firstEventOffset,
		position:         info.Size(),
		initialSize:      info.Size(),
		maxHandoffs:      maxHandoffs,
		historyWindows:   1,
	}, nil
}

func (c *QuestionHistoryCursor) Version() int {
	if c == nil {
		return 0
	}
	return c.version
}

func (c *QuestionHistoryCursor) InitialSize() int64 {
	if c == nil {
		return 0
	}
	return c.initialSize
}

func (c *QuestionHistoryCursor) HistoryOmitted() bool {
	return c != nil && c.historyOmitted
}

func (c *QuestionHistoryCursor) Close() error {
	if c == nil || c.fp == nil {
		return nil
	}
	err := c.fp.Close()
	c.fp = nil
	return err
}

func (c *QuestionHistoryCursor) Next() (*EventRecord, error) {
	if c == nil || c.fp == nil {
		return nil, errors.New("Question-history cursor is closed")
	}
	if c.done {
		return nil, nil
	}
	for c.position > c.firstEventOffset {
		recordOffset, lineEnd, terminated, err := previousCurrentEventLineRange(
			c.fp,
			c.position,
			c.firstEventOffset,
		)
		if err != nil {
			return nil, err
		}
		c.position = recordOffset
		if lineEnd == recordOffset {
			if !terminated {
				continue
			}
			return nil, fmt.Errorf(
				"Question-history event log contains an empty event line at byte %d",
				recordOffset,
			)
		}
		if !terminated {
			// A concurrent append may leave a torn newest line. The opened
			// cursor's contract permits ignoring that unfinished tail.
			continue
		}
		inspection, err := inspectEventRecordStream(
			io.NewSectionReader(c.fp, recordOffset, lineEnd-recordOffset),
			c.version,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"decode Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		if inspection.Kind == EventKindHistoryReplace {
			if c.historyWindows == c.maxHandoffs {
				c.historyOmitted = recordOffset > c.firstEventOffset
				c.done = true
				return nil, nil
			}
			c.historyWindows++
			continue
		}
		if inspection.Kind != EventKindToolCompletion || !inspection.QuestionCandidate {
			continue
		}
		line := make([]byte, lineEnd-recordOffset)
		if _, err := c.fp.ReadAt(line, recordOffset); err != nil {
			return nil, fmt.Errorf(
				"read Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		record, err := decodeEventRecordForVersion(c.version, line)
		if err != nil {
			return nil, fmt.Errorf(
				"decode Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		if record.Seq() != inspection.Sequence {
			return nil, fmt.Errorf(
				"Question-history event sequence changed during decode at byte %d: inspected %d decoded %d",
				recordOffset,
				inspection.Sequence,
				record.Seq(),
			)
		}
		return &record, nil
	}
	c.done = true
	return nil, nil
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
