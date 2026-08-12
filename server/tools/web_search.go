package tools

import (
	"encoding/json"
	"errors"
	"strings"

	"core/shared/toolspec"
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
	Query          string   `json:"query" jsonschema_description:"Required search query string. Keep it specific and concise; include concrete keywords (entity + property + timeframe) and optionally a site hint."`
	AllowedDomains []string `json:"allowed_domains,omitempty" jsonschema_description:"Optional allowlist of domains to constrain sources to preferred or authoritative sites."`
	BlockedDomains []string `json:"blocked_domains,omitempty" jsonschema_description:"Optional blocklist of domains to exclude low-quality or irrelevant sources."`
}

func WebSearchStaticContractSource() StaticContractSource {
	return StaticContractSource{ID: toolspec.ToolWebSearch, Input: WebSearchInput{}}
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
