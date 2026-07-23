package labelcontract_test

import (
	"encoding/json"
	"os"
	"testing"

	"core/shared/labelcontract"
)

func TestFoldMatchesVersionedFixture(t *testing.T) {
	data, err := os.ReadFile("../labelcomparison/testdata/casefold-v1.json")
	if err != nil {
		t.Fatalf("read comparison fixture: %v", err)
	}
	var fixture struct {
		Version string `json:"version"`
		Fold    []struct {
			Input    string `json:"input"`
			Expected string `json:"expected"`
		} `json:"fold"`
		Contains []struct {
			Value    string `json:"value"`
			Query    string `json:"query"`
			Expected bool   `json:"expected"`
		} `json:"contains"`
		Order []struct {
			Left     string `json:"left"`
			Right    string `json:"right"`
			Expected int    `json:"expected"`
		} `json:"order"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode comparison fixture: %v", err)
	}
	if fixture.Version != labelcontract.ComparisonVersion {
		t.Fatalf("fixture version = %q, want %q", fixture.Version, labelcontract.ComparisonVersion)
	}
	for _, test := range fixture.Fold {
		if got := labelcontract.Fold(test.Input); got != test.Expected {
			t.Errorf("Fold(%q) = %q, want %q", test.Input, got, test.Expected)
		}
	}
	for _, test := range fixture.Contains {
		if got := labelcontract.Contains(test.Value, test.Query); got != test.Expected {
			t.Errorf("Contains(%q, %q) = %t, want %t", test.Value, test.Query, got, test.Expected)
		}
	}
	for _, test := range fixture.Order {
		if got := sign(labelcontract.Compare(test.Left, test.Right)); got != test.Expected {
			t.Errorf("Compare(%q, %q) sign = %d, want %d", test.Left, test.Right, got, test.Expected)
		}
	}
}

func TestComparisonNormalizesAndMatchesUnicodeText(t *testing.T) {
	if got, want := labelcontract.Fold("Cafe\u0301"), labelcontract.Fold("Café"); got != want {
		t.Fatalf("Fold(NFD café) = %q, want NFC fold %q", got, want)
	}
	if !labelcontract.Equal("Straße", "STRASSE") {
		t.Fatal("Equal should apply full Unicode case folding")
	}
	if !labelcontract.Contains("İstanbul", "i\u0307ST") {
		t.Fatal("Contains should normalize and case-fold the query")
	}
	if got := labelcontract.Compare("apple", "Banana"); got >= 0 {
		t.Fatalf("Compare(apple, Banana) = %d, want negative", got)
	}
	if got := labelcontract.Compare("Priority", "priority"); got != 0 {
		t.Fatalf("Compare(Priority, priority) = %d, want zero", got)
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
