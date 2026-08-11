package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"unicode/utf8"
)

const (
	expectedModulePath    = "modernc.org/sqlite"
	expectedModuleVersion = "v1.56.0"
	expectedModuleSum     = "h1:/D8e2RfFqoy/Zc6PuC76U28zFwmI/sYx1Kjm4yEn9e0="

	expectedSQLiteVersion        = "3.53.3"
	sqliteUnicodeSourceUnit      = "fts5_unicode2.c"
	normalizationContractVersion = "kent-task-search-fts5-trigram-normalization-v1"

	sharedSQLiteTablesPath = "lib/sqlite.go"
	fts5UnicodeTablesPath  = "lib/sqlite_g_000000000001feab.go"
)

type moduleMetadata struct {
	Path    string
	Version string
	Sum     string
	Dir     string
}

type sourceFile struct {
	relativePath string
	contents     []byte
	parsed       *ast.File
}

type pinnedSQLiteSource struct {
	module moduleMetadata
	files  map[string]sourceFile
}

type caseEntry struct {
	code   int64
	flags  int64
	range_ int64
}

type normalizationTables struct {
	caseEntries     []caseEntry
	caseOffsets     []int64
	diacriticKeys   []int64
	diacriticValues []int64
	sqliteVersion   string
}

type normalizationMapping struct {
	from    rune
	to      rune
	removed bool
}

func runNormalizationCommand(command string, args []string) error {
	switch command {
	case "generate":
		return generateNormalizationCommand(args)
	case "check":
		return checkNormalizationCommand(args)
	default:
		return fmt.Errorf("unknown normalization generator command %q", command)
	}
}

func generateNormalizationCommand(args []string) error {
	output, err := outputFlag(args)
	if err != nil {
		return err
	}
	generated, err := generatePinnedNormalizationData()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create generated normalization directory: %w", err)
	}
	if err := os.WriteFile(output, generated, 0o644); err != nil {
		return fmt.Errorf("write generated normalization data: %w", err)
	}
	return nil
}

func checkNormalizationCommand(args []string) error {
	output, err := outputFlag(args)
	if err != nil {
		return err
	}
	generated, err := generatePinnedNormalizationData()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(output)
	if err != nil {
		return fmt.Errorf("read generated normalization data: %w", err)
	}
	if err := checkGeneratedOutput(current, generated); err != nil {
		return fmt.Errorf("generated normalization data is stale: %w", err)
	}
	return nil
}

func outputFlag(args []string) (string, error) {
	flags := flag.NewFlagSet("normalizationgen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("output", "", "generated normalization output")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", errors.New("normalization generator does not accept positional arguments")
	}
	if *output == "" {
		return "", errors.New("normalization generator output is required")
	}
	return *output, nil
}

func generatePinnedNormalizationData() ([]byte, error) {
	source, err := loadPinnedSQLiteSource()
	if err != nil {
		return nil, err
	}
	tables, err := extractNormalizationTables(source)
	if err != nil {
		return nil, err
	}
	return renderNormalizationData(source, tables)
}

func loadPinnedSQLiteSource() (pinnedSQLiteSource, error) {
	module, err := resolvePinnedModule()
	if err != nil {
		return pinnedSQLiteSource{}, err
	}
	source := pinnedSQLiteSource{
		module: module,
		files:  make(map[string]sourceFile, 2),
	}
	for _, relativePath := range []string{sharedSQLiteTablesPath, fts5UnicodeTablesPath} {
		fullPath := filepath.Join(module.Dir, filepath.FromSlash(relativePath))
		contents, err := os.ReadFile(fullPath)
		if err != nil {
			return pinnedSQLiteSource{}, fmt.Errorf("read %s: %w", relativePath, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), fullPath, contents, parser.ParseComments)
		if err != nil {
			return pinnedSQLiteSource{}, fmt.Errorf("parse %s: %w", relativePath, err)
		}
		source.files[relativePath] = sourceFile{
			relativePath: relativePath,
			contents:     contents,
			parsed:       parsed,
		}
	}
	if err := source.validate(); err != nil {
		return pinnedSQLiteSource{}, err
	}
	return source, nil
}

