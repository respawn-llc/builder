package boundedjson

import "errors"

var (
	ErrMalformed = errors.New("malformed JSON")
	ErrComplex   = errors.New("JSON is too deeply nested")
	ErrClosed    = errors.New("JSON scanner is closed")
)

const (
	MaxKnownFields         = 64
	MaxKnownFieldNameBytes = 256
	MaxNesting             = 10_000
)

type Range struct {
	Start int64
	End   int64
}

func (r Range) Size() int64 {
	return r.End - r.Start
}

type KnownFields []string

type ScannedObject struct {
	values  []Range
	present []bool
}

func (o ScannedObject) Value(slot int) (Range, bool) {
	if slot < 0 || slot >= len(o.values) {
		return Range{}, false
	}
	return o.values[slot], o.present[slot]
}
