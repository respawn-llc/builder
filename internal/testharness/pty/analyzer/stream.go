package analyzer

import (
	"errors"
	"fmt"

	"github.com/gdamore/tcell/v3/vt"
)

// Stream is the sole terminal interpretation path for both live PTY bytes and
// persisted capture replay. It owns no PTY I/O; callers supply bytes and
// observer-ordered resize barriers.
type Stream struct {
	backend     *tracingBackend
	emulator    vt.Emulator
	sideChannel *sequenceSideChannel
	offset      int64
	finished    bool
}

func NewStream(dimensions Dimensions) (*Stream, error) {
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return nil, err
	}
	backend := newTracingBackend(dimensions)
	emulator := vt.NewEmulator(backend)
	if err := emulator.Start(); err != nil {
		return nil, fmt.Errorf("start terminal emulator: %w", err)
	}
	return &Stream{
		backend:     backend,
		emulator:    emulator,
		sideChannel: newSequenceSideChannel(backend),
	}, nil
}

func (s *Stream) Offset() int64 {
	if s == nil {
		return 0
	}
	return s.offset
}

func (s *Stream) Feed(payload []byte) error {
	if s == nil {
		return errors.New("terminal stream is required")
	}
	if s.finished {
		return errors.New("terminal stream is finished")
	}
	source := Chunk{Index: 0}
	for _, b := range payload {
		s.backend.beginByte(source, s.offset)
		s.sideChannel.advance(b, source, s.offset)
		if _, err := s.emulator.Write([]byte{b}); err != nil {
			return fmt.Errorf("analyze byte at offset %d: %w", s.offset, err)
		}
		if err := s.backend.error(); err != nil {
			return fmt.Errorf("analyze byte at offset %d: %w", s.offset, err)
		}
		s.offset++
	}
	return nil
}

func (s *Stream) Resize(dimensions Dimensions) error {
	if s == nil {
		return errors.New("terminal stream is required")
	}
	if s.finished {
		return errors.New("terminal stream is finished")
	}
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return err
	}
	s.backend.resize(dimensions, 0)
	s.emulator.ResizeEvent(vt.Coord{X: vt.Col(dimensions.Cols), Y: vt.Row(dimensions.Rows)})
	return nil
}

func (s *Stream) Finish() (Analysis, error) {
	if s == nil {
		return Analysis{}, errors.New("terminal stream is required")
	}
	if s.finished {
		return Analysis{}, errors.New("terminal stream is already finished")
	}
	s.finished = true
	defer func() {
		_ = s.emulator.Stop()
	}()
	if err := s.emulator.Drain(); err != nil {
		return Analysis{}, fmt.Errorf("drain terminal emulator: %w", err)
	}
	if err := s.sideChannel.error(); err != nil {
		return Analysis{}, err
	}
	privateModeChanges := s.sideChannel.privateModeChangeLog()
	screen := s.backend.snapshot()
	return Analysis{
		Dimensions:         screen.Dimensions,
		Operations:         mergePrivateModeOperations(s.backend.operations(), privateModeChanges),
		PrivateModeChanges: privateModeChanges,
		PhaseEvents:        s.sideChannel.phaseEventLog(),
		Screen:             screen,
	}, nil
}