func resolvePinnedModule() (moduleMetadata, error) {
	moduleRoot, err := findModuleRoot()
	if err != nil {
		return moduleMetadata{}, err
	}
	command := exec.Command("go", "list", "-m", "-json", expectedModulePath)
	command.Dir = moduleRoot
	output, err := command.Output()
	if err != nil {
		return moduleMetadata{}, fmt.Errorf("resolve pinned SQLite module: %w", err)
	}
	var module moduleMetadata
	if err := json.Unmarshal(output, &module); err != nil {
		return moduleMetadata{}, fmt.Errorf("decode pinned SQLite module: %w", err)
	}
	if err := validateModuleMetadata(module); err != nil {
		return moduleMetadata{}, err
	}
	return module, nil
}

func findModuleRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect module root: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("find module root: go.mod is absent")
		}
		current = parent
	}
}

func validateModuleMetadata(module moduleMetadata) error {
	switch {
	case module.Path != expectedModulePath:
		return fmt.Errorf("unexpected SQLite module path %q", module.Path)
	case module.Version != expectedModuleVersion:
		return fmt.Errorf("unexpected SQLite module version %q", module.Version)
	case module.Sum != expectedModuleSum:
		return fmt.Errorf("unexpected SQLite module checksum %q", module.Sum)
	case module.Dir == "":
		return errors.New("pinned SQLite module directory is absent")
	default:
		return nil
	}
}

func (source pinnedSQLiteSource) validate() error {
	if err := validateModuleMetadata(source.module); err != nil {
		return err
	}
	for _, relativePath := range []string{sharedSQLiteTablesPath, fts5UnicodeTablesPath} {
		file, ok := source.files[relativePath]
		if !ok {
			return fmt.Errorf("pinned SQLite source %s is absent", relativePath)
		}
		if file.parsed == nil {
			return fmt.Errorf("pinned SQLite source %s has no AST", relativePath)
		}
	}
	return nil
}

func extractNormalizationTables(source pinnedSQLiteSource) (normalizationTables, error) {
	if err := source.validate(); err != nil {
		return normalizationTables{}, err
	}
	sharedTables := source.files[sharedSQLiteTablesPath].parsed
	unicodeTables := source.files[fts5UnicodeTablesPath].parsed

	caseEntries, err := extractCaseEntries(sharedTables)
	if err != nil {
		return normalizationTables{}, err
	}
	caseOffsets, err := extractIntArray(sharedTables, "_aiOff")
	if err != nil {
		return normalizationTables{}, err
	}
	sqliteVersion, err := extractStringConstant(sharedTables, "SQLITE_VERSION")
	if err != nil {
		return normalizationTables{}, err
	}
	if sqliteVersion != expectedSQLiteVersion {
		return normalizationTables{}, fmt.Errorf("unexpected SQLite source version %q", sqliteVersion)
	}
	if err := validateFTS5UnicodeAST(unicodeTables); err != nil {
		return normalizationTables{}, err
	}
	diacriticKeys, err := extractLocalIntArray(unicodeTables, "_fts5_remove_diacritic", "aDia")
	if err != nil {
		return normalizationTables{}, err
	}
	diacriticValues, err := extractLocalIntArray(unicodeTables, "_fts5_remove_diacritic", "aChar")
	if err != nil {
		return normalizationTables{}, err
	}
	return normalizationTables{
		caseEntries:     caseEntries,
		caseOffsets:     caseOffsets,
		diacriticKeys:   diacriticKeys,
		diacriticValues: diacriticValues,
		sqliteVersion:   sqliteVersion,
	}, nil
}

