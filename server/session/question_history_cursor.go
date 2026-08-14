package session

import (
	"context"
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

func (c *QuestionHistoryCursor) Next(ctx context.Context) (*EventRecord, error) {
	if c == nil || c.fp == nil {
		return nil, errors.New("Question-history cursor is closed")
	}
	if ctx == nil {
		return nil, errors.New("Question-history context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.done {
		return nil, nil
	}
	reader := questionHistoryContextReaderAt{ctx: ctx, reader: c.fp}
	for c.position > c.firstEventOffset {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		recordOffset, lineEnd, terminated, err := previousCurrentEventLineRange(
			reader,
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
		inspection, err := inspectEventRecordStream(
			io.NewSectionReader(reader, recordOffset, lineEnd-recordOffset),
			c.version,
		)
		if err != nil {
			if !terminated {
				// A concurrent append may leave a torn newest line. Complete
				// unterminated records remain visible; only undecodable tails
				// are ignored.
				continue
			}
			return nil, fmt.Errorf(
				"decode Question-history event record at byte %d: %w",
				recordOffset,
				err,
			)
		}
		if inspection.Kind == EventKindHistoryReplace {
			if err := inspectHistoryReplacementRecordStream(
				io.NewSectionReader(reader, recordOffset, lineEnd-recordOffset),
			); err != nil {
				return nil, fmt.Errorf(
					"decode Question-history replacement at byte %d: %w",
					recordOffset,
					err,
				)
			}
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
		line, err := io.ReadAll(
			io.NewSectionReader(reader, recordOffset, lineEnd-recordOffset),
		)
		if err != nil {
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

type questionHistoryContextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r questionHistoryContextReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	read, readErr := r.reader.ReadAt(buffer, offset)
	return read, errors.Join(readErr, r.ctx.Err())
}
