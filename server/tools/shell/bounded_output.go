package shell

import "bytes"

type BoundedOutput struct {
	limit    int
	buf      bytes.Buffer
	total    int
	overflow bool
}

func NewBoundedOutput(limit int) *BoundedOutput {
	return &BoundedOutput{limit: limit}
}

func (b *BoundedOutput) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	if b.limit <= 0 {
		b.total += len(p)
		if len(p) > 0 {
			b.overflow = true
		}
		return len(p), nil
	}
	b.total += len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.overflow = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.overflow = true
	}
	if b.total > b.limit {
		b.overflow = true
	}
	return len(p), nil
}

func (b *BoundedOutput) Bytes() []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *BoundedOutput) String() string {
	if b == nil {
		return ""
	}
	return b.buf.String()
}

func (b *BoundedOutput) Overflow() bool {
	if b == nil {
		return false
	}
	return b.overflow
}
