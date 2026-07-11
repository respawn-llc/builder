package tools

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateWebSearchInputRejectsInvalidQueries(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "empty", query: ""},
		{name: "whitespace", query: "   "},
		{name: "hallucinated tool name", query: "web search"},
		{name: "whitespace wrapped hallucinated tool name", query: " \tweb search\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(WebSearchInput{Query: tt.query})
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}
			err = ValidateWebSearchInput(raw)
			if err == nil {
				t.Fatal("expected query to be rejected")
			}
			if !errors.Is(err, ErrInvalidWebSearchQuery) {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidateWebSearchInputAllowsNearbyQueries(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "case differs", query: "Web Search"},
		{name: "hyphenated", query: "web-search"},
		{name: "specific web search", query: "web search docs"},
		{name: "word order differs", query: "search the web"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(WebSearchInput{Query: tt.query})
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}
			if err := ValidateWebSearchInput(raw); err != nil {
				t.Fatalf("expected query to be allowed, got %v", err)
			}
		})
	}
}

func TestValidateWebSearchDisplayTextUsesInvalidQueryDisplay(t *testing.T) {
	if got := FormatWebSearchDisplayText("web search"); got != "web search: invalid query" {
		t.Fatalf("display = %q, want invalid query display", got)
	}
	if got := FormatWebSearchDisplayText(" \tweb search\n"); got != "web search: invalid query" {
		t.Fatalf("display = %q, want invalid query display", got)
	}
}
