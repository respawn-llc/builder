package analyzer

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

type Dimensions struct {
	Rows int
	Cols int
}

func NewDimensions(rows, cols int) (Dimensions, error) {
	if rows <= 0 || cols <= 0 {
		return Dimensions{}, fmt.Errorf("terminal dimensions must be positive: rows=%d cols=%d", rows, cols)
	}
	return Dimensions{Rows: rows, Cols: cols}, nil
}

func MustDimensions(rows, cols int) Dimensions {
	dimensions, err := NewDimensions(rows, cols)
	if err != nil {
		panic(err)
	}
	return dimensions
}

type Region struct {
	Top    int
	Bottom int
	Left   int
	Right  int
}

func (r Region) ValidateWithin(dimensions Dimensions) error {
	if r.Top < 0 || r.Left < 0 || r.Bottom < r.Top || r.Right < r.Left {
		return fmt.Errorf("invalid terminal region: top=%d bottom=%d left=%d right=%d", r.Top, r.Bottom, r.Left, r.Right)
	}
	if r.Bottom > dimensions.Rows || r.Right > dimensions.Cols {
		return fmt.Errorf("terminal region outside dimensions: region=%+v dimensions=%+v", r, dimensions)
	}
	return nil
}

func (r Region) Empty() bool {
	return r.Top == r.Bottom || r.Left == r.Right
}

type ByteRange struct {
	Start int64
	End   int64
}

type Chunk struct {
	Index   int
	At      time.Duration
	Payload []byte
}

func NewChunk(index int, at time.Duration, payload []byte) Chunk {
	return Chunk{
		Index:   index,
		At:      at,
		Payload: slices.Clone(payload),
	}
}

type Capture struct {
	Dimensions   Dimensions
	Chunks       []Chunk
	Resizes      []ResizeEvent
	Raw          []byte
	ProcessExit  *ProcessExit
	ReadLoopDone bool
}

type ProcessExit struct {
	Code     int
	Signaled bool
}

type ResizeEvent struct {
	Placement  ResizePlacement
	At         time.Duration
	Dimensions Dimensions
}

type ResizePlacementKind int

const (
	ResizeBeforeFirstChunk ResizePlacementKind = iota + 1
	ResizeAfterChunk
)

type ResizePlacement struct {
	Kind       ResizePlacementKind
	ChunkIndex int
}

func BeforeFirstChunk() ResizePlacement {
	return ResizePlacement{Kind: ResizeBeforeFirstChunk}
}

func AfterChunk(index int) ResizePlacement {
	return ResizePlacement{Kind: ResizeAfterChunk, ChunkIndex: index}
}

func NewCapture(dimensions Dimensions, chunks []Chunk) (Capture, error) {
	return NewCaptureWithEvents(dimensions, chunks, nil)
}

func NewCaptureWithEvents(dimensions Dimensions, chunks []Chunk, resizes []ResizeEvent) (Capture, error) {
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return Capture{}, err
	}
	copied := make([]Chunk, len(chunks))
	rawLen := 0
	for i, chunk := range chunks {
		if chunk.Index != i {
			return Capture{}, fmt.Errorf("chunk index mismatch at position %d: got %d", i, chunk.Index)
		}
		if i > 0 && chunk.At < chunks[i-1].At {
			return Capture{}, fmt.Errorf("chunk timestamps must be monotonic: chunk=%d at=%s previous=%s", i, chunk.At, chunks[i-1].At)
		}
		copied[i] = NewChunk(chunk.Index, chunk.At, chunk.Payload)
		rawLen += len(chunk.Payload)
	}
	raw := make([]byte, 0, rawLen)
	for _, chunk := range copied {
		raw = append(raw, chunk.Payload...)
	}
	copiedResizes := make([]ResizeEvent, len(resizes))
	var previousPlacement *ResizePlacement
	for i, resize := range resizes {
		if _, err := NewDimensions(resize.Dimensions.Rows, resize.Dimensions.Cols); err != nil {
			return Capture{}, fmt.Errorf("resize event %d: %w", i, err)
		}
		if err := validateResizePlacement(resize.Placement, len(chunks)); err != nil {
			return Capture{}, fmt.Errorf("resize event %d: %w", i, err)
		}
		if previousPlacement != nil && compareResizePlacement(*previousPlacement, resize.Placement) > 0 {
			return Capture{}, fmt.Errorf("resize event placement order must be monotonic: resize=%d placement=%+v previous=%+v", i, resize.Placement, *previousPlacement)
		}
		if i > 0 && resize.At < resizes[i-1].At {
			return Capture{}, fmt.Errorf("resize event timestamps must be monotonic: resize=%d at=%s previous=%s", i, resize.At, resizes[i-1].At)
		}
		copiedResizes[i] = resize
		placement := resize.Placement
		previousPlacement = &placement
	}
	return Capture{
		Dimensions: dimensions,
		Chunks:     copied,
		Resizes:    copiedResizes,
		Raw:        raw,
	}, nil
}