func extractCaseEntries(file *ast.File) ([]caseEntry, error) {
	literal, err := findVariableLiteral(file, "_aEntry")
	if err != nil {
		return nil, err
	}
	length, err := arrayLength(literal.Type)
	if err != nil {
		return nil, fmt.Errorf("parse _aEntry length: %w", err)
	}
	entries := make([]caseEntry, length)
	nextIndex := 0
	for _, element := range literal.Elts {
		index, value, err := arrayElement(element, nextIndex)
		if err != nil {
			return nil, fmt.Errorf("parse _aEntry element: %w", err)
		}
		nextIndex = index + 1
		structLiteral, ok := value.(*ast.CompositeLit)
		if !ok {
			return nil, errors.New("_aEntry has a non-struct element")
		}
		fields, err := structLiteralFields(structLiteral)
		if err != nil {
			return nil, err
		}
		code, ok := fields["FiCode"]
		if !ok {
			return nil, errors.New("_aEntry element has no FiCode")
		}
		flags := fields["Fflags"]
		rangeValue, ok := fields["FnRange"]
		if !ok {
			return nil, errors.New("_aEntry element has no FnRange")
		}
		entries[index] = caseEntry{code: code, flags: flags, range_: rangeValue}
	}
	return entries, nil
}

func extractIntArray(file *ast.File, name string) ([]int64, error) {
	literal, err := findVariableLiteral(file, name)
	if err != nil {
		return nil, err
	}
	return evaluateArrayLiteral(literal)
}

func extractStringConstant(file *ast.File, name string) (string, error) {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			valueSpec, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, identifier := range valueSpec.Names {
				if identifier.Name != name {
					continue
				}
				if index >= len(valueSpec.Values) {
					return "", fmt.Errorf("constant %s has no value", name)
				}
				literal, ok := valueSpec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return "", fmt.Errorf("constant %s is not a string literal", name)
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					return "", fmt.Errorf("unquote constant %s: %w", name, err)
				}
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("constant %s is absent", name)
}

func validateFTS5UnicodeAST(file *ast.File) error {
	fold, err := findFunction(file, "_sqlite3Fts5UnicodeFold")
	if err != nil {
		return err
	}
	usedEntry := false
	usedOffsets := false
	calledDiacriticRemoval := false
	ast.Inspect(fold.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.Ident:
			switch node.Name {
			case "_aEntry":
				usedEntry = true
			case "_aiOff":
				usedOffsets = true
			}
		case *ast.CallExpr:
			if identifier, ok := node.Fun.(*ast.Ident); ok && identifier.Name == "_fts5_remove_diacritic" {
				calledDiacriticRemoval = true
			}
		}
		return true
	})
	switch {
	case !usedEntry:
		return errors.New("FTS5 fold function does not reference _aEntry")
	case !usedOffsets:
		return errors.New("FTS5 fold function does not reference _aiOff")
	case !calledDiacriticRemoval:
		return errors.New("FTS5 fold function does not call _fts5_remove_diacritic")
	default:
		return nil
	}
}

func extractLocalIntArray(file *ast.File, functionName string, variableName string) ([]int64, error) {
	function, err := findFunction(file, functionName)
	if err != nil {
		return nil, err
	}
	var found *ast.CompositeLit
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if found != nil {
			return false
		}
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		identifier, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok || identifier.Name != variableName {
			return true
		}
		literal, ok := assignment.Rhs[0].(*ast.CompositeLit)
		if ok {
			found = literal
		}
		return false
	})
	if found == nil {
		return nil, fmt.Errorf("%s table is absent from %s", variableName, functionName)
	}
	return evaluateArrayLiteral(found)
}

func findVariableLiteral(file *ast.File, name string) (*ast.CompositeLit, error) {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, specification := range general.Specs {
			valueSpec, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, identifier := range valueSpec.Names {
				if identifier.Name != name {
					continue
				}
				if index >= len(valueSpec.Values) {
					return nil, fmt.Errorf("variable %s has no value", name)
				}
				literal, ok := valueSpec.Values[index].(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("variable %s is not a composite literal", name)
				}
				return literal, nil
			}
		}
	}
	return nil, fmt.Errorf("variable %s is absent", name)
}

func findFunction(file *ast.File, name string) (*ast.FuncDecl, error) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function, nil
		}
	}
	return nil, fmt.Errorf("function %s is absent", name)
}

