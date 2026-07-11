package edit

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"core/server/tools"
)

type rangeMatch struct {
	start           int
	end             int
	actual          string
	quoteNormalized bool
}

type replacementSelection struct {
	matches []rangeMatch
	newText string
}

func selectReplacement(content string, in tools.EditInput) (replacementSelection, error) {
	matches := findMatches(content, in.OldString)
	if len(matches) == 0 {
		return replacementSelection{}, failf("old_string matched 0 occurrences")
	}
	if !in.ReplaceAll && len(matches) != 1 {
		return replacementSelection{}, failf("old_string matched %d occurrences; pass replace_all=true or extend old_string with surrounding context to make it unique", len(matches))
	}
	selected := matches
	if in.ReplaceAll {
		selected = exactOccurrences(content, matches[0].actual, false)
	}
	lineEnding := "\n"
	if strings.Contains(matches[0].actual, "\r\n") {
		lineEnding = "\r\n"
	}
	newText := convertReplacementLineEndings(in.NewString, lineEnding)
	if matches[0].quoteNormalized {
		newText = preserveCurlyQuotes(matches[0].actual, newText)
	}
	if newText == "" {
		selected = extendDeletionNewline(content, selected)
	}
	return replacementSelection{matches: selected, newText: newText}, nil
}

func applyReplacement(content string, selection replacementSelection) string {
	matches := append([]rangeMatch(nil), selection.matches...)
	sort.Slice(matches, func(i, j int) bool { return matches[i].start > matches[j].start })
	out := content
	for _, match := range matches {
		out = out[:match.start] + selection.newText + out[match.end:]
	}
	return out
}

func findMatches(content, old string) []rangeMatch {
	if matches := exactOccurrences(content, old, false); len(matches) > 0 {
		return matches
	}
	strategies := []func(string, string) []rangeMatch{
		func(content, old string) []rangeMatch {
			return lineWindowMatches(content, old, func(actual, expected string) bool {
				return strings.TrimRight(actual, " \t") == strings.TrimRight(expected, " \t")
			})
		},
		blockAnchorMatches,
		func(content, old string) []rangeMatch {
			return normalizedLineWindowMatches(content, old, normalizeWhitespace)
		},
		func(content, old string) []rangeMatch {
			return normalizedLineWindowMatches(content, old, stripCommonIndent)
		},
		quoteNormalizedMatches,
		escapedStringMatches,
		trimmedBoundaryMatches,
		contextAwareMatches,
	}
	for _, strategy := range strategies {
		matches := uniqueRanges(strategy(content, old))
		if len(matches) > 0 {
			return matches
		}
	}
	return exactOccurrences(content, old, true)
}

func exactOccurrences(content, needle string, convertLineEndings bool) []rangeMatch {
	candidates := []string{needle}
	if convertLineEndings {
		if strings.Contains(content, "\r\n") {
			candidates = append(candidates, strings.ReplaceAll(needle, "\n", "\r\n"))
		}
		candidates = append(candidates, strings.ReplaceAll(needle, "\r\n", "\n"))
	}
	var out []rangeMatch
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		start := 0
		for {
			idx := strings.Index(content[start:], candidate)
			if idx < 0 {
				break
			}
			abs := start + idx
			key := strconv.Itoa(abs) + ":" + strconv.Itoa(abs+len(candidate))
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				out = append(out, rangeMatch{start: abs, end: abs + len(candidate), actual: candidate})
			}
			start = abs + max(1, len(candidate))
		}
	}
	return out
}

func blockAnchorMatches(content, old string) []rangeMatch {
	expected := nonEmptyLines(old)
	if len(expected) < 2 {
		return nil
	}
	first := strings.TrimSpace(expected[0])
	last := strings.TrimSpace(expected[len(expected)-1])
	return lineWindowMatches(content, old, func(actual []lineSpan) bool {
		return strings.TrimSpace(actual[0].text) == first && strings.TrimSpace(actual[len(actual)-1].text) == last
	})
}

func normalizedLineWindowMatches(content string, old string, normalize func(string) string) []rangeMatch {
	want := normalize(old)
	return lineWindowMatches(content, old, func(actual, expected string) bool {
		return normalize(actual) == want
	})
}

func escapedStringMatches(content, old string) []rangeMatch {
	unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(old, `"`, `\"`) + `"`)
	if err != nil || unquoted == old {
		return nil
	}
	return exactOccurrences(content, unquoted, true)
}

func quoteNormalizedMatches(content, old string) []rangeMatch {
	needle := normalizeQuotes(old)
	if needle == old {
		return nil
	}
	var out []rangeMatch
	for _, match := range lineWindowMatches(content, old, func(actual, expected string) bool {
		return normalizeQuotes(actual) == needle
	}) {
		match.quoteNormalized = true
		out = append(out, match)
	}
	return out
}

