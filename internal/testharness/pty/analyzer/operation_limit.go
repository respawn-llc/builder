package analyzer

const maxAnalyzerOperations = 16_384

// A write transaction records screen-local spans. Its independent cap is the
// maximum validated screen cardinality; top-level operations remain capped at
// 16,384 and aggregate write text at 1 MiB.
const maxWriteBatchSegments = maxTerminalCells

// operationBudget is shared by terminal writes, controls, private-mode
// changes, and phase events. It retains only bounded raw-byte diagnostics.
type operationBudget struct {
	count     int
	detail    string
	prefix    []byte
	tail      []byte
	tailStart int
}

func newOperationBudget() *operationBudget {
	return &operationBudget{
		prefix: make([]byte, 0, evidenceExcerptSize),
		tail:   make([]byte, 0, evidenceExcerptSize),
	}
}

func (b *operationBudget) observeByte(value byte) {
	if b == nil {
		return
	}
	if len(b.prefix) < evidenceExcerptSize {
		b.prefix = append(b.prefix, value)
	}
	if len(b.tail) < evidenceExcerptSize {
		b.tail = append(b.tail, value)
		return
	}
	b.tail[b.tailStart] = value
	b.tailStart = (b.tailStart + 1) % len(b.tail)
}

func (b *operationBudget) reserve() error {
	if b == nil {
		return nil
	}
	if b.count == maxAnalyzerOperations {
		return &EvidenceLimitExceeded{
			Source:   EvidenceSourceOperations,
			Detail:   b.detail,
			Limit:    maxAnalyzerOperations,
			Observed: b.count + 1,
			Prefix:   b.prefixBytes(),
			Tail:     b.tailBytes(),
		}
	}
	b.count++
	return nil
}

func (b *operationBudget) prefixBytes() []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b.prefix...)
}

func (b *operationBudget) tailBytes() []byte {
	if b == nil || len(b.tail) == 0 {
		return nil
	}
	if len(b.tail) < evidenceExcerptSize || b.tailStart == 0 {
		return append([]byte(nil), b.tail...)
	}
	tail := make([]byte, len(b.tail))
	n := copy(tail, b.tail[b.tailStart:])
	copy(tail[n:], b.tail[:b.tailStart])
	return tail
}
