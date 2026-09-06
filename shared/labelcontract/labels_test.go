package labelcontract_test

import (
	"testing"

	"core/shared/labelcontract"
)

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
