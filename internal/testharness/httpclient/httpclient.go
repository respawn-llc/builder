// Package httpclient contains generic test-only HTTP client adapters shared by
// external-service and model transport tests.
package httpclient

import "net/http"

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
