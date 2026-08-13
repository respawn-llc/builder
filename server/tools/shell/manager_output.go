package shell

import (
	"context"
	"io"
	"os"
	"sync"

	"core/server/tools/shell/postprocess"
	"core/shared/serverapi"
)

type outputSubscription struct {
	processID string
	entry     *processEntry
	offset    int64
	closeOnce sync.Once
	closeCh   chan struct{}
}

func (m *Manager) SubscribeOutput(_ context.Context, processID string, offsetBytes int64) (OutputSubscription, error) {
	entry, err := m.entry(processID)
	if err != nil {
		return nil, err
	}
	entry.mu.Lock()
	outputAvailable := entry.logPath != ""
	retainedToBytes := entry.outputBytes
	entry.mu.Unlock()
	if !outputAvailable {
		return nil, serverapi.ErrStreamUnavailable
	}
	if offsetBytes > retainedToBytes {
		return nil, serverapi.ErrStreamGap
	}
	return &outputSubscription{processID: processID, entry: entry, offset: offsetBytes, closeCh: make(chan struct{})}, nil
}

func (s *outputSubscription) Next(ctx context.Context) (OutputChunk, error) {
	if s == nil {
		return OutputChunk{}, io.EOF
	}
	for {
		chunk, done, err := s.readNextChunk()
		if err != nil {
			return OutputChunk{}, err
		}
		if chunk.Text != "" {
			return chunk, nil
		}
		if done {
			return OutputChunk{}, io.EOF
		}

		notify := s.entry.notify
		doneCh := s.entry.done
		select {
		case <-ctx.Done():
			return OutputChunk{}, ctx.Err()
		case <-s.closeCh:
			return OutputChunk{}, io.EOF
		case <-notify:
		case <-doneCh:
		}
	}
}

func (s *outputSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	return nil
}

func (s *outputSubscription) readNextChunk() (OutputChunk, bool, error) {
	select {
	case <-s.closeCh:
		return OutputChunk{}, true, nil
	default:
	}
	s.entry.mu.Lock()
	logPath := s.entry.logPath
	running := s.entry.running
	preserveOutput := s.entry.preserveOutput
	s.entry.mu.Unlock()

	file, err := os.Open(logPath)
	if err != nil {
		return OutputChunk{}, false, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(s.offset, io.SeekStart); err != nil {
		return OutputChunk{}, false, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return OutputChunk{}, false, err
	}
	if len(data) > 0 {
		nextOffset := s.offset + int64(len(data))
		chunk := OutputChunk{
			ProcessID:       s.processID,
			OffsetBytes:     s.offset,
			NextOffsetBytes: nextOffset,
		}
		if preserveOutput {
			chunk.Text = string(data)
		} else {
			chunk.Text = postprocess.SanitizeOutput(string(data))
		}
		s.offset = nextOffset
		return chunk, false, nil
	}
	return OutputChunk{}, !running, nil
}

var _ OutputSubscription = (*outputSubscription)(nil)