func trimmedBoundaryMatches(content, old string) []rangeMatch {
	trimmed := strings.TrimSpace(old)
	if trimmed == "" || trimmed == old {
		return nil
	}
	return exactOccurrences(content, trimmed, true)
}

func normalizeQuotes(text string) string {
	replacer := strings.NewReplacer(
		"“", `"`,
		"”", `"`,
		"„", `"`,
		"«", `"`,
		"»", `"`,
		"‘", "'",
		"’", "'",
		"‚", "'",
	)
	return replacer.Replace(text)
}

func contextAwareMatches(content, old string) []rangeMatch {
	expected := normalizedNonEmptyLines(old)
	if len(expected) < 3 {
		return nil
	}
	return lineWindowMatches(content, old, func(actual []lineSpan) bool {
		actualLines := normalizedNonEmptyLines(sliceText(actual))
		return stringSlicesEqual(actualLines, expected)
	})
}

func stringSlicesEqual(actual []string, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for idx, value := range actual {
		if value != expected[idx] {
			return false
		}
	}
	return true
}

func normalizedNonEmptyLines(text string) []string {
	lines := nonEmptyLines(text)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		normalized := normalizeWhitespace(line)
		if normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

type lineSpan struct {
	text  string
	start int
	end   int
}

func lineWindowMatches(content, old string, predicate any) []rangeMatch {
	lines := lineSpans(content)
	expected := lineSpans(old)
	if len(expected) == 0 || len(lines) < len(expected) {
		return nil
	}
	var out []rangeMatch
	for i := 0; i+len(expected) <= len(lines); i++ {
		window := lines[i : i+len(expected)]
		ok := false
		switch p := predicate.(type) {
		case func(string, string) bool:
			ok = p(sliceText(window), old)
		case func([]lineSpan) bool:
			ok = p(window)
		}
		if ok {
			start := window[0].start
			end := window[len(window)-1].end
			out = append(out, rangeMatch{start: start, end: end, actual: content[start:end]})
		}
	}
	return out
}

func lineSpans(text string) []lineSpan {
	if text == "" {
		return nil
	}
	var out []lineSpan
	start := 0
	for start < len(text) {
		end := start
		for end < len(text) && text[end] != '\n' {
			end++
		}
		lineEnd := end
		if end < len(text) {
			end++
		}
		out = append(out, lineSpan{text: strings.TrimSuffix(text[start:lineEnd], "\r"), start: start, end: end})
		start = end
	}
	return out
}

func sliceText(lines []lineSpan) string {
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, line.text)
	}
	return strings.Join(parts, "\n")
}

func nonEmptyLines(text string) []string {
	spans := lineSpans(text)
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		if strings.TrimSpace(span.text) != "" {
			out = append(out, span.text)
		}
	}
	return out
}

func normalizeWhitespace(text string) string {
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func stripCommonIndent(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) >= minIndent {
			out = append(out, line[minIndent:])
		} else {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func uniqueRanges(matches []rangeMatch) []rangeMatch {
	seen := map[string]struct{}{}
	out := make([]rangeMatch, 0, len(matches))
	for _, match := range matches {
		key := strconv.Itoa(match.start) + ":" + strconv.Itoa(match.end)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

func convertReplacementLineEndings(text, ending string) string {
	if ending != "\r\n" {
		return strings.ReplaceAll(text, "\r\n", "\n")
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\n", "\r\n")
}

func extendDeletionNewline(content string, matches []rangeMatch) []rangeMatch {
	out := append([]rangeMatch(nil), matches...)
	for i, match := range out {
		if strings.HasSuffix(match.actual, "\n") || match.end >= len(content) {
			continue
		}
		if strings.HasPrefix(content[match.end:], "\r\n") {
			out[i].end += 2
			out[i].actual += "\r\n"
		} else if content[match.end] == '\n' {
			out[i].end++
			out[i].actual += "\n"
		}
	}
	return out
}

func preserveCurlyQuotes(actual, replacement string) string {
	if !strings.ContainsAny(actual, "“”‘’") {
		return replacement
	}
	var out strings.Builder
	inWord := false
	var prev rune
	hasPrev := false
	for _, r := range replacement {
		switch r {
		case '"':
			if inWord {
				out.WriteRune('”')
			} else {
				out.WriteRune('“')
			}
			inWord = !inWord
		case '\'':
			if isOpeningSingleQuote(prev, hasPrev) {
				out.WriteRune('‘')
			} else {
				out.WriteRune('’')
			}
			inWord = false
		default:
			out.WriteRune(r)
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				inWord = true
			} else if unicode.IsSpace(r) {
				inWord = false
			}
		}
		prev = r
		hasPrev = true
	}
	return out.String()
}

func isOpeningSingleQuote(prev rune, hasPrev bool) bool {
	if !hasPrev {
		return true
	}
	if unicode.IsSpace(prev) {
		return true
	}
	return strings.ContainsRune("([{<", prev)
}
