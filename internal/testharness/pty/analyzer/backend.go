package analyzer

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v3/color"
	"github.com/gdamore/tcell/v3/vt"
)

type tracingBackend struct {
	dimensions      Dimensions
	cells           [][]Cell
	cursor          Position
	normal          *ScreenSnapshot
	modes           map[vt.PrivateMode]vt.ModeStatus
	chunk           Chunk
	byteOffset      int64
	byteEnd         int64
	ops             []Operation
	writeText       *writeTextArena
	pendingWrite    *pendingWrite
	writeBatch      *writeBatch
	operationBudget *operationBudget
	err             error
}

type pendingWrite struct {
	chunk     Chunk
	byteRange ByteRange
	before    Position
	after     Position
	region    Region
	write     WritePayload
}

type writeBatch struct {
	segments []WriteSegment
	controls []Operation
}

func newTracingBackend(dimensions Dimensions) *tracingBackend {
	snapshot := NewScreenSnapshot(dimensions)
	return &tracingBackend{
		dimensions: dimensions,
		cells:      snapshot.Cells,
		modes: map[vt.PrivateMode]vt.ModeStatus{
			vt.PmAltScreen: vt.ModeOff,
		},
		writeText:       newDefaultWriteTextArena(),
		operationBudget: newOperationBudget(),
	}
}

func (b *tracingBackend) error() error {
	return b.err
}

func (b *tracingBackend) beginChunk(chunk Chunk, offset int64) {
	b.chunk = chunk
	b.byteOffset = offset
	b.byteEnd = offset
}

func (b *tracingBackend) beginByte(chunk Chunk, offset int64) {
	b.chunk = chunk
	b.byteOffset = offset
	b.byteEnd = offset + 1
}

func (b *tracingBackend) observeByte(value byte) {
	b.operationBudget.observeByte(value)
}

func (b *tracingBackend) appendOperation(operation Operation) bool {
	if b.err != nil {
		return false
	}
	if err := b.flushPendingWrite(); err != nil {
		b.err = err
		return false
	}
	switch operation.Kind {
	case OperationCursorMove, OperationErase, OperationScrollRegionChange:
		if b.writeBatch == nil {
			b.writeBatch = &writeBatch{}
		}
		if len(b.writeBatch.segments)+len(b.writeBatch.controls) >= maxWriteBatchSegments {
			b.err = writeBatchLimitExceeded(b)
			return false
		}
		b.writeBatch.controls = append(b.writeBatch.controls, operation)
		return true
	}
	if err := b.flushWriteBatch(); err != nil {
		b.err = err
		return false
	}
	return b.appendFinalOperation(operation)
}

func (b *tracingBackend) appendFinalOperation(operation Operation) bool {
	if b.err != nil {
		return false
	}
	b.operationBudget.detail = fmt.Sprintf("operation_kind=%d", operation.Kind)
	if err := b.operationBudget.reserve(); err != nil {
		b.err = err
		return false
	}
	operation.Sequence = len(b.ops)
	b.ops = append(b.ops, operation)
	return true
}

func (b *tracingBackend) operations() []Operation {
	if err := b.flushPendingWrite(); err != nil {
		b.err = err
	}
	if err := b.flushWriteBatch(); err != nil {
		b.err = err
	}
	return append([]Operation(nil), b.ops...)
}

func (b *tracingBackend) snapshotOperations() ([]Operation, error) {
	operations := append([]Operation(nil), b.ops...)
	segments := append([]WriteSegment(nil), b.writeBatchSegments()...)
	controls := append([]Operation(nil), b.writeBatchControls()...)
	if b.pendingWrite != nil {
		pending := b.pendingWrite
		segments = append(segments, WriteSegment{ChunkIndex: pending.chunk.Index, ByteRange: pending.byteRange, Before: pending.before, After: pending.after, Region: pending.region, Write: pending.write, CapturedAt: pending.chunk.At})
	}
	if len(segments) > 0 || len(controls) > 0 {
		operations = append(operations, b.operationForWriteBatch(segments, controls))
	}
	return operations, nil
}

func (b *tracingBackend) snapshot() ScreenSnapshot {
	cells := make([][]Cell, len(b.cells))
	for row := range b.cells {
		cells[row] = append([]Cell(nil), b.cells[row]...)
	}
	return ScreenSnapshot{
		Dimensions: b.dimensions,
		Cells:      cells,
		Cursor:     b.cursor,
	}
}

func (b *tracingBackend) resize(dimensions Dimensions, at time.Duration) {
	old := b.snapshot()
	resized := resizeScreenSnapshot(old, dimensions)
	if b.normal != nil {
		normal := resizeScreenSnapshot(*b.normal, dimensions)
		b.normal = &normal
	}
	b.dimensions = dimensions
	b.cells = resized.Cells
	b.cursor = resized.Cursor
	b.appendOperation(Operation{
		Kind:       OperationResize,
		ChunkIndex: b.chunk.Index,
		ByteRange:  ByteRange{Start: b.byteEnd, End: b.byteEnd},
		Before:     old.Cursor,
		After:      b.cursor,
		Region:     Region{Top: 0, Bottom: dimensions.Rows, Left: 0, Right: dimensions.Cols},
		CapturedAt: at,
	})
}

func (b *tracingBackend) GetPrivateMode(mode vt.PrivateMode) vt.ModeStatus {
	if status, ok := b.modes[mode]; ok {
		return status
	}
	return vt.ModeNA
}

