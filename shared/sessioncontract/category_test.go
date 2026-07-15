package sessioncontract

import "testing"

func TestSessionCategoryVocabulary(t *testing.T) {
	tests := []struct {
		raw  string
		want SessionCategory
	}{
		{raw: "main", want: SessionCategoryMain},
		{raw: "subagent", want: SessionCategorySubagent},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseSessionCategory(test.raw)
			if err != nil {
				t.Fatalf("ParseSessionCategory(%q): %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ParseSessionCategory(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestSessionCategoryRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"", " ", "Main", "subagents", "worker"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseSessionCategory(raw); err == nil {
				t.Fatalf("ParseSessionCategory(%q) succeeded", raw)
			}
		})
	}
}
