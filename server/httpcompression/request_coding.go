package httpcompression

import (
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

type RequestContentCoding uint8

const (
	ContentCodingIdentity RequestContentCoding = iota
	ContentCodingGzip
	ContentCodingZstd
)

type requestEncoder struct {
	header    string
	newWriter func(io.Writer) (io.WriteCloser, error)
}

func newRequestEncoder(coding RequestContentCoding) (requestEncoder, error) {
	switch coding {
	case ContentCodingGzip:
		return requestEncoder{
			header: "gzip",
			newWriter: func(destination io.Writer) (io.WriteCloser, error) {
				return gzip.NewWriter(destination), nil
			},
		}, nil
	case ContentCodingZstd:
		return requestEncoder{
			header: "zstd",
			newWriter: func(destination io.Writer) (io.WriteCloser, error) {
				return zstd.NewWriter(destination, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)))
			},
		}, nil
	default:
		return requestEncoder{}, fmt.Errorf("unsupported request content coding %q", coding)
	}
}
