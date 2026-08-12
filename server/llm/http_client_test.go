package llm

import (
	"net/http"
	"testing"
	"time"
)

type stubRoundTripper struct{}

func (stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}

func TestNewHTTPClientSharesTransport(t *testing.T) {
	first := NewHTTPClient(5 * time.Second)
	second := NewHTTPClient(10 * time.Second)

	if first.Transport == nil {
		t.Fatal("expected compressed transport to be set")
	}
	if first.Transport != second.Transport {
		t.Fatal("expected shared compressed transport instance")
	}
	transport := sharedHTTPTransport
	if transport.MaxIdleConns < sharedHTTPTransportMaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want >= %d", transport.MaxIdleConns, sharedHTTPTransportMaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost < sharedHTTPTransportMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= %d", transport.MaxIdleConnsPerHost, sharedHTTPTransportMaxIdleConnsPerHost)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("expected ForceAttemptHTTP2 to be enabled")
	}
}

func TestNewHTTPClientPreservesTimeout(t *testing.T) {
	client := NewHTTPClient(7 * time.Second)
	if client.Timeout != 7*time.Second {
		t.Fatalf("Timeout = %v, want %v", client.Timeout, 7*time.Second)
	}

	withoutTimeout := NewHTTPClient(0)
	if withoutTimeout.Timeout != 0 {
		t.Fatalf("Timeout = %v, want 0", withoutTimeout.Timeout)
	}
}

func TestNewProviderHTTPClientKeepsLoopbackTransportUncompressed(t *testing.T) {
	local := NewProviderHTTPClient("http://127.0.0.1:11434/v1", 0)
	if local.Transport != sharedHTTPTransport {
		t.Fatalf("loopback transport = %T, want shared local transport", local.Transport)
	}

	remote := NewProviderHTTPClient("https://api.openai.com/v1", 0)
	if remote.Transport != sharedCompressedHTTPTransport {
		t.Fatalf("remote transport = %T, want shared compressed transport", remote.Transport)
	}
}

func TestNewSharedHTTPTransportAppliesFloorsWithoutDefaultTransportClone(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = stubRoundTripper{}
	defer func() { http.DefaultTransport = original }()

	transport := newSharedHTTPTransport()
	if transport.MaxIdleConns < sharedHTTPTransportMaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want >= %d", transport.MaxIdleConns, sharedHTTPTransportMaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost < sharedHTTPTransportMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= %d", transport.MaxIdleConnsPerHost, sharedHTTPTransportMaxIdleConnsPerHost)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("expected ForceAttemptHTTP2 to be enabled")
	}
}
