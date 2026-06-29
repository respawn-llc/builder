package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	InvalidWebSearchQueryMessage = "you provided an invalid search query"
	blockedWebSearchQuery        = "web search"
)

// ErrInvalidWebSearchQuery is the sentinel for rejected web search queries.
// Callers match it via errors.Is; the message wording lives in
// InvalidWebSearchQueryMessage for model-facing output.
var ErrInvalidWebSearchQuery = errors.New(InvalidWebSearchQueryMessage)

type WebSearchInput struct {
	Query          string   `json:"query"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

func ParseWebSearchInput(raw json.RawMessage) (WebSearchInput, error) {
	var in WebSearchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return WebSearchInput{}, err
	}
	return in, nil
}

func ValidateWebSearchQuery(query string) error {
	if !isValidWebSearchQuery(normalizeWebSearchQuery(query)) {
		return ErrInvalidWebSearchQuery
	}
	return nil
}

func normalizeWebSearchQuery(query string) string {
	return strings.TrimSpace(query)
}

func isValidWebSearchQuery(normalizedQuery string) bool {
	return normalizedQuery != "" && normalizedQuery != blockedWebSearchQuery
}

func ValidateWebSearchInput(raw json.RawMessage) error {
	in, err := ParseWebSearchInput(raw)
	if err != nil {
		return ErrInvalidWebSearchQuery
	}
	return ValidateWebSearchQuery(in.Query)
}

func FormatWebSearchDisplayText(query string) string {
	trimmed := normalizeWebSearchQuery(query)
	if !isValidWebSearchQuery(trimmed) {
		return "web search: invalid query"
	}
	return fmt.Sprintf("web search: %q", trimmed)
}
