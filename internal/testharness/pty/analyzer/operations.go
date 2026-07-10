package analyzer

func (b *tracingBackend) recordPut(position Position, text string) {
	if b.err != nil {
		return
	}
	if b.pendingWrite != nil && canExtendWrite(*b.pendingWrite, position, text, b) {
		payload, err := b.writePayload(text)
		if err != nil {
			b.err = err
			return
		}
		b.pendingWrite.span.End = payload.Span.End
		b.pendingWrite.after = position
		b.pendingWrite.region.Right = position.Col + 1
		b.pendingWrite.byteRange.End = b.byteEnd
		return
	}
	if err := b.flushPendingWrite(); err != nil {
		b.err = err
		return
	}
	payload, err := b.writePayload(text)
	if err != nil {
		b.err = err
		return
	}
	b.pendingWrite = &pendingWrite{
		chunk:     b.chunk,
		byteRange: ByteRange{Start: b.byteOffset, End: b.byteEnd},
		before:    b.cursor,
		after:     position,
		region:    Region{Top: position.Row, Bottom: position.Row + 1, Left: position.Col, Right: position.Col + 1},
		span:      payload.Span,
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

// flushPendingWrite stages one semantic row segment in the current logical
// write transaction. The transaction is finalized only at a control boundary.
func (b *tracingBackend) flushPendingWrite() error {
	if b.pendingWrite == nil {
		return nil
	}
	pending := b.pendingWrite
	b.pendingWrite = nil
	payload := WritePayload{Span: pending.span, arena: b.writeText}
	if b.writeBatch == nil {
		b.writeBatch = &writeBatch{}
	}
	if len(b.writeBatch.segments)+len(b.writeBatch.controls) >= maxWriteBatchSegments {
		return writeBatchLimitExceeded(b)
	}
	b.writeBatch.segments = append(b.writeBatch.segments, WriteSegment{ChunkIndex: pending.chunk.Index, ByteRange: pending.byteRange, Before: pending.before, After: pending.after, Region: pending.region, Write: payload, CapturedAt: pending.chunk.At})
	return nil
}

func (b *tracingBackend) writePayload(text string) (WritePayload, error) {
	span, err := b.writeText.append(text)
	if err != nil {
		return WritePayload{}, err
	}
	return WritePayload{Span: span, arena: b.writeText}, nil
}

func (b *tracingBackend) writeBatchSegments() []WriteSegment {
	if b.writeBatch == nil {
		return nil
	}
	return b.writeBatch.segments
}

func (b *tracingBackend) writeBatchControls() []Operation {
	if b.writeBatch == nil {
		return nil
	}
	return b.writeBatch.controls
}

func (b *tracingBackend) operationForWriteBatch(segments []WriteSegment, controls []Operation) Operation {
	batch := Operation{
		Sequence:      len(b.ops),
		Kind:          OperationCursorMove,
		WriteSegments: segments,
		Controls:      controls,
	}
	if len(segments) > 0 {
		batch.Kind = OperationWrite
		batch.Write = &segments[0].Write
	}
	records := OperationRecords(batch)
	first := records[0]
	last := records[len(records)-1]
	batch.ChunkIndex = first.ChunkIndex
	batch.ByteRange = ByteRange{Start: first.ByteRange.Start, End: last.ByteRange.End}
	batch.Before = first.Before
	batch.After = last.After
	batch.Region = first.Region
	batch.CapturedAt = first.CapturedAt
	for _, record := range records[1:] {
		batch.Region = unionRegion(batch.Region, record.Region)
	}
	return batch
}

func (b *tracingBackend) flushWriteBatch() error {
	if b.writeBatch == nil || (len(b.writeBatch.segments) == 0 && len(b.writeBatch.controls) == 0) {
		return nil
	}
	operation := b.operationForWriteBatch(b.writeBatch.segments, b.writeBatch.controls)
	b.writeBatch = nil
	if !b.appendFinalOperation(operation) {
		return b.err
	}
	return nil
}

func writeBatchLimitExceeded(backend *tracingBackend) error {
	return &EvidenceLimitExceeded{
		Source:   EvidenceSourceOperations,
		Detail:   "write_batch_records",
		Limit:    maxWriteBatchSegments,
		Observed: len(backend.writeBatch.segments) + len(backend.writeBatch.controls) + 1,
		Prefix:   append([]byte(nil), backend.operationBudget.prefix...),
		Tail:     append([]byte(nil), backend.operationBudget.tail...),
	}
}

func unionRegion(left, right Region) Region {
	return Region{
		Top:    min(left.Top, right.Top),
		Bottom: max(left.Bottom, right.Bottom),
		Left:   min(left.Left, right.Left),
		Right:  max(left.Right, right.Right),
	}
}
