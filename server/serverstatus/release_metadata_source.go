package serverstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"core/server/httpcompression"
	brand "core/shared/config"
)

const (
	defaultLatestReleaseURL = "https://api.github.com/repos/" + brand.RepoSlug + "/releases/latest"
	maxReleaseMetadataBytes = 64 * 1024
)

type releaseMetadata struct {
	Version string
}

type releaseMetadataSource interface {
	LatestRelease(context.Context) (releaseMetadata, error)
}

type releaseTransportError struct {
	Cause error
}

func (e *releaseTransportError) Error() string {
	if e == nil || e.Cause == nil {
		return "release request transport failed"
	}
	return e.Cause.Error()
}

func (e *releaseTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type releaseHTTPStatusError struct {
	StatusCode int
	Status     string
}

func (e *releaseHTTPStatusError) Error() string {
	if e == nil || strings.TrimSpace(e.Status) == "" {
		return "release request returned an invalid HTTP status"
	}
	return e.Status
}

type releaseMetadataError struct {
	Cause error
}

func (e *releaseMetadataError) Error() string {
	if e == nil || e.Cause == nil {
		return "release metadata is invalid"
	}
	return e.Cause.Error()
}

func (e *releaseMetadataError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type githubReleaseMetadataSource struct {
	client    *http.Client
	latestURL string
}

func newDefaultGitHubReleaseMetadataSource() *githubReleaseMetadataSource {
	return newGitHubReleaseMetadataSource(nil, defaultLatestReleaseURL)
}

func newGitHubReleaseMetadataSource(client *http.Client, latestURL string) *githubReleaseMetadataSource {
	if client == nil {
		client = httpcompression.NewClient(nil)
	}
	return &githubReleaseMetadataSource{client: client, latestURL: latestURL}
}

func (s *githubReleaseMetadataSource) LatestRelease(ctx context.Context) (metadata releaseMetadata, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.latestURL, nil)
	if err != nil {
		return releaseMetadata{}, &releaseTransportError{Cause: err}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "kent")

	response, err := s.client.Do(request)
	if err != nil {
		return releaseMetadata{}, &releaseTransportError{Cause: err}
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			closeFailure := &releaseTransportError{Cause: fmt.Errorf("close latest release response: %w", closeErr)}
			metadata = releaseMetadata{}
			if resultErr == nil {
				resultErr = closeFailure
			} else {
				resultErr = errors.Join(resultErr, closeFailure)
			}
		}
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return releaseMetadata{}, &releaseHTTPStatusError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
		}
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseMetadataBytes+1))
	if err != nil {
		return releaseMetadata{}, &releaseTransportError{Cause: fmt.Errorf("read latest release response: %w", err)}
	}
	if len(body) > maxReleaseMetadataBytes {
		return releaseMetadata{}, &releaseMetadataError{Cause: errors.New("latest release metadata exceeds maximum size")}
	}

	var payload struct {
		TagName *string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return releaseMetadata{}, &releaseMetadataError{Cause: fmt.Errorf("decode latest release: %w", err)}
	}
	if payload.TagName == nil || strings.TrimSpace(*payload.TagName) == "" {
		return releaseMetadata{}, &releaseMetadataError{Cause: errors.New("latest release tag is required")}
	}
	version, err := parseUpdateVersion(*payload.TagName)
	if err != nil {
		return releaseMetadata{}, &releaseMetadataError{Cause: fmt.Errorf("latest release tag is invalid: %w", err)}
	}
	return releaseMetadata{Version: version.String()}, nil
}
