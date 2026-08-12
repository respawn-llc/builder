package llm

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"core/server/httpcompression"
)

const (
	sharedHTTPTransportMaxIdleConns        = 128
	sharedHTTPTransportMaxIdleConnsPerHost = 32
)

var sharedHTTPTransport = newSharedHTTPTransport()
var sharedCompressedHTTPTransport = httpcompression.Transport(sharedHTTPTransport)

// NewHTTPClient returns an HTTP client that shares a tuned transport across
// runtimes so local/LAN model backends can reuse warm connections aggressively.
func NewHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Transport: sharedCompressedHTTPTransport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

// NewProviderHTTPClient returns the default model-provider client while
// keeping loopback model servers on the uncompressed local transport.
func NewProviderHTTPClient(baseURL string, timeout time.Duration) *http.Client {
	if isLoopbackHTTPURL(baseURL) {
		client := &http.Client{Transport: sharedHTTPTransport}
		if timeout > 0 {
			client.Timeout = timeout
		}
		return client
	}
	return NewHTTPClient(timeout)
}

func isLoopbackHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func newSharedHTTPTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	transport := &http.Transport{}
	if ok {
		transport = base.Clone()
	}
	transport.ForceAttemptHTTP2 = true
	if transport.MaxIdleConns < sharedHTTPTransportMaxIdleConns {
		transport.MaxIdleConns = sharedHTTPTransportMaxIdleConns
	}
	if transport.MaxIdleConnsPerHost < sharedHTTPTransportMaxIdleConnsPerHost {
		transport.MaxIdleConnsPerHost = sharedHTTPTransportMaxIdleConnsPerHost
	}
	return transport
}
