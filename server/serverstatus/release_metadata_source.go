package serverstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	brand "core/shared/config"
)

const defaultLatestReleaseURL = "https://api.github.com/repos/" + brand.RepoSlug + "/releases/latest"

type ReleaseMetadata struct {
	Version string
}

type ReleaseMetadataSource interface {
	LatestRelease(context.Context) (ReleaseMetadata, error)
}

type ReleaseSourceConfigurationError struct {
	Cause error
}

func (e *ReleaseSourceConfigurationError) Error() string {
	if e == nil || e.Cause == nil {
		return "GitHub release source configuration is invalid"
	}
	return e.Cause.Error()
}

func (e *ReleaseSourceConfigurationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ReleaseTransportError struct {
	Cause error
}

func (e *ReleaseTransportError) Error() string {
	if e == nil || e.Cause == nil {
		return "release request transport failed"
	}
	return e.Cause.Error()
}

func (e *ReleaseTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ReleaseHTTPStatusError struct {
	StatusCode int
	Status     string
}

func (e *ReleaseHTTPStatusError) Error() string {
	if e == nil || strings.TrimSpace(e.Status) == "" {
		return "release request returned an invalid HTTP status"
	}
	return e.Status
}

type ReleaseMetadataError struct {
	Cause error
}

func (e *ReleaseMetadataError) Error() string {
	if e == nil || e.Cause == nil {
		return "release metadata is invalid"
	}
	return e.Cause.Error()
}

func (e *ReleaseMetadataError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type GitHubReleaseMetadataSource struct {
	client    *http.Client
	latestURL string
}

type releaseResponseReader struct {
	io.Reader
	failure error
}

func (r *releaseResponseReader) Read(buffer []byte) (int, error) {
	read, err := r.Reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) && r.failure == nil {
		r.failure = err
	}
	return read, err
}

func (r *releaseResponseReader) decodeError(err error) error {
	if r.failure != nil {
		return &ReleaseTransportError{Cause: fmt.Errorf("read latest release response: %w", r.failure)}
	}
	return &ReleaseMetadataError{Cause: fmt.Errorf("decode latest release: %w", err)}
}

func NewDefaultGitHubReleaseMetadataSource() *GitHubReleaseMetadataSource {
	return newGitHubReleaseMetadataSource(nil, defaultLatestReleaseURL)
}

func NewGitHubReleaseMetadataSource(client *http.Client, latestURL string) (*GitHubReleaseMetadataSource, error) {
	parsedURL, err := url.ParseRequestURI(strings.TrimSpace(latestURL))
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		if err == nil {
			err = errors.New("custom GitHub release URL must be an absolute URL")
		}
		return nil, &ReleaseSourceConfigurationError{Cause: err}
	}
	return newGitHubReleaseMetadataSource(client, parsedURL.String()), nil
}

func newGitHubReleaseMetadataSource(client *http.Client, latestURL string) *GitHubReleaseMetadataSource {
	if client == nil {
		client = &http.Client{}
	}
	return &GitHubReleaseMetadataSource{client: client, latestURL: latestURL}
}

func (s *GitHubReleaseMetadataSource) LatestRelease(ctx context.Context) (metadata ReleaseMetadata, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.latestURL, nil)
	if err != nil {
		return ReleaseMetadata{}, &ReleaseTransportError{Cause: err}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "kent")

	response, err := s.client.Do(request)
	if err != nil {
		return ReleaseMetadata{}, &ReleaseTransportError{Cause: err}
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			typedCloseErr := &ReleaseTransportError{Cause: fmt.Errorf("close latest release response: %w", closeErr)}
			metadata = ReleaseMetadata{}
			if resultErr == nil {
				resultErr = typedCloseErr
				return
			}
			resultErr = errors.Join(resultErr, typedCloseErr)
		}
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ReleaseMetadata{}, &ReleaseHTTPStatusError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
		}
	}

	var payload struct {
		TagName *string `json:"tag_name"`
	}
	responseReader := &releaseResponseReader{Reader: response.Body}
	decoder := json.NewDecoder(responseReader)
	if err := decoder.Decode(&payload); err != nil {
		return ReleaseMetadata{}, responseReader.decodeError(err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); responseReader.failure != nil {
		return ReleaseMetadata{}, responseReader.decodeError(err)
	} else if !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return ReleaseMetadata{}, responseReader.decodeError(err)
	}
	if payload.TagName == nil || strings.TrimSpace(*payload.TagName) == "" {
		return ReleaseMetadata{}, &ReleaseMetadataError{Cause: errors.New("latest release tag is required")}
	}
	version, err := parseUpdateVersion(*payload.TagName)
	if err != nil {
		return ReleaseMetadata{}, &ReleaseMetadataError{Cause: fmt.Errorf("latest release tag is invalid: %w", err)}
	}
	return ReleaseMetadata{Version: version.String()}, nil
}
