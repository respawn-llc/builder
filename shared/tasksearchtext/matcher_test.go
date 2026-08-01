package tasksearchtext

import "testing"

func TestLiteralMatcherUsesPinnedInsensitiveNormalization(t *testing.T) {
	tests := []struct {
		name   string
		source string
		query  string
		count  int
	}{
		{
			name:   "english case",
			source: "Fix BUG in KENT-342",
			query:  "bug",
			count:  1,
		},
		{
			name:   "internal identifier substring",
			source: "Fix BUG in KENT-342",
			query:  "342",
			count:  1,
		},
		{
			name:   "russian case",
			source: "Привет, МИР",
			query:  "мир",
			count:  1,
		},
		{
			name:   "latin NFC and NFD source variants",
			source: "café cafe\u0301",
			query:  "cafe",
			count:  2,
		},
		{
			name:   "NFC query matches NFD source",
			source: "cafe\u0301",
			query:  "café",
			count:  1,
		},
		{
			name:   "NFD query matches NFC source",
			source: "café",
			query:  "cafe\u0301",
			count:  1,
		},
		{
			name:   "sharp s does not expand",
			source: "Straße",
			query:  "strasse",
			count:  0,
		},
		{
			name:   "cyrillic short i does not fold to i",
			source: "май",
			query:  "маи",
			count:  0,
		},
		{
			name:   "greek sigma forms",
			source: "Σςσ",
			query:  "σ",
			count:  3,
		},
		{
			name:   "turkish dotted and dotless i",
			source: "İIıi",
			query:  "i",
			count:  3,
		},
		{
			name:   "quotes and punctuation stay literal",
			source: `before a."b"[c] after`,
			query:  `a."b"[c]`,
			count:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matcher, err := NewLiteralMatcher(test.query, LiteralCaseInsensitive)
			if err != nil {
				t.Fatalf("NewLiteralMatcher: %v", err)
			}
			if got := matcher.OccurrenceCount(test.source); got != test.count {
				t.Fatalf("OccurrenceCount(%q) = %d, want %d", test.source, got, test.count)
			}
		})
	}
}

func TestLiteralMatcherRejectsCombiningOnlyInsensitiveQueryAndPreservesShortenedNormalization(t *testing.T) {
	if _, err := NewLiteralMatcher("\u0301", LiteralCaseInsensitive); err == nil {
		t.Fatal("combining-only insensitive query was accepted")
	}

	matcher, err := NewLiteralMatcher("e\u0301", LiteralCaseInsensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	if got := matcher.CandidateExpression(); got != `"e"` {
		t.Fatalf("CandidateExpression = %q, want %q", got, `"e"`)
	}
	if got := matcher.OccurrenceCount("é"); got != 1 {
		t.Fatalf("OccurrenceCount = %d, want 1", got)
	}
}

func TestNormalizedLiteralRuneCountUsesPinnedNormalization(t *testing.T) {
	if got := NormalizedLiteralRuneCount("e\u0301"); got != 1 {
		t.Fatalf("normalized literal rune count = %d, want 1", got)
	}
}

func TestLiteralMatcherCaseSensitiveUsesExactOriginalCodePoints(t *testing.T) {
	matcher, err := NewLiteralMatcher("café", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}

	if got := matcher.OccurrenceCount("Café café cafe\u0301"); got != 1 {
		t.Fatalf("OccurrenceCount = %d, want 1", got)
	}
}

func TestLiteralMatcherEnumeratesOnlyNonOverlappingAdjacentHits(t *testing.T) {
	matcher, err := NewLiteralMatcher("aa", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	if got := matcher.OccurrenceCount("aaaaa"); got != 2 {
		t.Fatalf("OccurrenceCount = %d, want 2", got)
	}

	adjacent, err := NewLiteralMatcher("foo", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	for ordinal := 1; ordinal <= 2; ordinal++ {
		hit, ok := adjacent.NthHit("foofoo", ordinal, 1)
		if !ok {
			t.Fatalf("NthHit ordinal %d is absent", ordinal)
		}
		if hit.Match != "foo" {
			t.Fatalf("NthHit ordinal %d match = %q, want %q", ordinal, hit.Match, "foo")
		}
	}
	if _, ok := adjacent.NthHit("foofoo", 3, 1); ok {
		t.Fatal("third adjacent hit exists")
	}
}

func TestLiteralMatcherMaterializesWholeGraphemeClusters(t *testing.T) {
	matcher, err := NewLiteralMatcher("a", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}

	hit, ok := matcher.NthHit("a\u0301bc", 1, 1)
	if !ok {
		t.Fatal("NthHit is absent")
	}
	if hit.Match != "a\u0301" {
		t.Fatalf("match = %q, want complete grapheme %q", hit.Match, "a\u0301")
	}
	if hit.Before != "" || hit.After != "b" {
		t.Fatalf("context = before %q / after %q, want empty / %q", hit.Before, hit.After, "b")
	}
}

func TestLiteralMatcherMaterializesBoundedGraphemeContext(t *testing.T) {
	matcher, err := NewLiteralMatcher("def", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	hit, ok := matcher.NthHit("abcdefghi", 1, 2)
	if !ok {
		t.Fatal("NthHit is absent")
	}
	if hit.Before != "bc" || hit.Match != "def" || hit.After != "gh" {
		t.Fatalf("two-sided context = %#v", hit)
	}
	if !hit.LeftTruncated || !hit.RightTruncated {
		t.Fatalf("two-sided truncation = %#v, want both sides", hit)
	}

	start, err := NewLiteralMatcher("abc", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	startHit, ok := start.NthHit("abcdef", 1, 2)
	if !ok {
		t.Fatal("start NthHit is absent")
	}
	if startHit.LeftTruncated || !startHit.RightTruncated {
		t.Fatalf("start truncation = %#v, want right only", startHit)
	}

	end, err := NewLiteralMatcher("def", LiteralCaseSensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}
	endHit, ok := end.NthHit("abcdef", 1, 2)
	if !ok {
		t.Fatal("end NthHit is absent")
	}
	if !endHit.LeftTruncated || endHit.RightTruncated {
		t.Fatalf("end truncation = %#v, want left only", endHit)
	}
}

func TestLiteralMatcherSerializesNormalizedLiteralAsOneSafeFTS5Phrase(t *testing.T) {
	matcher, err := NewLiteralMatcher(`A."É`, LiteralCaseInsensitive)
	if err != nil {
		t.Fatalf("NewLiteralMatcher: %v", err)
	}

	if got := matcher.CandidateExpression(); got != `"a.""e"` {
		t.Fatalf("CandidateExpression = %q, want %q", got, `"a.""e"`)
	}
}
