package tools

import (
	"strconv"
	"strings"
)

const DenialCommentaryMarker = "User also said:"

type DenialCommentaryPresentation struct {
	Commentary *string
}

func (p DenialCommentaryPresentation) Value() *string {
	if p.Commentary == nil {
		return nil
	}
	value := strings.TrimSpace(*p.Commentary)
	return &value
}

func (p DenialCommentaryPresentation) Append(base string) string {
	return p.append(base, false)
}

func (p DenialCommentaryPresentation) AppendQuoted(base string) string {
	return p.append(base, true)
}

func (p DenialCommentaryPresentation) append(base string, quoted bool) string {
	commentary := p.Value()
	if commentary == nil {
		return base
	}
	value := *commentary
	if quoted {
		value = strconv.Quote(value)
	}
	return base + "\n" + DenialCommentaryMarker + " " + value
}
