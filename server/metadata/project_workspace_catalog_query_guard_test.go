package metadata_test

import (
	"slices"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
)

func TestProjectWorkspaceCatalogGeneratedQueryIsOneBoundedStatementOverProjectAndWorkspaceRelations(t *testing.T) {
	statements, _, err := parseGeneratedSQLQueries("sqlitegen/queries.sql.go")
	if err != nil {
		t.Fatalf("parse generated metadata queries: %v", err)
	}
	statement, exists := statements["ListProjectWorkspaceCatalogPage"]
	if !exists {
		t.Fatal("generated Project Workspace catalog query is missing")
	}
	tokens, err := testsetup.SQLiteTokens(statement.source)
	if err != nil {
		t.Fatalf("tokenize generated Project Workspace catalog query: %v", err)
	}
	for _, token := range tokens {
		if strings.EqualFold(token.GetText(), "count") {
			t.Fatal("generated Project Workspace catalog query contains a count")
		}
	}
	if !slices.Equal(statement.shape.relations, []string{"catalog_page", "projects", "workspaces"}) {
		t.Fatalf(
			"generated Project Workspace catalog relations = %v, want the page CTE plus Project and Workspace relations",
			statement.shape.relations,
		)
	}
}
