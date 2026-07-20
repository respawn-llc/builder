package boundedjson

import (
	"fmt"
	"io"
)

type Scanner struct {
	source         io.ReaderAt
	start          int64
	end            int64
	offset         int64
	buffer         []byte
	bufferStart    int64
	bufferLength   int
	foldFieldNames bool
	closed         bool
}

func NewScanner(
	source io.ReaderAt,
	start int64,
	end int64,
	buffer []byte,
	foldFieldNames bool,
) (*Scanner, error) {
	if source == nil {
		return nil, fmt.Errorf("JSON source is required")
	}
	if start < 0 || end < start {
		return nil, fmt.Errorf("JSON range is invalid: [%d,%d)", start, end)
	}
	if len(buffer) == 0 {
		return nil, fmt.Errorf("JSON scanner buffer is required")
	}
	return &Scanner{
		source:         source,
		start:          start,
		end:            end,
		offset:         start,
		buffer:         buffer,
		bufferStart:    -1,
		foldFieldNames: foldFieldNames,
	}, nil
}

func (s *Scanner) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	s.buffer = nil
	return nil
}
