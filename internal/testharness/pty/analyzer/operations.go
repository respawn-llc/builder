package analyzer

func (b *tracingBackend) recordPut(position Position, cell Cell) {
	payload := MustWritePayload(cell.Content)
	payload.Faint = cell.Faint
	payload.Bold = cell.Bold
	payload.Italic = cell.Italic
	payload.Underline = cell.Underline
	payload.Foreground = cell.Foreground
	payload.Background = cell.Background
	current := Operation{
		Sequence:   len(b.ops),
		Kind:       OperationWrite,
		ChunkIndex: b.chunk.Index,
		ByteRange: ByteRange{
			Start: b.byteOffset,
			End:   b.byteEnd,
		},
		Before:     b.cursor,
		After:      position,
		Region:     Region{Top: position.Row, Bottom: position.Row + 1, Left: position.Col, Right: position.Col + 1},
		Write:      &payload,
		CapturedAt: b.chunk.At,
	}
	if len(b.ops) > 0 && canMergeWrite(b.ops[len(b.ops)-1], current) {
		previous := &b.ops[len(b.ops)-1]
		previous.Region.Right = current.Region.Right
		mergedPayload := MustWritePayload(previous.Write.Text + current.Write.Text)
		mergedPayload.Faint = previous.Write.Faint
		mergedPayload.Bold = previous.Write.Bold
		mergedPayload.Italic = previous.Write.Italic
		mergedPayload.Underline = previous.Write.Underline
		mergedPayload.Foreground = previous.Write.Foreground
		mergedPayload.Background = previous.Write.Background
		previous.Write = &mergedPayload
		previous.After = current.After
		previous.ByteRange.End = current.ByteRange.End
		return
	}
	b.ops = append(b.ops, current)
}

func canMergeWrite(previous Operation, current Operation) bool {
	return previous.Kind == OperationWrite &&
		previous.Write != nil &&
		current.Write != nil &&
		previous.Write.Faint == current.Write.Faint &&
		previous.Write.Bold == current.Write.Bold &&
		previous.Write.Italic == current.Write.Italic &&
		previous.Write.Underline == current.Write.Underline &&
		previous.Write.Foreground == current.Write.Foreground &&
		previous.Write.Background == current.Write.Background &&
		previous.ChunkIndex == current.ChunkIndex &&
		previous.Region.Top == current.Region.Top &&
		previous.Region.Bottom == current.Region.Bottom &&
		previous.Region.Right == current.Region.Left &&
		previous.ByteRange.End == current.ByteRange.Start
}
