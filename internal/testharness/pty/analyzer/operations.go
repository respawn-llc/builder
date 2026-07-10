package analyzer

func (b *tracingBackend) recordPut(position Position, text string) {
	if b.err != nil {
		return
	}
	if b.pendingWrite != nil && canExtendWrite(*b.pendingWrite, position, text, b) {
		b.pendingWrite.text = append(b.pendingWrite.text, text...)
		b.pendingWrite.after = position
		b.pendingWrite.region.Right = position.Col + 1
		b.pendingWrite.byteRange.End = b.byteEnd
		return
	}
	if err := b.flushPendingWrite(); err != nil {
		b.err = err
		return
	}
	b.pendingWrite = &pendingWrite{
		chunk:     b.chunk,
		byteRange: ByteRange{Start: b.byteOffset, End: b.byteEnd},
		before:    b.cursor,
		after:     position,
		region:    Region{Top: position.Row, Bottom: position.Row + 1, Left: position.Col, Right: position.Col + 1},
		text:      append([]byte(nil), text...),
	}
}

func canExtendWrite(pending pendingWrite, position Position, text string, backend *tracingBackend) bool {
	return backend.chunk.Index == pending.chunk.Index &&
		backend.chunk.At == pending.chunk.At &&
		pending.region.Top == position.Row &&
		pending.region.Right == position.Col &&
		pending.byteRange.End == backend.byteOffset &&
		text != ""
}

// flushPendingWrite emits one semantic row segment. Repeated redraws of an
// unchanged segment do not add duplicate diagnostic operations or arena text:
// the final screen is authoritative and one representative operation remains.
func (b *tracingBackend) flushPendingWrite() error {
	if b.pendingWrite == nil {
		return nil
	}
	pending := b.pendingWrite
	b.pendingWrite = nil
	text := string(pending.text)
	if previous, exists := b.writeCache[pending.region]; exists && previous == text {
		return nil
	}
	span, err := b.writeText.append(text)
	if err != nil {
		return err
	}
	payload := WritePayload{Span: span, arena: b.writeText}
	if !b.appendFinalOperation(Operation{
		Kind:       OperationWrite,
		ChunkIndex: pending.chunk.Index,
		ByteRange:  pending.byteRange,
		Before:     pending.before,
		After:      pending.after,
		Region:     pending.region,
		Write:      &payload,
		CapturedAt: pending.chunk.At,
	}) {
		return b.err
	}
	if len(b.writeCache) < maxAnalyzerOperations {
		b.writeCache[pending.region] = text
	}
	return nil
}