func validateResizePlacement(placement ResizePlacement, chunkCount int) error {
	switch placement.Kind {
	case ResizeBeforeFirstChunk:
		return nil
	case ResizeAfterChunk:
		if placement.ChunkIndex < 0 || placement.ChunkIndex >= chunkCount {
			return fmt.Errorf("resize placement references invalid chunk index %d", placement.ChunkIndex)
		}
		return nil
	default:
		return fmt.Errorf("unknown resize placement kind %d", placement.Kind)
	}
}

func compareResizePlacement(left ResizePlacement, right ResizePlacement) int {
	if left.Kind == ResizeBeforeFirstChunk && right.Kind == ResizeAfterChunk {
		return -1
	}
	if left.Kind == ResizeAfterChunk && right.Kind == ResizeBeforeFirstChunk {
		return 1
	}
	if left.Kind == ResizeAfterChunk && right.Kind == ResizeAfterChunk {
		if left.ChunkIndex < right.ChunkIndex {
			return -1
		}
		if left.ChunkIndex > right.ChunkIndex {
			return 1
		}
	}
	return 0
}

type OperationKind int

const (
	OperationWrite OperationKind = iota + 1
	OperationErase
	OperationCursorMove
	OperationScrollRegionChange
	OperationResize
	OperationModeChange
)

type Position struct {
	Row int
	Col int
}

type Operation struct {
	Sequence    int
	Kind        OperationKind
	ChunkIndex  int
	ByteRange   ByteRange
	Before      Position
	After       Position
	Region      Region
	Write       *WritePayload
	PrivateMode *PrivateModeChange
	CapturedAt  time.Duration
}

type WritePayload struct {
	Text       string
	Faint      bool
	Foreground string
}

func NewWritePayload(text string) (WritePayload, error) {
	if text == "" {
		return WritePayload{}, errors.New("write payload text must not be empty")
	}
	return WritePayload{Text: text}, nil
}

func MustWritePayload(text string) WritePayload {
	payload, err := NewWritePayload(text)
	if err != nil {
		panic(err)
	}
	return payload
}

type Analysis struct {
	Dimensions         Dimensions
	Operations         []Operation
	PrivateModeChanges []PrivateModeChange
	PhaseEvents        []PhaseEvent
	Screen             ScreenSnapshot
}

type PrivateModeChange struct {
	Mode       int
	Enabled    bool
	ChunkIndex int
	ByteRange  ByteRange
	CapturedAt time.Duration
}

type PhaseKind int

const (
	PhaseScenarioStart PhaseKind = iota + 1
	PhaseWindowStart
	PhaseWindowEnd
	PhaseReadyForQuit
	PhaseScenarioComplete
)

type WindowID struct {
	value uuid.UUID
}

func NewWindowID(raw string) (WindowID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return WindowID{}, fmt.Errorf("parse window_id as UUID: %w", err)
	}
	if id == uuid.Nil {
		return WindowID{}, errors.New("window_id must not be nil UUID")
	}
	if id.Version() != 7 {
		return WindowID{}, fmt.Errorf("window_id must be UUIDv7: got version %d", id.Version())
	}
	return WindowID{value: id}, nil
}

func (id WindowID) String() string {
	return id.value.String()
}

type PhaseEvent struct {
	Sequence   int
	Phase      PhaseKind
	WindowID   *WindowID
	ChunkIndex int
	ByteRange  ByteRange
	CapturedAt time.Duration
}

type PhaseMarker struct {
	Sequence int
	Phase    PhaseKind
	WindowID *WindowID
}

type OperationWindow struct {
	Start int
	End   int
}

type AppendOperation struct {
	Operation Operation
}