func arrayLength(expression ast.Expr) (int, error) {
	array, ok := expression.(*ast.ArrayType)
	if !ok || array.Len == nil {
		return 0, errors.New("array type is required")
	}
	length, err := evaluateInteger(array.Len)
	if err != nil {
		return 0, err
	}
	if length < 0 {
		return 0, errors.New("array length is negative")
	}
	return int(length), nil
}

func evaluateArrayLiteral(literal *ast.CompositeLit) ([]int64, error) {
	length, err := arrayLength(literal.Type)
	if err != nil {
		return nil, err
	}
	values := make([]int64, length)
	nextIndex := 0
	for _, element := range literal.Elts {
		index, value, err := arrayElement(element, nextIndex)
		if err != nil {
			return nil, err
		}
		if index < 0 || index >= length {
			return nil, fmt.Errorf("array index %d is outside length %d", index, length)
		}
		number, err := evaluateInteger(value)
		if err != nil {
			return nil, err
		}
		values[index] = number
		nextIndex = index + 1
	}
	return values, nil
}

func arrayElement(element ast.Expr, nextIndex int) (int, ast.Expr, error) {
	keyValue, ok := element.(*ast.KeyValueExpr)
	if !ok {
		return nextIndex, element, nil
	}
	index, err := evaluateInteger(keyValue.Key)
	if err != nil {
		return 0, nil, err
	}
	return int(index), keyValue.Value, nil
}

func structLiteralFields(literal *ast.CompositeLit) (map[string]int64, error) {
	fields := make(map[string]int64, len(literal.Elts))
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil, errors.New("struct literal field is unkeyed")
		}
		identifier, ok := keyValue.Key.(*ast.Ident)
		if !ok {
			return nil, errors.New("struct literal field name is not an identifier")
		}
		value, err := evaluateInteger(keyValue.Value)
		if err != nil {
			return nil, err
		}
		fields[identifier.Name] = value
	}
	return fields, nil
}

func evaluateInteger(expression ast.Expr) (int64, error) {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		switch expression.Kind {
		case token.INT:
			return strconv.ParseInt(expression.Value, 0, 64)
		case token.CHAR:
			value, err := strconv.Unquote(expression.Value)
			if err != nil {
				return 0, err
			}
			runes := []rune(value)
			if len(runes) != 1 {
				return 0, fmt.Errorf("character literal %q has %d runes", expression.Value, len(runes))
			}
			return int64(runes[0]), nil
		default:
			return 0, fmt.Errorf("unsupported literal kind %s", expression.Kind)
		}
	case *ast.ParenExpr:
		return evaluateInteger(expression.X)
	case *ast.UnaryExpr:
		value, err := evaluateInteger(expression.X)
		if err != nil {
			return 0, err
		}
		switch expression.Op {
		case token.ADD:
			return value, nil
		case token.SUB:
			return -value, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator %s", expression.Op)
		}
	case *ast.BinaryExpr:
		left, err := evaluateInteger(expression.X)
		if err != nil {
			return 0, err
		}
		right, err := evaluateInteger(expression.Y)
		if err != nil {
			return 0, err
		}
		switch expression.Op {
		case token.ADD:
			return left + right, nil
		case token.SUB:
			return left - right, nil
		case token.OR:
			return left | right, nil
		case token.AND:
			return left & right, nil
		case token.SHL:
			return left << right, nil
		case token.SHR:
			return left >> right, nil
		default:
			return 0, fmt.Errorf("unsupported binary operator %s", expression.Op)
		}
	case *ast.CallExpr:
		if len(expression.Args) != 1 {
			return 0, errors.New("integer conversion call must have one argument")
		}
		return evaluateInteger(expression.Args[0])
	default:
		return 0, fmt.Errorf("unsupported integer expression %T", expression)
	}
}

