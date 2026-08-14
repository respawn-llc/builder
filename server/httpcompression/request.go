package httpcompression

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"

	"github.com/klauspost/compress/zstd"
	"github.com/openai/openai-go/v3/option"
)

const MinimumRequestBodySize = 1024

func Middleware(coding RequestContentCoding) option.Middleware {
	return func(request *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		if request == nil {
			return next(request)
		}
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		if coding == ContentCodingIdentity {
			return next(request)
		}
		if request.Body == nil ||
			request.ContentLength < MinimumRequestBodySize ||
			request.GetBody == nil ||
			request.Header.Get("Content-Encoding") != "" {
			return next(request)
		}

		replay, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		compressed, err := encode(coding, replay)
		closeErr := replay.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}

		if err := request.Context().Err(); err != nil {
			return nil, err
		}

		prepared := request.Clone(request.Context())
		if err := request.Body.Close(); err != nil {
			return nil, err
		}
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		prepared.Body = io.NopCloser(bytes.NewReader(compressed))
		prepared.ContentLength = int64(len(compressed))
		prepared.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(compressed)), nil
		}
		prepared.Header.Set("Content-Encoding", contentCodingHeader(coding))
		return next(prepared)
	}
}

func encode(coding RequestContentCoding, plain io.Reader) ([]byte, error) {
	var compressed bytes.Buffer
	var writer io.WriteCloser
	switch coding {
	case ContentCodingGzip:
		writer = gzip.NewWriter(&compressed)
	case ContentCodingZstd:
		var err error
		writer, err = zstd.NewWriter(&compressed, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)))
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported request content coding %q", coding)
	}
	if _, err := io.Copy(writer, plain); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}
