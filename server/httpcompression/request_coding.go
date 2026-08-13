package httpcompression

type RequestContentCoding uint8

const (
	ContentCodingIdentity RequestContentCoding = iota
	ContentCodingGzip
	ContentCodingZstd
)

func contentCodingHeader(coding RequestContentCoding) string {
	switch coding {
	case ContentCodingGzip:
		return "gzip"
	case ContentCodingZstd:
		return "zstd"
	default:
		return ""
	}
}
