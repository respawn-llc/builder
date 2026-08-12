package httpcompression

import (
	"net/http"

	"github.com/klauspost/compress/gzhttp"
)

// Transport wraps an HTTP transport with transparent zstd and gzip response
// negotiation and decoding.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return gzhttp.Transport(base, gzhttp.TransportAlwaysDecompress(true))
}

// NewClient wraps the supplied HTTP client with transparent zstd and gzip
// response negotiation and decoding.
func NewClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = Transport(transport)
	return &client
}
