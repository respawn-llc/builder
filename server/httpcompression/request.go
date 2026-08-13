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
const responseContentCodings = "zstd,gzip"

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
		nextRequest := request
		if request.Header.Get("Accept-Encoding") == "" {
			nextRequest = request.Clone(request.Context())
			nextRequest.Header.Set("Accept-Encoding", responseContentCodings)
		}
		if nextRequest.Body == nil ||
			nextRequest.ContentLength < MinimumRequestBodySize ||
			nextRequest.GetBody == nil ||
			nextRequest.Header.Get("Content-Encoding") != "" {
			return next(nextRequest)
		}

		replay, err := nextRequest.GetBody()
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

		prepared := nextRequest.Clone(request.Context())
		if err := nextRequest.Body.Close(); err != nil {
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
	switch coding {
	case ContentCodingGzip:
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := io.Copy(writer, plain); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return compressed.Bytes(), nil
	case ContentCodingZstd:
		var compressed bytes.Buffer
		writer, err := zstd.NewWriter(&compressed, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)))
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(writer, plain); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return compressed.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported request content coding %q", coding)
	}
}
