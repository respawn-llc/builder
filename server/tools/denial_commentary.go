package tools

import (
	"strconv"
	"strings"
)

const DenialCommentaryMarker = "User also said:"

type DenialCommentaryPresentation struct {
	Commentary *string
}

func (p DenialCommentaryPresentation) Append(base string) string {
	return p.append(base, false)
}

func (p DenialCommentaryPresentation) AppendQuoted(base string) string {
	return p.append(base, true)
}

func (p DenialCommentaryPresentation) append(base string, quoted bool) string {
	if p.Commentary == nil {
		return base
	}
	commentary := strings.TrimSpace(*p.Commentary)
	if quoted {
		commentary = strconv.Quote(commentary)
	}
	return base + "\n" + DenialCommentaryMarker + " " + commentary
}