func (b *tracingBackend) SetPrivateMode(mode vt.PrivateMode, status vt.ModeStatus) error {
	if mode == vt.PmAltScreen {
		switch status {
		case vt.ModeOn:
			if b.modes[mode] != vt.ModeOn {
				normal := b.snapshot()
				b.normal = &normal
				alternate := NewScreenSnapshot(b.dimensions)
				b.cells = alternate.Cells
				b.cursor = alternate.Cursor
			}
		case vt.ModeOff:
			if b.modes[mode] == vt.ModeOn && b.normal != nil {
				b.cells = b.normal.Cells
				b.cursor = b.normal.Cursor
				b.normal = nil
			}
		default:
			return fmt.Errorf("set alternate-screen mode to unsupported status %d", status)
		}
	}
	b.modes[mode] = status
	return nil
}

func resizeScreenSnapshot(snapshot ScreenSnapshot, dimensions Dimensions) ScreenSnapshot {
	resized := NewScreenSnapshot(dimensions)
	copyRows := min(len(snapshot.Cells), dimensions.Rows)
	for row := 0; row < copyRows; row++ {
		copy(resized.Cells[row], snapshot.Cells[row][:min(len(snapshot.Cells[row]), dimensions.Cols)])
	}
	resized.Cursor = Position{
		Row: clamp(snapshot.Cursor.Row, 0, dimensions.Rows-1),
		Col: clamp(snapshot.Cursor.Col, 0, dimensions.Cols-1),
	}
	return resized
}

func (b *tracingBackend) GetSize() vt.Coord {
	return vt.Coord{X: vt.Col(b.dimensions.Cols), Y: vt.Row(b.dimensions.Rows)}
}

func (b *tracingBackend) Colors() int {
	return 1 << 24
}

func (b *tracingBackend) Put(coord vt.Coord, cell vt.Cell) {
	position := Position{Row: int(coord.Y), Col: int(coord.X)}
	if position.Row < 0 || position.Row >= b.dimensions.Rows || position.Col < 0 || position.Col >= b.dimensions.Cols {
		return
	}
	style := cellStyle(cell.S)
	styledCell := Cell{
		Content:    cell.C,
		Faint:      style.Faint,
		Bold:       style.Bold,
		Italic:     style.Italic,
		Underline:  style.Underline,
		Foreground: style.Foreground,
		Background: style.Background,
	}
	b.cells[position.Row][position.Col] = styledCell
	if cell.C == "" {
		return
	}
	b.recordPut(position, styledCell)
}

type terminalCellStyle struct {
	Faint      bool
	Bold       bool
	Italic     bool
	Underline  bool
	Foreground string
	Background string
}

func cellStyle(style vt.Style) terminalCellStyle {
	if style == nil {
		return terminalCellStyle{}
	}
	attrs := style.Attr()
	return terminalCellStyle{
		Faint:      attrs&vt.Dim != 0,
		Bold:       attrs&vt.Bold != 0,
		Italic:     attrs&vt.Italic != 0,
		Underline:  attrs&vt.Underline != 0,
		Foreground: styleColor(style.Fg()),
		Background: styleColor(style.Bg()),
	}
}

func styleColor(value color.Color) string {
	if !value.Valid() || value == color.Default {
		return ""
	}
	r, g, b := value.TrueColor().RGB()
	return fmt.Sprintf("#%02x%02x%02x", uint8(r), uint8(g), uint8(b))
}

// Blit applies emulator-internal scrolling without synthesizing terminal
// writes. The terminal byte stream already records the user-visible write that
// caused the scroll; recording the emulator's fallback cell repaint would turn
// one printable byte into a full-screen redraw and exhaust bounded evidence.
func (b *tracingBackend) Blit(source, destination, size vt.Coord) {
	if size.X <= 0 || size.Y <= 0 {
		return
	}
	rowStart, rowEnd, rowStep := 0, int(size.Y), 1
	colStart, colEnd, colStep := 0, int(size.X), 1
	if destination.Y > source.Y {
		rowStart, rowEnd, rowStep = int(size.Y)-1, -1, -1
	} else if destination.Y == source.Y && destination.X > source.X {
		colStart, colEnd, colStep = int(size.X)-1, -1, -1
	}
	for row := rowStart; row != rowEnd; row += rowStep {
		for col := colStart; col != colEnd; col += colStep {
			from := Position{Row: int(source.Y) + row, Col: int(source.X) + col}
			to := Position{Row: int(destination.Y) + row, Col: int(destination.X) + col}
			if from.Row < 0 || from.Row >= b.dimensions.Rows || from.Col < 0 || from.Col >= b.dimensions.Cols ||
				to.Row < 0 || to.Row >= b.dimensions.Rows || to.Col < 0 || to.Col >= b.dimensions.Cols {
				continue
			}
			b.cells[to.Row][to.Col] = b.cells[from.Row][from.Col]
		}
	}
}

func (b *tracingBackend) GetPosition() vt.Coord {
	return vt.Coord{X: vt.Col(b.cursor.Col), Y: vt.Row(b.cursor.Row)}
}

func (b *tracingBackend) SetPosition(coord vt.Coord) {
	b.cursor = Position{Row: clamp(int(coord.Y), 0, b.dimensions.Rows-1), Col: clamp(int(coord.X), 0, b.dimensions.Cols-1)}
}

func (b *tracingBackend) Reset() {
	if err := b.flushPendingWrite(); err != nil {
		b.err = err
		return
	}
	if err := b.flushWriteBatch(); err != nil {
		b.err = err
		return
	}
	snapshot := NewScreenSnapshot(b.dimensions)
	b.cells = snapshot.Cells
	b.cursor = Position{}
}

func (b *tracingBackend) RaiseResize() {}

func (b *tracingBackend) Buffering(bool) {}

func (b *tracingBackend) SetCursor(vt.CursorStyle) {}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