func (tables normalizationTables) normalize(codePoint rune) rune {
	value := int64(codePoint)
	if value < 128 {
		if value >= 'A' && value <= 'Z' {
			return rune(value + ('a' - 'A'))
		}
		return codePoint
	}
	if value >= 65536 {
		if value >= 66560 && value < 66600 {
			return rune(value + 40)
		}
		return codePoint
	}

	entryIndex := -1
	for low, high := 0, len(tables.caseEntries)-1; low <= high; {
		middle := (low + high) / 2
		if value >= tables.caseEntries[middle].code {
			entryIndex = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if entryIndex < 0 {
		return codePoint
	}
	entry := tables.caseEntries[entryIndex]
	if value < entry.code+entry.range_ && entry.flags&1&(entry.code^value) == 0 {
		value = (value + tables.caseOffsets[entry.flags>>1]) & 0xFFFF
	}
	return rune(tables.removeDiacritic(value))
}

func (tables normalizationTables) removeDiacritic(value int64) int64 {
	key := value<<3 | 7
	entryIndex := 0
	for low, high := 0, len(tables.diacriticKeys)-1; low <= high; {
		middle := (low + high) / 2
		if key >= tables.diacriticKeys[middle] {
			entryIndex = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if value > (tables.diacriticKeys[entryIndex]>>3)+(tables.diacriticKeys[entryIndex]&7) {
		return value
	}
	return tables.diacriticValues[entryIndex] & 0x7F
}

func renderNormalizationData(source pinnedSQLiteSource, tables normalizationTables) ([]byte, error) {
	mappings := make([]normalizationMapping, 0)
	for codePoint := rune(0); codePoint <= utf8.MaxRune; codePoint++ {
		if codePoint >= 0xD800 && codePoint <= 0xDFFF {
			continue
		}
		normalized := tables.normalize(codePoint)
		if normalized != codePoint {
			mappings = append(mappings, normalizationMapping{from: codePoint, to: normalized})
		}
	}

	var output bytes.Buffer
	output.WriteString("// Code generated by normalizationgen; DO NOT EDIT.\n\n")
	output.WriteString("package tasksearchtext\n\n")
	fmt.Fprintf(&output, "const NormalizationContractVersion = %q\n", normalizationContractVersion)
	fmt.Fprintf(&output, "const normalizationSourceModulePath = %q\n", source.module.Path)
	fmt.Fprintf(&output, "const normalizationSourceModuleVersion = %q\n", source.module.Version)
	fmt.Fprintf(&output, "const normalizationSourceModuleChecksum = %q\n", source.module.Sum)
	fmt.Fprintf(&output, "const normalizationSQLiteVersion = %q\n", tables.sqliteVersion)
	fmt.Fprintf(&output, "const normalizationSQLiteSourceUnit = %q\n", sqliteUnicodeSourceUnit)
	fmt.Fprintf(&output, "const normalizationSourceChecksum = %q\n\n", sourceChecksum(source))
	output.WriteString("type normalizationRuneMapping struct {\n")
	output.WriteString("\tsource  rune\n")
	output.WriteString("\tto      rune\n")
	output.WriteString("\tremoved bool\n")
	output.WriteString("}\n\n")
	output.WriteString("var insensitiveNormalizationMappings = [...]normalizationRuneMapping{\n")
	for _, mapping := range mappings {
		if mapping.to == 0 {
			fmt.Fprintf(&output, "\t{source: 0x%04X, removed: true},\n", mapping.from)
			continue
		}
		fmt.Fprintf(&output, "\t{source: 0x%04X, to: 0x%04X},\n", mapping.from, mapping.to)
	}
	output.WriteString("}\n")

	formatted, err := format.Source(output.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated normalization data: %w", err)
	}
	return formatted, nil
}

func sourceChecksum(source pinnedSQLiteSource) string {
	hash := sha256.New()
	for _, relativePath := range []string{sharedSQLiteTablesPath, fts5UnicodeTablesPath} {
		file := source.files[relativePath]
		_, _ = hash.Write([]byte(file.relativePath))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(file.contents)
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func checkGeneratedOutput(current []byte, generated []byte) error {
	if bytes.Equal(current, generated) {
		return nil
	}
	return errors.New("generated output differs")
}
