package workflowview

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestTaskSearchReturnsAValidEmptyResponse(t *testing.T) {
	search, err := NewTaskSearch()
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	}
	response, err := search.Search(context.Background(), request)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response validation: %v", err)
	}
	if response.Mode != request.Mode || response.Groups == nil || len(response.Groups) != 0 || response.NextPageToken != nil {
		t.Fatalf("empty task search response = %+v", response)
	}

	_, err = search.Search(context.Background(), serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "ab",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonNormalizedTooShort {
		t.Fatalf("invalid literal search error = %v", err)
	}
}
