package label_test

import (
	"encoding/json"
	"os"
	"testing"

	"core/server/workflow/label"
)

type comparisonFixture struct {
	Version  string `json:"version"`
	Fold     []foldFixture
	Contains []containsFixture
	Order    []orderFixture
}

type foldFixture struct {
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

type containsFixture struct {
	Value    string `json:"value"`
	Query    string `json:"query"`
	Expected bool   `json:"expected"`
}

type orderFixture struct {
	Left     string `json:"left"`
	Right    string `json:"right"`
	Expected int    `json:"expected"`
}

func TestComparisonMatchesSharedV1Fixture(t *testing.T) {
	data, err := os.ReadFile("../../../shared/labelcomparison/testdata/casefold-v1.json")
	if err != nil {
		t.Fatalf("read comparison fixture: %v", err)
	}
	var fixture comparisonFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode comparison fixture: %v", err)
	}
	if fixture.Version != label.ComparisonVersion {
		t.Fatalf("fixture version = %q, want %q", fixture.Version, label.ComparisonVersion)
	}
	for _, test := range fixture.Fold {
		if got := label.Fold(test.Input); got != test.Expected {
			t.Errorf("Fold(%q) = %q, want %q", test.Input, got, test.Expected)
		}
	}
	for _, test := range fixture.Contains {
		name, err := label.PrepareName(test.Value)
		if err != nil {
			t.Fatalf("PrepareName(%q): %v", test.Value, err)
		}
		if got := label.Contains(name, test.Query); got != test.Expected {
			t.Errorf("Contains(%q, %q) = %t, want %t", test.Value, test.Query, got, test.Expected)
		}
	}
	for _, test := range fixture.Order {
		left, err := label.PrepareName(test.Left)
		if err != nil {
			t.Fatalf("PrepareName(%q): %v", test.Left, err)
		}
		right, err := label.PrepareName(test.Right)
		if err != nil {
			t.Fatalf("PrepareName(%q): %v", test.Right, err)
		}
		if got := sign(label.Compare(left, right)); got != test.Expected {
			t.Errorf("Compare(%q, %q) sign = %d, want %d", test.Left, test.Right, got, test.Expected)
		}
	}
}

func sign(value int) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	default:
		return 0
	}
}
