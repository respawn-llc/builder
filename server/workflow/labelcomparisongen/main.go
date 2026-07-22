package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"unicode/utf8"

	"core/shared/labelcontract"
)

func main() {
	fixturePath := flag.String("fixture", "", "shared comparison fixture JSON file")
	output := flag.String("output", "", "generated desktop comparison JSON file")
	flag.Parse()
	if *fixturePath == "" {
		fmt.Fprintln(os.Stderr, "-fixture is required")
		os.Exit(2)
	}
	if *output == "" {
		fmt.Fprintln(os.Stderr, "-output is required")
		os.Exit(2)
	}
	fixture, err := os.ReadFile(*fixturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read shared label comparison fixture: %v\n", err)
		os.Exit(1)
	}
	generated, err := generateDesktopComparison(fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate desktop label comparison contract: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, generated, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write generated desktop label comparison contract: %v\n", err)
		os.Exit(1)
	}
}

type comparisonFixture struct {
	Version string `json:"version"`
}

type generatedDesktopComparison struct {
	Version         string            `json:"version"`
	FoldByCodePoint map[string]string `json:"fold_by_code_point"`
	Fixture         json.RawMessage   `json:"fixture"`
}

func generateDesktopComparison(fixtureJSON []byte) ([]byte, error) {
	var fixture comparisonFixture
	if err := json.Unmarshal(fixtureJSON, &fixture); err != nil {
		return nil, fmt.Errorf("decode shared comparison fixture: %w", err)
	}
	if fixture.Version != labelcontract.ComparisonVersion {
		return nil, fmt.Errorf("shared comparison fixture version %q does not match %q", fixture.Version, labelcontract.ComparisonVersion)
	}
	foldByCodePoint := make(map[string]string)
	for character := rune(0); character <= utf8.MaxRune; character++ {
		if character >= 0xD800 && character <= 0xDFFF {
			continue
		}
		source := string(character)
		folded := labelcontract.Fold(source)
		if folded == source {
			continue
		}
		foldByCodePoint[strconv.FormatInt(int64(character), 10)] = folded
	}
	generated, err := json.MarshalIndent(generatedDesktopComparison{
		Version:         labelcontract.ComparisonVersion,
		FoldByCodePoint: foldByCodePoint,
		Fixture:         json.RawMessage(fixtureJSON),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode desktop comparison contract: %w", err)
	}
	return append(generated, '\n'), nil
}
