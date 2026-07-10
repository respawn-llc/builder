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

const (
	minTerminalRows  = 1
	maxTerminalRows  = 200
	minTerminalCols  = 1
	maxTerminalCols  = 500
	maxTerminalCells = 100_000
)

func NewDimensions(rows, cols int) (Dimensions, error) {
	if err := validateDimensions(rows, cols); err != nil {
		return Dimensions{}, err
	}
	return Dimensions{Rows: rows, Cols: cols}, nil
}

func validateDimensions(rows, cols int) error {
	if rows < minTerminalRows || cols < minTerminalCols {
		return fmt.Errorf("terminal dimensions must be positive: rows=%d cols=%d", rows, cols)
	}
	if rows > maxTerminalCells/cols {
		return fmt.Errorf("terminal dimensions exceed cell limit: rows=%d cols=%d cells>%d", rows, cols, maxTerminalCells)
	}
	if rows > maxTerminalRows {
		return fmt.Errorf("terminal rows exceed limit: rows=%d max=%d", rows, maxTerminalRows)
	}
	if cols > maxTerminalCols {
		return fmt.Errorf("terminal columns exceed limit: cols=%d max=%d", cols, maxTerminalCols)
	}
	return nil
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
	Offset     int64
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

const evidenceBlockSize = 16 * 1024

const (
	maxPTYEvidenceBytes = 1 * 1024 * 1024
	evidenceExcerptSize = 32 * 1024
	maxEvidenceBlocks   = 320
)

type EvidenceSource string

const (
	EvidenceSourcePTY           EvidenceSource = "pty"
	EvidenceSourceOperationText EvidenceSource = "operation_text"
	EvidenceSourceOperations    EvidenceSource = "operations"
)

type EvidenceLimitExceeded struct {
	Source   EvidenceSource
	Limit    int
	Observed int
	Prefix   []byte
	Tail     []byte
}

func (e *EvidenceLimitExceeded) Error() string {
	return fmt.Sprintf("%s evidence limit exceeded: observed=%d limit=%d", e.Source, e.Observed, e.Limit)
}

// CaptureAssembler coalesces transient PTY reads into deterministic evidence
// blocks. A resize flushes the current block so its observer offset is stable
// regardless of how the kernel fragmented reads.
type CaptureAssembler struct {
	dimensions Dimensions
	blocks     [][]byte
	current    []byte
	offset     int64
	resizes    []ResizeEvent
}

func NewCaptureAssembler(dimensions Dimensions) (*CaptureAssembler, error) {
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return nil, err
	}
	return &CaptureAssembler{
		dimensions: dimensions,
		current:    make([]byte, 0, evidenceBlockSize),
	}, nil
}

