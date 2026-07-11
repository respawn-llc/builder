// Package terminaltext projects untrusted text into terminal-safe display text.
package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

// Plain preserves displayable text and explicit lines. Tabs become spaces and
// all other terminal controls are dropped.
func Plain(text string) string {
	return projectControls(text, preserveLines)
}

// SingleLine preserves displayable text while flattening line breaks and tabs
// to spaces and trimming surrounding whitespace.
func SingleLine(text string) string {
	return strings.TrimSpace(projectControls(text, flattenLines))
}

type lineLayout uint8

const (
	preserveLines lineLayout = iota
	flattenLines
)

func projectControls(text string, layout lineLayout) string {
	var out strings.Builder
	for _, r := range text {
		switch {
		case r == '\n':
			if layout == preserveLines {
				out.WriteRune(r)
			} else {
				out.WriteRune(' ')
			}
		case r == '\t':
			out.WriteRune(' ')
		case unicode.IsControl(r):
			continue
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// PrintableLines preserves explicit line boundaries and printable grapheme
// clusters only. It rejects malformed bytes and any cluster containing a
// terminal control character.
func PrintableLines(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == '\n' {
			out.WriteByte('\n')
			text = text[size:]
			continue
		}
		if unicode.IsControl(r) || (r == utf8.RuneError && size == 1) {
			text = text[size:]
			continue
		}
		grapheme, _ := xansi.FirstGraphemeCluster(text, xansi.GraphemeWidth)
		if len(grapheme) == 0 {
			break
		}
		if isPrintableGrapheme(grapheme) {
			out.WriteString(grapheme)
		}
		text = text[len(grapheme):]
	}
	return out.String()
}

func isPrintableGrapheme(grapheme string) bool {
	hasPrintableRune := false
	for _, r := range grapheme {
		if unicode.IsControl(r) {
			return false
		}
		if unicode.IsPrint(r) {
			hasPrintableRune = true
		}
	}
	return hasPrintableRune
}
