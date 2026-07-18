package serverstatus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubReleaseMetadataSourceReturnsValidatedRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", request.Method)
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("Accept = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("User-Agent") != "kent" {
			t.Fatalf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3"}`))
	}))
	defer server.Close()

	source, err := NewGitHubReleaseMetadataSource(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("NewGitHubReleaseMetadataSource: %v", err)
	}
	metadata, err := source.LatestRelease(context.Background())
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if metadata.Version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", metadata.Version)
	}
}

func TestGitHubReleaseMetadataSourceConstructionSeparatesDefaultAndCustomEndpoints(t *testing.T) {
	defaultSource := NewDefaultGitHubReleaseMetadataSource()
	if defaultSource.latestURL != defaultLatestReleaseURL {
		t.Fatalf("default latest URL = %q, want %q", defaultSource.latestURL, defaultLatestReleaseURL)
	}

	for _, endpoint := range []string{"", "  "} {
		source, err := NewGitHubReleaseMetadataSource(nil, endpoint)
		if err == nil {
			t.Fatalf("NewGitHubReleaseMetadataSource(%q) unexpectedly succeeded with %+v", endpoint, source)
		}
		var configurationError *ReleaseSourceConfigurationError
		if !errors.As(err, &configurationError) {
			t.Fatalf("error = %T, want ReleaseSourceConfigurationError", err)
		}
	}
}

func TestGitHubReleaseMetadataSourceReturnsTypedErrors(t *testing.T) {
	transportCause := errors.New("network unavailable")
	tests := []struct {
		name       string
		handler    http.Handler
		client     *http.Client
		wantError  any
		wantUnwrap error
	}{
		{
			name:       "request transport",
			client:     &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, transportCause })},
			wantError:  &ReleaseTransportError{},
			wantUnwrap: transportCause,
		},
		{
			name:      "http client error",
			handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "bad request", http.StatusBadRequest) }),
			wantError: &ReleaseHTTPStatusError{},
		},
		{
			name: "http server error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}),
			wantError: &ReleaseHTTPStatusError{},
		},
		{
			name:      "invalid json",
			handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":`)) }),
			wantError: &ReleaseMetadataError{},
		},
		{
			name:      "missing tag",
			handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }),
			wantError: &ReleaseMetadataError{},
		},
		{
			name:      "blank tag",
			handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"  "}`)) }),
			wantError: &ReleaseMetadataError{},
		},
		{
			name:      "malformed tag",
			handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"release-1.2"}`)) }),
			wantError: &ReleaseMetadataError{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := test.client
			url := "https://release.invalid/latest"
			var server *httptest.Server
			if test.handler != nil {
				server = httptest.NewServer(test.handler)
				defer server.Close()
				client = server.Client()
				url = server.URL
			}

			source, err := NewGitHubReleaseMetadataSource(client, url)
			if err != nil {
				t.Fatalf("NewGitHubReleaseMetadataSource: %v", err)
			}
			_, err = source.LatestRelease(context.Background())
			if err == nil {
				t.Fatal("LatestRelease unexpectedly succeeded")
			}
			switch target := test.wantError.(type) {
			case *ReleaseTransportError:
				if !errors.As(err, &target) {
					t.Fatalf("error = %T, want ReleaseTransportError", err)
				}
			case *ReleaseHTTPStatusError:
				if !errors.As(err, &target) {
					t.Fatalf("error = %T, want ReleaseHTTPStatusError", err)
				}
				if target.StatusCode < 400 || target.Status == "" {
					t.Fatalf("HTTP status error = %+v", target)
				}
			case *ReleaseMetadataError:
				if !errors.As(err, &target) {
					t.Fatalf("error = %T, want ReleaseMetadataError", err)
				}
			default:
				t.Fatalf("unsupported expected error type %T", test.wantError)
			}
			if test.wantUnwrap != nil && !errors.Is(err, test.wantUnwrap) {
				t.Fatalf("error %v does not wrap %v", err, test.wantUnwrap)
			}
		})
	}
}

func TestGitHubReleaseMetadataSourceSurfacesResponseCloseFailure(t *testing.T) {
	closeCause := errors.New("response close failed")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body: &closeErrorBody{
				Reader: strings.NewReader(`{"tag_name":"v1.2.3"}`),
				err:    closeCause,
			},
		}, nil
	})}

	source, err := NewGitHubReleaseMetadataSource(client, "https://release.invalid/latest")
	if err != nil {
		t.Fatalf("NewGitHubReleaseMetadataSource: %v", err)
	}
	_, err = source.LatestRelease(context.Background())
	var transportError *ReleaseTransportError
	if !errors.As(err, &transportError) {
		t.Fatalf("error = %T, want ReleaseTransportError", err)
	}
	if !errors.Is(err, closeCause) {
		t.Fatalf("error %v does not wrap close cause", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type closeErrorBody struct {
	io.Reader
	err error
}

func (b *closeErrorBody) Close() error {
	return b.err
}
