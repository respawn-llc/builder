package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

const canonicalCodexWireAuthorityPath = "server/llm/openai_http_transport_helpers.go"

var forbiddenDirectCodexHeaders = map[string]struct{}{
	"session_id":            {},
	"x-codex-turn-metadata": {},
	"x-codex-window-id":     {},
}

var canonicalCodexOutboundHeaders = map[string]struct{}{
	"session-id":           {},
	"x-codex-routing-hint": {},
	"x-codex-turn-state":   {},
}

func TestCodexWireHeadersHaveOneCanonicalAuthority(t *testing.T) {
	repoRoot := findRepoRoot(t)
	var findings []string
	for _, relativePath := range repositoryGoSourcePaths(t, repoRoot) {
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, filepath.Join(repoRoot, relativePath), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relativePath, err)
		}
		findings = append(findings, codexWireHeaderFindings(fileSet, file, relativePath)...)
	}
	if len(findings) != 0 {
		t.Fatalf("Codex wire header authority violations:\n%s", joinFindings(findings))
	}
}

func TestCodexWireOwnershipGuardRecognizesOnlyHeaderAuthorities(t *testing.T) {
	source := `package fixture
func set(request *http.Request) {
	request.Header.Set("session_id", "session"); request.Header.Add("x-codex-turn-metadata", "metadata")
	_ = option.WithHeader("x-codex-window-id", "window")
}
type metadata struct { SessionID string ` + "`json:\"session_id\"`" + ` }
`
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if findings := codexWireHeaderFindings(fileSet, file, "fixture.go"); len(findings) != 3 {
		t.Fatalf("findings = %v, want three header authorities and no JSON-tag finding", findings)
	}
}

func codexWireHeaderFindings(fileSet *token.FileSet, file *ast.File, relativePath string) []string {
	canonicalAuthority := relativePath == canonicalCodexWireAuthorityPath
	isTestFile := filepath.Ext(relativePath) == ".go" &&
		len(filepath.Base(relativePath)) >= len("_test.go") &&
		filepath.Base(relativePath)[len(filepath.Base(relativePath))-len("_test.go"):] == "_test.go"
	var findings []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		headerName, callKind, ok := structurallySetHeaderName(call)
		if !ok {
			return true
		}
		if _, forbidden := forbiddenDirectCodexHeaders[headerName]; forbidden {
			findings = append(findings, codexWireFinding(fileSet, call.Pos(), relativePath, "forbidden direct HTTP header "+headerName))
			return true
		}
		if _, governed := canonicalCodexOutboundHeaders[headerName]; !governed {
			return true
		}
		if canonicalAuthority {
			return true
		}
		if callKind == headerCallSetOrAdd && isTestFile {
			return true
		}
		findings = append(findings, codexWireFinding(fileSet, call.Pos(), relativePath, "Codex outbound header "+headerName+" outside "+canonicalCodexWireAuthorityPath))
		return true
	})
	return findings
}

type headerCallKind uint8

const (
	headerCallSetOrAdd headerCallKind = iota
	headerCallWithHeader
)

func structurallySetHeaderName(call *ast.CallExpr) (string, headerCallKind, bool) {
	if len(call.Args) == 0 {
		return "", 0, false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", 0, false
	}
	callKind := headerCallSetOrAdd
	switch selector.Sel.Name {
	case "Set", "Add":
		parent, ok := selector.X.(*ast.SelectorExpr)
		if !ok || parent.Sel.Name != "Header" {
			return "", 0, false
		}
	case "WithHeader":
		callKind = headerCallWithHeader
	default:
		return "", 0, false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", 0, false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, callKind, err == nil
}

func codexWireFinding(fileSet *token.FileSet, position token.Pos, relativePath string, description string) string {
	return relativePath + ":" + strconv.Itoa(fileSet.Position(position).Line) + ": " + description
}

func joinFindings(findings []string) string {
	result := ""
	for index, finding := range findings {
		if index > 0 {
			result += "\n"
		}
		result += finding
	}
	return result
}
