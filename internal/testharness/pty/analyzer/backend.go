package analyzer

import (
	"time"

	"github.com/gdamore/tcell/v3/vt"
)

type tracingBackend struct {
	dimensions Dimensions
	cells      [][]Cell
	cursor     Position
	modes      map[vt.PrivateMode]vt.ModeStatus
	chunk      Chunk
	byteOffset int64
	byteEnd    int64
	ops        []Operation
	writeText  *writeTextArena
	err        error
}

func newTracingBackend(dimensions Dimensions) *tracingBackend {
	snapshot := NewScreenSnapshot(dimensions)
	return &tracingBackend{
		dimensions: dimensions,
		cells:      snapshot.Cells,
		modes:      map[vt.PrivateMode]vt.ModeStatus{},
		writeText:  newDefaultWriteTextArena(),
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

func (b *tracingBackend) operations() []Operation {
	return append([]Operation(nil), b.ops...)
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
	newCells := NewScreenSnapshot(dimensions).Cells
	copyRows := min(len(old.Cells), dimensions.Rows)
	for row := 0; row < copyRows; row++ {
		copy(newCells[row], old.Cells[row][:min(len(old.Cells[row]), dimensions.Cols)])
	}
	b.dimensions = dimensions
	b.cells = newCells
	b.cursor = Position{
		Row: clamp(b.cursor.Row, 0, dimensions.Rows-1),
		Col: clamp(b.cursor.Col, 0, dimensions.Cols-1),
	}
	b.ops = append(b.ops, Operation{
		Sequence:   len(b.ops),
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
	b.modes[mode] = status
	return nil
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
	b.cells[position.Row][position.Col] = Cell{Content: cell.C}
	if cell.C == "" {
		return
	}
	b.recordPut(position, cell.C)
}

func (b *tracingBackend) GetPosition() vt.Coord {
	return vt.Coord{X: vt.Col(b.cursor.Col), Y: vt.Row(b.cursor.Row)}
}

func (b *tracingBackend) SetPosition(coord vt.Coord) {
	b.cursor = Position{Row: clamp(int(coord.Y), 0, b.dimensions.Rows-1), Col: clamp(int(coord.X), 0, b.dimensions.Cols-1)}
}

func (b *tracingBackend) Reset() {
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