func (a *CaptureAssembler) Append(payload []byte) error {
	if a == nil {
		return errors.New("capture assembler is required")
	}
	if len(payload) > maxPTYEvidenceBytes-int(a.offset) {
		return a.overflow(payload)
	}
	for len(payload) > 0 {
		space := evidenceBlockSize - len(a.current)
		take := min(space, len(payload))
		a.current = append(a.current, payload[:take]...)
		a.offset += int64(take)
		payload = payload[take:]
		if len(a.current) == evidenceBlockSize {
			if err := a.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *CaptureAssembler) overflow(payload []byte) error {
	return &EvidenceLimitExceeded{
		Source:   EvidenceSourcePTY,
		Limit:    maxPTYEvidenceBytes,
		Observed: int(a.offset) + len(payload),
		Prefix:   a.prefixExcerpt(payload),
		Tail:     a.tailExcerpt(payload),
	}
}

func (a *CaptureAssembler) prefixExcerpt(extra []byte) []byte {
	size := min(int(a.offset)+len(extra), evidenceExcerptSize)
	prefix := make([]byte, size)
	fromEvidence := min(int(a.offset), size)
	a.copyEvidencePrefix(prefix[:fromEvidence])
	if fromEvidence < size {
		copy(prefix[fromEvidence:], extra[:size-fromEvidence])
	}
	return prefix
}

func (a *CaptureAssembler) tailExcerpt(extra []byte) []byte {
	size := min(int(a.offset)+len(extra), evidenceExcerptSize)
	tail := make([]byte, size)
	fromExtra := min(len(extra), size)
	if fromExtra > 0 {
		copy(tail[size-fromExtra:], extra[len(extra)-fromExtra:])
	}
	a.copyEvidenceTail(tail[:size-fromExtra])
	return tail
}

func (a *CaptureAssembler) evidenceBytes() []byte {
	size := len(a.current)
	for _, block := range a.blocks {
		size += len(block)
	}
	evidence := make([]byte, 0, size)
	for _, block := range a.blocks {
		evidence = append(evidence, block...)
	}
	return append(evidence, a.current...)
}

// copyEvidencePrefix copies only the requested bounded diagnostic prefix.
func (a *CaptureAssembler) copyEvidencePrefix(dst []byte) {
	cursor := 0
	copyFromStart := func(source []byte) {
		if cursor == len(dst) {
			return
		}
		take := min(len(dst)-cursor, len(source))
		copy(dst[cursor:cursor+take], source[:take])
		cursor += take
	}
	for _, block := range a.blocks {
		copyFromStart(block)
		if cursor == len(dst) {
			return
		}
	}
	copyFromStart(a.current)
}

// copyEvidenceTail copies only the requested bounded diagnostic tail.
func (a *CaptureAssembler) copyEvidenceTail(dst []byte) {
	cursor := len(dst)
	copyFromEnd := func(source []byte) {
		if cursor == 0 {
			return
		}
		take := min(cursor, len(source))
		cursor -= take
		copy(dst[cursor:cursor+take], source[len(source)-take:])
	}
	copyFromEnd(a.current)
	for index := len(a.blocks) - 1; index >= 0 && cursor > 0; index-- {
		copyFromEnd(a.blocks[index])
	}
}

func (a *CaptureAssembler) Resize(dimensions Dimensions) error {
	if a == nil {
		return errors.New("capture assembler is required")
	}
	if _, err := NewDimensions(dimensions.Rows, dimensions.Cols); err != nil {
		return err
	}
	if err := a.flush(); err != nil {
		return err
	}
	placement := BeforeFirstChunk()
	if len(a.blocks) > 0 {
		placement = AfterChunk(len(a.blocks) - 1)
	}
	a.resizes = append(a.resizes, ResizeEvent{
		Placement:  placement,
		Offset:     a.offset,
		Dimensions: dimensions,
	})
	return nil
}

func (a *CaptureAssembler) Capture() (Capture, error) {
	if a == nil {
		return Capture{}, errors.New("capture assembler is required")
	}
	if err := a.flush(); err != nil {
		return Capture{}, err
	}
	chunks := make([]Chunk, len(a.blocks))
	for index, block := range a.blocks {
		chunks[index] = NewChunk(index, 0, block)
	}
	return NewCaptureWithEvents(a.dimensions, chunks, a.resizes)
}

func (a *CaptureAssembler) flush() error {
	if len(a.current) == 0 {
		return nil
	}
	if len(a.blocks) == maxEvidenceBlocks {
		return &EvidenceLimitExceeded{
			Source:   EvidenceSourcePTY,
			Limit:    maxEvidenceBlocks,
			Observed: len(a.blocks) + 1,
			Prefix:   a.prefixExcerpt(nil),
			Tail:     a.tailExcerpt(nil),
		}
	}
	a.blocks = append(a.blocks, append([]byte(nil), a.current...))
	a.current = make([]byte, 0, evidenceBlockSize)
	return nil
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
	var previousOffset *int64
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
		if copiedResizes[i].Offset == 0 {
			copiedResizes[i].Offset = resizePlacementOffset(resize.Placement, copied)
		}
		if copiedResizes[i].Offset < 0 || copiedResizes[i].Offset > int64(rawLen) {
			return Capture{}, fmt.Errorf("resize event %d observer offset %d outside captured byte range [0,%d]", i, copiedResizes[i].Offset, rawLen)
		}
		if previousOffset != nil && copiedResizes[i].Offset < *previousOffset {
			return Capture{}, fmt.Errorf("resize event observer offsets must be monotonic: resize=%d offset=%d previous=%d", i, copiedResizes[i].Offset, *previousOffset)
		}
		placement := resize.Placement
		previousPlacement = &placement
		offset := copiedResizes[i].Offset
		previousOffset = &offset
	}
	return Capture{
		Dimensions: dimensions,
		Chunks:     copied,
		Resizes:    copiedResizes,
		Raw:        raw,
	}, nil
}

func resizePlacementOffset(placement ResizePlacement, chunks []Chunk) int64 {
	if placement.Kind == ResizeBeforeFirstChunk {
		return 0
	}
	offset := int64(0)
	for index := 0; index <= placement.ChunkIndex; index++ {
		offset += int64(len(chunks[index].Payload))
	}
	return offset
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
	Span  TextSpan
	arena *writeTextArena
}

type TextSpan struct {
	Start int
	End   int
}

func (span TextSpan) Validate() error {
	if span.Start < 0 || span.End <= span.Start {
		return fmt.Errorf("invalid write text span: start=%d end=%d", span.Start, span.End)
	}
	return nil
}

func (payload WritePayload) Text() string {
	if payload.arena == nil || payload.Span.Validate() != nil || payload.Span.End > len(payload.arena.bytes) {
		panic(fmt.Sprintf("invalid write payload span=%+v arena_bytes=%d", payload.Span, len(payload.arena.bytes)))
	}
	return string(payload.arena.bytes[payload.Span.Start:payload.Span.End])
}

func NewWritePayload(text string) (WritePayload, error) {
	if text == "" {
		return WritePayload{}, errors.New("write payload text must not be empty")
	}
	arena := newWriteTextArena(len(text))
	span, err := arena.append(text)
	if err != nil {
		return WritePayload{}, err
	}
	return WritePayload{Span: span, arena: arena}, nil
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
	if id.Version() != 4 {
		return WindowID{}, fmt.Errorf("window_id must be UUIDv4: got version %d", id.Version())
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
