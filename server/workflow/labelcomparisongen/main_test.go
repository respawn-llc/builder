package main

import (
	"bytes"
	"os"
	"testing"
)

func TestGeneratedTypeScriptCaseFoldTableIsFresh(t *testing.T) {
	const fixturePath = "../../../shared/labelcomparison/testdata/casefold-v1.json"
	const outputPath = "../../../apps/desktop/src/shared/labels/labelComparisonV1.generated.json"
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read shared comparison fixture: %v", err)
	}
	current, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated desktop comparison contract: %v", err)
	}
	generated, err := generateDesktopComparison(fixture)
	if err != nil {
		t.Fatalf("generate desktop comparison contract: %v", err)
	}
	if !bytes.Equal(current, generated) {
		t.Fatalf(
			"generated desktop comparison contract is stale; run go run ./server/workflow/labelcomparisongen -fixture %s -output %s",
			fixturePath,
			outputPath,
		)
	}
}
