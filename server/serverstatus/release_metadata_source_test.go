package serverstatus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubReleaseMetadataSourceReturnsValidatedRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3"}`))
	}))
	defer server.Close()

	metadata, err := newGitHubReleaseMetadataSource(server.Client(), server.URL).LatestRelease(context.Background())
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if metadata.Version.String() != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", metadata.Version.String())
	}
}

func TestGitHubReleaseMetadataSourceClassifiesTransportFailure(t *testing.T) {
	cause := errors.New("network unavailable")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, cause
	})}

	_, err := newGitHubReleaseMetadataSource(client, "https://release.invalid/latest").LatestRelease(context.Background())
	var transportError *releaseTransportError
	if !errors.As(err, &transportError) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want transport error wrapping %v", err, cause)
	}
}

func TestGitHubReleaseMetadataSourceClassifiesHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := newGitHubReleaseMetadataSource(server.Client(), server.URL).LatestRelease(context.Background())
	var statusError *releaseHTTPStatusError
	if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want HTTP 503 status error", err)
	}
}

func TestGitHubReleaseMetadataSourceBoundsInvalidMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxReleaseMetadataBytes+1)))
	}))
	defer server.Close()

	_, err := newGitHubReleaseMetadataSource(server.Client(), server.URL).LatestRelease(context.Background())
	var metadataError *releaseMetadataError
	if !errors.As(err, &metadataError) {
		t.Fatalf("error = %v, want metadata error", err)
	}
}

func TestGitHubReleaseMetadataSourceRejectsInvalidVersionAtSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.invalid.3"}`))
	}))
	defer server.Close()

	_, err := newGitHubReleaseMetadataSource(server.Client(), server.URL).LatestRelease(context.Background())
	var metadataError *releaseMetadataError
	if !errors.As(err, &metadataError) {
		t.Fatalf("error = %v, want metadata error", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
