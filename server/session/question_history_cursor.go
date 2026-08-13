package session

import (
	"bytes"
	"errors"
	"fmt"
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
		line, recordOffset, terminated, err := readPreviousCurrentEventLine(
			c.fp,
			c.position,
			c.firstEventOffset,
		)
		if err != nil {
			return nil, err
		}
		c.position = recordOffset
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
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
		record, err := decodeEventRecordForVersion(c.version, trimmed)
		if err != nil {
			return nil, fmt.Errorf(
				"decode Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		kind, err := record.Kind()
		if err != nil {
			return nil, err
		}
		if kind == EventKindHistoryReplace {
			if c.historyWindows == c.maxHandoffs {
				c.historyOmitted = recordOffset > c.firstEventOffset
				c.done = true
				return nil, nil
			}
			c.historyWindows++
		}
		return &record, nil
	}
	c.done = true
	return nil, nil
}
