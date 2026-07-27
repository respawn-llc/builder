package sqlitegen

import (
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"core/shared/tasksearchtext"
)

func TestSensitiveLiteralMatchesRemainInsensitiveTrigramCandidates(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE VIRTUAL TABLE task_search_candidate_superset
USING fts5(source, tokenize = 'trigram case_sensitive 0 remove_diacritics 1');`); err != nil {
		t.Fatalf("create insensitive trigram fixture: %v", err)
	}

	fixtures := sensitiveCandidateFixtures(t)
	for rowID, fixture := range fixtures {
		matcher, err := tasksearchtext.NewLiteralMatcher(fixture.query, tasksearchtext.LiteralCaseSensitive)
		if err != nil {
			t.Fatalf("create sensitive matcher for %s: %v", fixture.name, err)
		}
		if matcher.OccurrenceCount(fixture.source) == 0 {
			t.Fatalf("fixture %s has no exact sensitive occurrence", fixture.name)
		}
		if _, err := db.Exec(
			`INSERT INTO task_search_candidate_superset(rowid, source) VALUES (?, ?)`,
			rowID+1,
			fixture.source,
		); err != nil {
			t.Fatalf("insert fixture %s: %v", fixture.name, err)
		}
	}

	for rowID, fixture := range fixtures {
		matcher, err := tasksearchtext.NewLiteralMatcher(fixture.query, tasksearchtext.LiteralCaseSensitive)
		if err != nil {
			t.Fatalf("create sensitive matcher for %s: %v", fixture.name, err)
		}
		if !matchCandidatesContain(t, db, matcher.CandidateExpression(), int64(rowID+1)) {
			t.Fatalf(
				"sensitive fixture %s (%q) is missing from insensitive candidate query %q",
				fixture.name,
				fixture.source,
				matcher.CandidateExpression(),
			)
		}
	}
}

type sensitiveCandidateFixture struct {
	name   string
	source string
	query  string
}

func sensitiveCandidateFixtures(t *testing.T) []sensitiveCandidateFixture {
	t.Helper()
	mappings := generatedInsensitiveNormalizationMappings(t)
	fixtures := make([]sensitiveCandidateFixture, 0, len(mappings)*3+5)
	for _, mapping := range mappings {
		original := string(mapping.from)
		fixtures = append(fixtures,
			sensitiveCandidateFixture{
				name:   "mapping-at-start-" + strconv.FormatInt(int64(mapping.from), 16),
				source: original + "abc",
				query:  original + "abc",
			},
			sensitiveCandidateFixture{
				name:   "mapping-in-middle-" + strconv.FormatInt(int64(mapping.from), 16),
				source: "a" + original + "bc",
				query:  "a" + original + "bc",
			},
			sensitiveCandidateFixture{
				name:   "mapping-at-end-" + strconv.FormatInt(int64(mapping.from), 16),
				source: "abc" + original,
				query:  "abc" + original,
			},
		)
	}
	return append(fixtures,
		sensitiveCandidateFixture{
			name:   "quoted-punctuation",
			source: `before a."b"[c] after`,
			query:  `a."b"[c]`,
		},
		sensitiveCandidateFixture{
			name:   "grapheme-boundaries",
			source: "before a\u0301bc after",
			query:  "a\u0301bc",
		},
		sensitiveCandidateFixture{
			name:   "removed-combining-mark",
			source: "before ab\u0301cd after",
			query:  "ab\u0301cd",
		},
		sensitiveCandidateFixture{
			name:   "unicode-code-point-boundary",
			source: "before αβγ—δεζ after",
			query:  "βγ—δ",
		},
		sensitiveCandidateFixture{
			name:   "whitespace-token-boundaries",
			source: "before abc\tdef\nghi after",
			query:  "abc\tdef\nghi",
		},
	)
}

func matchCandidatesContain(t *testing.T, db *sql.DB, expression string, expectedRowID int64) bool {
	t.Helper()
	rows, err := db.Query(
		`SELECT rowid
FROM task_search_candidate_superset
WHERE task_search_candidate_superset MATCH ?`,
		expression,
	)
	if err != nil {
		t.Fatalf("run insensitive candidate query %q: %v", expression, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			t.Fatalf("scan insensitive candidate row ID: %v", err)
		}
		if rowID == expectedRowID {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate insensitive candidate rows: %v", err)
	}
	return false
}

type normalizationMappingFixture struct {
	from rune
	to   rune
}

func generatedInsensitiveNormalizationMappings(t *testing.T) []normalizationMappingFixture {
	t.Helper()
	generatedPath := filepath.Join(taskSearchCandidateSupersetRepositoryRoot(t), "shared", "tasksearchtext", "normalization_generated.go")
	file, err := parser.ParseFile(token.NewFileSet(), generatedPath, nil, 0)
	if err != nil {
		t.Fatalf("parse generated normalization data: %v", err)
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "insensitiveNormalizationMappings" {
				continue
			}
			if len(value.Values) != 1 {
				t.Fatal("generated normalization mapping declaration does not have one value")
			}
			array, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatal("generated normalization mapping declaration is not a composite literal")
			}
			mappings := make([]normalizationMappingFixture, 0, len(array.Elts))
			for _, element := range array.Elts {
				mappings = append(mappings, generatedNormalizationMapping(t, element))
			}
			if len(mappings) == 0 {
				t.Fatal("generated normalization mapping declaration is empty")
			}
			return mappings
		}
	}
	t.Fatal("generated normalization mapping declaration is absent")
	return nil
}

func generatedNormalizationMapping(t *testing.T, expression ast.Expr) normalizationMappingFixture {
	t.Helper()
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("generated normalization mapping has type %T, want composite literal", expression)
	}
	var mapping normalizationMappingFixture
	var foundFrom, foundTo bool
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("generated normalization mapping field has type %T, want key-value", element)
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok {
			t.Fatalf("generated normalization mapping key has type %T, want identifier", field.Key)
		}
		switch name.Name {
		case "from":
			mapping.from = generatedRuneLiteral(t, field.Value)
			foundFrom = true
		case "to":
			mapping.to = generatedRuneLiteral(t, field.Value)
			foundTo = true
		default:
			t.Fatalf("generated normalization mapping has unexpected field %q", name.Name)
		}
	}
	if !foundFrom || !foundTo {
		t.Fatalf("generated normalization mapping completeness from=%t to=%t", foundFrom, foundTo)
	}
	return mapping
}

func generatedRuneLiteral(t *testing.T, expression ast.Expr) rune {
	t.Helper()
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		t.Fatalf("generated normalization rune has type %T, want integer literal", expression)
	}
	value, err := strconv.ParseInt(literal.Value, 0, 32)
	if err != nil {
		t.Fatalf("parse generated normalization rune %q: %v", literal.Value, err)
	}
	return rune(value)
}

func taskSearchCandidateSupersetRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate candidate-superset test source")
	}
	for directory := filepath.Dir(sourcePath); ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("locate repository root containing go.mod")
		}
	}
}
