package analyzer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

type sequenceSideChannel struct {
	parser             *ansi.Parser
	backend            *tracingBackend
	activeSequence     *activeSequence
	currentChunk       Chunk
	currentOffset      int64
	privateModeChanges []PrivateModeChange
	phaseEvents        []PhaseEvent
	err                error
}

type activeSequence struct {
	start int64
}

func newSequenceSideChannel(backend *tracingBackend) *sequenceSideChannel {
	side := &sequenceSideChannel{
		parser:  ansi.NewParser(),
		backend: backend,
	}
	side.parser.SetHandler(ansi.Handler{
		HandleCsi: side.handleCSI,
		HandleOsc: side.handleOSC,
	})
	return side
}

func (s *sequenceSideChannel) advance(b byte, chunk Chunk, offset int64) {
	if s.parser.State() == parser.GroundState && startsControlSequence(b) {
		s.activeSequence = &activeSequence{start: offset}
	}
	s.currentChunk = chunk
	s.currentOffset = offset
	s.parser.Advance(b)
	if s.parser.State() == parser.GroundState {
		s.activeSequence = nil
	}
}

func startsControlSequence(b byte) bool {
	return b == '\x1b' || b == 0x9b || b == 0x9d
}

func (s *sequenceSideChannel) privateModeChangeLog() []PrivateModeChange {
	return append([]PrivateModeChange(nil), s.privateModeChanges...)
}

func (s *sequenceSideChannel) phaseEventLog() []PhaseEvent {
	return append([]PhaseEvent(nil), s.phaseEvents...)
}

func (s *sequenceSideChannel) error() error {
	return s.err
}

func (s *sequenceSideChannel) handleCSI(cmd ansi.Cmd, params ansi.Params) {
	byteRange, ok := s.activeByteRange("CSI")
	if !ok {
		return
	}
	switch cmd.Final() {
	case 'H', 'f':
		row, _, _ := params.Param(0, 1)
		col, _, _ := params.Param(1, 1)
		s.recordCursorMove(Position{Row: row - 1, Col: col - 1}, byteRange)
	case 'J':
		mode, _, _ := params.Param(0, 0)
		s.recordErase(eraseDisplayRegion(s.backend.cursor, s.backend.dimensions, mode), byteRange)
	case 'K':
		mode, _, _ := params.Param(0, 0)
		s.recordErase(eraseLineRegion(s.backend.cursor, s.backend.dimensions, mode), byteRange)
	case 'X':
		count, _, _ := params.Param(0, 1)
		right := min(s.backend.cursor.Col+count, s.backend.dimensions.Cols)
		s.recordErase(Region{Top: s.backend.cursor.Row, Bottom: s.backend.cursor.Row + 1, Left: s.backend.cursor.Col, Right: right}, byteRange)
	case 'r':
		top, _, _ := params.Param(0, 1)
		bottom, _, _ := params.Param(1, s.backend.dimensions.Rows)
		s.recordScrollRegion(Region{Top: top - 1, Bottom: bottom, Left: 0, Right: s.backend.dimensions.Cols}, byteRange)
	case 's':
		left, _, _ := params.Param(0, 1)
		right, _, _ := params.Param(1, s.backend.dimensions.Cols)
		s.recordScrollRegion(Region{Top: 0, Bottom: s.backend.dimensions.Rows, Left: left - 1, Right: right}, byteRange)
	case 'h', 'l':
		if cmd.Prefix() == '?' {
			enabled := cmd.Final() == 'h'
			params.ForEach(0, func(_ int, mode int, _ bool) {
				s.privateModeChanges = append(s.privateModeChanges, PrivateModeChange{
					Mode:       mode,
					Enabled:    enabled,
					ChunkIndex: s.currentChunk.Index,
					ByteRange:  byteRange,
					CapturedAt: s.currentChunk.At,
				})
			})
		}
	}
}

func (s *sequenceSideChannel) handleOSC(cmd int, data []byte) {
	if cmd != 777 {
		return
	}
	byteRange, ok := s.activeByteRange("OSC")
	if !ok {
		return
	}
	payload, ok := phaseMarkerPayload(data)
	if !ok {
		return
	}
	event, err := parsePhaseMarker(payload, byteRange, s.currentChunk)
	if err != nil {
		s.err = err
		return
	}
	if len(s.phaseEvents) > 0 && event.Sequence <= s.phaseEvents[len(s.phaseEvents)-1].Sequence {
		s.err = fmt.Errorf("phase marker sequence must increase: previous=%d current=%d", s.phaseEvents[len(s.phaseEvents)-1].Sequence, event.Sequence)
		return
	}
	s.phaseEvents = append(s.phaseEvents, event)
}

func (s *sequenceSideChannel) activeByteRange(kind string) (ByteRange, bool) {
	if s.activeSequence == nil {
		s.err = fmt.Errorf("%s handler invoked without active control-sequence start at chunk=%d byte_offset=%d", kind, s.currentChunk.Index, s.currentOffset)
		return ByteRange{}, false
	}
	return ByteRange{Start: s.activeSequence.start, End: s.currentOffset + 1}, true
}

