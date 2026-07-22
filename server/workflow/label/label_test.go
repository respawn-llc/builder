package label_test

import (
	"errors"
	"reflect"
	"testing"

	"core/server/workflow/label"
)

func TestParseIDAndPrepareNameAcceptCanonicalValues(t *testing.T) {
	const rawID = "9e7bab10-773a-4a16-9d4f-4e7bd2321327"

	id, err := label.ParseID(rawID)
	if err != nil {
		t.Fatalf("ParseID: %v", err)
	}
	if got := id.String(); got != rawID {
		t.Fatalf("ID.String() = %q, want %q", got, rawID)
	}

	name, err := label.PrepareName("  Café  ")
	if err != nil {
		t.Fatalf("PrepareName: %v", err)
	}
	if got := name.String(); got != "Café" {
		t.Fatalf("Name.String() = %q, want Café", got)
	}

	generatedID := label.NewID()
	if _, err := label.ParseID(generatedID.String()); err != nil {
		t.Fatalf("ParseID(NewID()) = %q: %v", generatedID.String(), err)
	}
}

func TestParseIDRejectsNonCanonicalOrNonV4Values(t *testing.T) {
	for _, raw := range []string{
		"",
		" 9e7bab10-773a-4a16-9d4f-4e7bd2321327",
		"9E7BAB10-773A-4A16-9D4F-4E7BD2321327",
		"9e7bab10-773a-1a16-9d4f-4e7bd2321327",
	} {
		if _, err := label.ParseID(raw); err == nil {
			t.Fatalf("ParseID(%q) succeeded", raw)
		}
	}
}

func TestPrepareNameNormalizesAndValidatesTheLabelAlphabet(t *testing.T) {
	const sixtyFourRunes = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "  Cafe\u0301  ", want: "Café"},
		{raw: "Priority HIGH_2/βeta-３", want: "Priority HIGH_2/βeta-３"},
		{raw: sixtyFourRunes, want: sixtyFourRunes},
	} {
		got, err := label.PrepareName(test.raw)
		if err != nil {
			t.Fatalf("PrepareName(%q): %v", test.raw, err)
		}
		if got.String() != test.want {
			t.Fatalf("PrepareName(%q) = %q, want %q", test.raw, got.String(), test.want)
		}
	}

	for _, test := range []struct {
		raw       string
		reason    label.NameErrorReason
		character *rune
	}{
		{raw: "   ", reason: label.NameErrorRequired},
		{raw: sixtyFourRunes + "a", reason: label.NameErrorTooLong},
		{raw: "customer.acme", reason: label.NameErrorInvalidCharacter, character: runePointer('.')},
		{raw: "release:urgent", reason: label.NameErrorInvalidCharacter, character: runePointer(':')},
		{raw: "ops&support", reason: label.NameErrorInvalidCharacter, character: runePointer('&')},
		{raw: "priority\turgent", reason: label.NameErrorInvalidCharacter, character: runePointer('\t')},
		{raw: "ship🚀", reason: label.NameErrorInvalidCharacter, character: runePointer('🚀')},
	} {
		_, err := label.PrepareName(test.raw)
		var nameErr *label.NameError
		if !errors.As(err, &nameErr) {
			t.Fatalf("PrepareName(%q) error = %v, want *label.NameError", test.raw, err)
		}
		if nameErr.Reason != test.reason {
			t.Fatalf("PrepareName(%q) reason = %q, want %q", test.raw, nameErr.Reason, test.reason)
		}
		if !reflect.DeepEqual(nameErr.Rune, test.character) {
			t.Fatalf("PrepareName(%q) character = %v, want %v", test.raw, nameErr.Rune, test.character)
		}
	}
}

func runePointer(value rune) *rune {
	return &value
}

func TestComparisonUsesUnicodeCaseFolding(t *testing.T) {
	mustName := func(raw string) label.Name {
		t.Helper()
		name, err := label.PrepareName(raw)
		if err != nil {
			t.Fatalf("PrepareName(%q): %v", raw, err)
		}
		return name
	}

	if !label.Equal(mustName("Straße"), mustName("STRASSE")) {
		t.Fatal("Equal should apply full Unicode case folding")
	}
	if !label.Contains(mustName("İstanbul"), "i\u0307ST") {
		t.Fatal("Contains should normalize and case-fold the query")
	}
	if got := label.Compare(mustName("apple"), mustName("Banana")); got >= 0 {
		t.Fatalf("Compare(apple, Banana) = %d, want negative", got)
	}
	if got := label.Compare(mustName("Priority"), mustName("priority")); got != 0 {
		t.Fatalf("Compare(Priority, priority) = %d, want zero", got)
	}
}
