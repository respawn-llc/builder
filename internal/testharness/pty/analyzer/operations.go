package analyzer

func (b *tracingBackend) recordPut(position Position, text string) {
	span, err := b.writeText.append(text)
	if err != nil {
		b.err = err
		return
	}
	payload := WritePayload{Span: span, arena: b.writeText}
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
		previous.Write.Span.End = current.Write.Span.End
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
		previous.Write.arena == current.Write.arena &&
		previous.Write.Span.End == current.Write.Span.Start &&
		previous.ChunkIndex == current.ChunkIndex &&
		previous.Region.Top == current.Region.Top &&
		previous.Region.Bottom == current.Region.Bottom &&
		previous.Region.Right == current.Region.Left &&
		previous.ByteRange.End == current.ByteRange.Start
}
