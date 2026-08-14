// Package httpclient contains generic test-only HTTP client adapters shared by
// external-service and model transport tests.
package httpclient

import (
	"net/http"
	"net/url"
	"strings"
)

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func NewURLRewriteTransport(target *url.URL, transport http.RoundTripper, stripPathPrefix string) http.RoundTripper {
	return RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		cloned := request.Clone(request.Context())
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		cloned.URL.Path = target.Path + strings.TrimPrefix(request.URL.Path, stripPathPrefix)
		cloned.Host = target.Host
		return transport.RoundTrip(cloned)
	})
}
