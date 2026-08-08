package workflowview

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"core/server/metadata/sqlitegen"
)

func TestResolveSessionNamesDeduplicatesInputAndMatchesReorderedRowsByExactID(t *testing.T) {
	var queriedIDs []string
	names, err := resolveSessionNames(
		t.Context(),
		func(_ context.Context, ids []string) ([]sqlitegen.ListSessionNamesByIDsRow, error) {
			queriedIDs = append([]string(nil), ids...)
			return []sqlitegen.ListSessionNamesByIDsRow{
				{ID: "session-b", Name: "Second"},
				{ID: "session-a", Name: "First"},
			}, nil
		},
		[]string{"session-a", "session-b", "session-a"},
	)
	if err != nil {
		t.Fatalf("resolveSessionNames: %v", err)
	}
	if !reflect.DeepEqual(queriedIDs, []string{"session-a", "session-b"}) {
		t.Fatalf("queried IDs = %v, want stable deduplicated input", queriedIDs)
	}
	if names["session-a"] == nil || *names["session-a"] != "First" ||
		names["session-b"] == nil || *names["session-b"] != "Second" {
		t.Fatalf("resolved names = %+v", names)
	}
}

func TestResolveSessionNamesRejectsInvalidInputsAndResults(t *testing.T) {
	tests := []struct {
		name       string
		sessionIDs []string
		rows       []sqlitegen.ListSessionNamesByIDsRow
		wantError  string
	}{
		{
			name:       "blank input ID",
			sessionIDs: []string{" "},
			wantError:  "blank session id",
		},
		{
			name:       "blank result ID",
			sessionIDs: []string{"session-a"},
			rows:       []sqlitegen.ListSessionNamesByIDsRow{{ID: " ", Name: "Name"}},
			wantError:  "blank session id",
		},
		{
			name:       "duplicate result ID",
			sessionIDs: []string{"session-a"},
			rows: []sqlitegen.ListSessionNamesByIDsRow{
				{ID: "session-a", Name: "First"},
				{ID: "session-a", Name: "Second"},
			},
			wantError: "duplicate session",
		},
		{
			name:       "missing result",
			sessionIDs: []string{"session-a", "session-b"},
			rows:       []sqlitegen.ListSessionNamesByIDsRow{{ID: "session-a", Name: "First"}},
			wantError:  "session \"session-b\" has no persisted metadata",
		},
		{
			name:       "non-empty whitespace name",
			sessionIDs: []string{"session-a"},
			rows:       []sqlitegen.ListSessionNamesByIDsRow{{ID: "session-a", Name: " "}},
			wantError:  "blank name",
		},
		{
			name:       "result ID only differs by whitespace",
			sessionIDs: []string{"session-a"},
			rows:       []sqlitegen.ListSessionNamesByIDsRow{{ID: " session-a ", Name: "Name"}},
			wantError:  "session \"session-a\" has no persisted metadata",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveSessionNames(
				t.Context(),
				func(context.Context, []string) ([]sqlitegen.ListSessionNamesByIDsRow, error) {
					return test.rows, nil
				},
				test.sessionIDs,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("resolveSessionNames error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestResolveSessionNamesMapsOnlyTheExactEmptySentinelToUnnamed(t *testing.T) {
	names, err := resolveSessionNames(
		t.Context(),
		func(context.Context, []string) ([]sqlitegen.ListSessionNamesByIDsRow, error) {
			return []sqlitegen.ListSessionNamesByIDsRow{{ID: "session-a", Name: ""}}, nil
		},
		[]string{"session-a"},
	)
	if err != nil {
		t.Fatalf("resolveSessionNames: %v", err)
	}
	if name, exists := names["session-a"]; !exists || name != nil {
		t.Fatalf("resolved exact-empty Session name = %v, present = %t; want unnamed", name, exists)
	}
}