func (s *sequenceSideChannel) recordCursorMove(position Position, byteRange ByteRange) {
	position.Row = clamp(position.Row, 0, s.backend.dimensions.Rows-1)
	position.Col = clamp(position.Col, 0, s.backend.dimensions.Cols-1)
	s.backend.ops = append(s.backend.ops, Operation{
		Sequence:   len(s.backend.ops),
		Kind:       OperationCursorMove,
		ChunkIndex: s.currentChunk.Index,
		ByteRange:  byteRange,
		Before:     s.backend.cursor,
		After:      position,
		Region:     Region{Top: position.Row, Bottom: position.Row + 1, Left: position.Col, Right: position.Col + 1},
		CapturedAt: s.currentChunk.At,
	})
}

func (s *sequenceSideChannel) recordErase(region Region, byteRange ByteRange) {
	s.backend.ops = append(s.backend.ops, Operation{
		Sequence:   len(s.backend.ops),
		Kind:       OperationErase,
		ChunkIndex: s.currentChunk.Index,
		ByteRange:  byteRange,
		Before:     s.backend.cursor,
		After:      s.backend.cursor,
		Region:     region,
		CapturedAt: s.currentChunk.At,
	})
}

func (s *sequenceSideChannel) recordScrollRegion(region Region, byteRange ByteRange) {
	s.backend.ops = append(s.backend.ops, Operation{
		Sequence:   len(s.backend.ops),
		Kind:       OperationScrollRegionChange,
		ChunkIndex: s.currentChunk.Index,
		ByteRange:  byteRange,
		Before:     s.backend.cursor,
		After:      s.backend.cursor,
		Region:     region,
		CapturedAt: s.currentChunk.At,
	})
}

func eraseDisplayRegion(cursor Position, dimensions Dimensions, mode int) Region {
	switch mode {
	case 1:
		return Region{Top: 0, Bottom: cursor.Row + 1, Left: 0, Right: dimensions.Cols}
	case 2, 3:
		return Region{Top: 0, Bottom: dimensions.Rows, Left: 0, Right: dimensions.Cols}
	default:
		return Region{Top: cursor.Row, Bottom: dimensions.Rows, Left: 0, Right: dimensions.Cols}
	}
}

func eraseLineRegion(cursor Position, dimensions Dimensions, mode int) Region {
	switch mode {
	case 1:
		return Region{Top: cursor.Row, Bottom: cursor.Row + 1, Left: 0, Right: cursor.Col + 1}
	case 2:
		return Region{Top: cursor.Row, Bottom: cursor.Row + 1, Left: 0, Right: dimensions.Cols}
	default:
		return Region{Top: cursor.Row, Bottom: cursor.Row + 1, Left: cursor.Col, Right: dimensions.Cols}
	}
}

type phaseMarkerJSON struct {
	Version  int     `json:"version"`
	Seq      int     `json:"seq"`
	Phase    string  `json:"phase"`
	WindowID *string `json:"window_id"`
}

func phaseMarkerPayload(data []byte) ([]byte, bool) {
	prefix := []byte("777;kent-pty-phase;")
	if len(data) <= len(prefix) {
		return nil, false
	}
	for index, want := range prefix {
		if data[index] != want {
			return nil, false
		}
	}
	return data[len(prefix):], true
}

func parsePhaseMarker(payload []byte, byteRange ByteRange, chunk Chunk) (PhaseEvent, error) {
	decoded := make([]byte, base64.RawURLEncoding.DecodedLen(len(payload)))
	n, err := base64.RawURLEncoding.Decode(decoded, payload)
	if err != nil {
		return PhaseEvent{}, fmt.Errorf("decode phase marker payload: %w", err)
	}
	var marker phaseMarkerJSON
	if err := json.Unmarshal(decoded[:n], &marker); err != nil {
		return PhaseEvent{}, fmt.Errorf("decode phase marker JSON: %w", err)
	}
	if marker.Version != 1 {
		return PhaseEvent{}, fmt.Errorf("unsupported phase marker version %d", marker.Version)
	}
	if marker.Seq <= 0 {
		return PhaseEvent{}, fmt.Errorf("phase marker seq must be positive: %d", marker.Seq)
	}
	kind, err := phaseKindFromProtocol(marker.Phase)
	if err != nil {
		return PhaseEvent{}, err
	}
	var windowID *WindowID
	if marker.WindowID != nil {
		if *marker.WindowID == "" {
			return PhaseEvent{}, errors.New("window_id must not be empty")
		}
		parsed, err := NewWindowID(*marker.WindowID)
		if err != nil {
			return PhaseEvent{}, err
		}
		windowID = &parsed
	}
	if err := validateWindowEventID(kind, windowID); err != nil {
		return PhaseEvent{}, err
	}
	return PhaseEvent{
		Sequence:   marker.Seq,
		Phase:      kind,
		WindowID:   windowID,
		ChunkIndex: chunk.Index,
		ByteRange:  byteRange,
		CapturedAt: chunk.At,
	}, nil
}
