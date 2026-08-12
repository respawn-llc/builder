package httpcompression

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/openai/openai-go/v3/option"
)

func TestClientNegotiatesAndDecodesZstdAndGzipResponses(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		encode   func([]byte) []byte
	}{
		{
			name:     "zstd",
			encoding: "zstd",
			encode: func(payload []byte) []byte {
				encoder, err := zstd.NewWriter(nil)
				if err != nil {
					t.Fatalf("create zstd encoder: %v", err)
				}
				return encoder.EncodeAll(payload, nil)
			},
		},
		{
			name:     "gzip",
			encoding: "gzip",
			encode: func(payload []byte) []byte {
				var compressed strings.Builder
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(payload); err != nil {
					t.Fatalf("write gzip response: %v", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatalf("close gzip response: %v", err)
				}
				return []byte(compressed.String())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte("decoded response")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Accept-Encoding"); got != "zstd,gzip" {
					t.Fatalf("Accept-Encoding = %q, want zstd,gzip", got)
				}
				w.Header().Set("Content-Encoding", test.encoding)
				_, _ = w.Write(test.encode(payload))
			}))
			defer server.Close()

			response, err := NewClient(nil).Get(server.URL)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer response.Body.Close()

			got, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if string(got) != string(payload) {
				t.Fatalf("body = %q, want %q", got, payload)
			}
		})
	}
}

func TestClientReturnsMalformedCompressedResponseReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "zstd")
		_, _ = w.Write([]byte("not compressed"))
	}))
	defer server.Close()

	response, err := NewClient(nil).Get(server.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()

	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("expected malformed compressed response read error")
	}
}

func TestClientPreservesCallerAcceptEncodingAndDecodesMatchingResponse(t *testing.T) {
	payload := []byte("caller-selected encoding")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Fatalf("Accept-Encoding = %q, want gzip", got)
		}
		var compressed strings.Builder
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(payload); err != nil {
			t.Fatalf("write gzip response: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close gzip response: %v", err)
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte(compressed.String()))
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Accept-Encoding", "gzip")

	response, err := NewClient(nil).Do(request)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()

	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
}

func TestRequestMiddlewareGzipEncoderProducesGzipBody(t *testing.T) {
	payload := bytes.Repeat([]byte("payload"), 200)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.ContentLength = int64(len(payload))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}

	var gotEncoding string
	var gotBody []byte
	next := option.MiddlewareNext(func(request *http.Request) (*http.Response, error) {
		gotEncoding = request.Header.Get("Content-Encoding")
		gotBody, err = io.ReadAll(request.Body)
		return nil, err
	})
	if _, err := Middleware(ContentCodingGzip)(request, next); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if gotEncoding != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", gotEncoding)
	}
	reader, err := gzip.NewReader(bytes.NewReader(gotBody))
	if err != nil {
		t.Fatalf("create gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close gzip reader: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded body does not match original payload")
	}
}

func TestRequestMiddlewareCompressesOnlyReplayableBodiesAtOrAboveThreshold(t *testing.T) {
	tests := []struct {
		name           string
		length         int64
		getBody        func() (io.ReadCloser, error)
		wantEncoding   string
		wantCompressed bool
	}{
		{
			name:           "zero length",
			length:         0,
			getBody:        replayBody([]byte{}),
			wantCompressed: false,
		},
		{
			name:           "below threshold",
			length:         MinimumRequestBodySize - 1,
			getBody:        replayBody(bytes.Repeat([]byte("x"), MinimumRequestBodySize-1)),
			wantCompressed: false,
		},
		{
			name:           "at threshold",
			length:         MinimumRequestBodySize,
			getBody:        replayBody(bytes.Repeat([]byte("x"), MinimumRequestBodySize)),
			wantEncoding:   "zstd",
			wantCompressed: true,
		},
		{
			name:           "unknown length",
			length:         -1,
			getBody:        replayBody(bytes.Repeat([]byte("x"), MinimumRequestBodySize)),
			wantCompressed: false,
		},
		{
			name:           "not replayable",
			length:         MinimumRequestBodySize,
			wantCompressed: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(strings.Repeat("x", max(int(test.length), 0))))
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			request.ContentLength = test.length
			request.GetBody = test.getBody

			var gotEncoding string
			next := option.MiddlewareNext(func(request *http.Request) (*http.Response, error) {
				gotEncoding = request.Header.Get("Content-Encoding")
				return nil, nil
			})
			if _, err := Middleware(ContentCodingZstd)(request, next); err != nil {
				t.Fatalf("middleware: %v", err)
			}
			if gotEncoding != test.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", gotEncoding, test.wantEncoding)
			}
			if (gotEncoding != "") != test.wantCompressed {
				t.Fatalf("compressed = %v, want %v", gotEncoding != "", test.wantCompressed)
			}
		})
	}
}

func TestRequestMiddlewarePreservesExplicitContentEncoding(t *testing.T) {
	payload := bytes.Repeat([]byte("payload"), 200)
	request, err := http.NewRequest(http.MethodPost, "https://example.test", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.ContentLength = int64(len(payload))
	request.GetBody = replayBody(payload)
	request.Header.Set("Content-Encoding", "custom")

	var gotBody []byte
	var gotEncoding string
	next := option.MiddlewareNext(func(request *http.Request) (*http.Response, error) {
		gotBody, err = io.ReadAll(request.Body)
		gotEncoding = request.Header.Get("Content-Encoding")
		return nil, err
	})
	if _, err := Middleware(ContentCodingZstd)(request, next); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatal("explicitly encoded body was changed")
	}
	if gotEncoding != "custom" {
		t.Fatalf("Content-Encoding = %q, want custom", gotEncoding)
	}
}

func TestRequestMiddlewarePreservesCancellationAndCompressedReplay(t *testing.T) {
	payload := bytes.Repeat([]byte("payload"), 200)
	request, err := http.NewRequest(http.MethodPost, "https://example.test", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.ContentLength = int64(len(payload))
	request.GetBody = replayBody(payload)

	var prepared *http.Request
	next := option.MiddlewareNext(func(request *http.Request) (*http.Response, error) {
		prepared = request
		return nil, nil
	})
	if _, err := Middleware(ContentCodingZstd)(request, next); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	replay, err := prepared.GetBody()
	if err != nil {
		t.Fatalf("get compressed replay: %v", err)
	}
	replayed, err := io.ReadAll(replay)
	if err != nil {
		t.Fatalf("read compressed replay: %v", err)
	}
	_ = replay.Close()
	decoder, err := zstd.NewReader(bytes.NewReader(replayed))
	if err != nil {
		t.Fatalf("create replay decoder: %v", err)
	}
	decoded, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	decoder.Close()
	if !bytes.Equal(decoded, payload) {
		t.Fatal("compressed replay does not preserve the original payload")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	canceledRequest, err := http.NewRequestWithContext(canceled, http.MethodPost, "https://example.test", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create canceled request: %v", err)
	}
	canceledRequest.ContentLength = int64(len(payload))
	canceledRequest.GetBody = replayBody(payload)
	called := false
	_, err = Middleware(ContentCodingZstd)(canceledRequest, func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})
	if err != context.Canceled {
		t.Fatalf("canceled middleware error = %v, want context canceled", err)
	}
	if called {
		t.Fatal("canceled request reached the next handler")
	}
}

func replayBody(payload []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
}
