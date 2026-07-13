package transcriptrender

import (
	"fmt"

	"core/shared/clientui"
	"core/shared/theme"
)

type Mode uint8

const (
	ModeOngoing Mode = iota
	ModeOngoingCollapsed
	ModeOngoingFull
	ModeOngoingStable
	ModeDetailCollapsed
	ModeDetailExpanded
)

type Row struct {
	Group clientui.TranscriptRowKind
	Lines []Line
}

type StyleRole uint8

const (
	StyleRoleUser StyleRole = iota
	StyleRoleAssistant
	StyleRoleMarkdownCode
	StyleRoleTool
	StyleRoleToolSuccess
	StyleRoleToolError
	StyleRoleToolShell
	StyleRoleToolShellSecondary
	StyleRoleToolPatch
	StyleRoleToolQuestion
	StyleRoleToolWebSearch
	StyleRoleNotice
	StyleRoleNoticeForeground
	StyleRoleNoticeForegroundFaint
	StyleRoleNoticePrimary
	StyleRoleNoticeSecondary
	StyleRoleNoticeReviewer
	StyleRoleWarning
	StyleRoleError
)

type ColorRole uint8

const (
	ColorRoleForeground ColorRole = iota
	ColorRolePrimary
	ColorRoleSecondary
	ColorRoleUser
	ColorRoleAssistant
	ColorRoleTool
	ColorRoleToolSuccess
	ColorRoleToolError
	ColorRoleSuccess
	ColorRoleWarning
	ColorRoleError
)

func ColorRoleForStyle(role StyleRole) ColorRole {
	switch role {
	case StyleRoleUser,
		StyleRoleAssistant,
		StyleRoleTool,
		StyleRoleToolShell,
		StyleRoleToolPatch,
		StyleRoleToolQuestion,
		StyleRoleToolWebSearch,
		StyleRoleNotice,
		StyleRoleNoticeForeground,
		StyleRoleNoticeForegroundFaint:
		return ColorRoleForeground
	case StyleRoleNoticePrimary:
		return ColorRolePrimary
	case StyleRoleMarkdownCode:
		return ColorRolePrimary
	case StyleRoleNoticeSecondary:
		return ColorRoleSecondary
	case StyleRoleNoticeReviewer:
		return ColorRoleSuccess
	case StyleRoleToolShellSecondary:
		return ColorRoleSecondary
	case StyleRoleToolSuccess:
		return ColorRoleToolSuccess
	case StyleRoleToolError:
		return ColorRoleError
	case StyleRoleWarning:
		return ColorRoleWarning
	case StyleRoleError:
		return ColorRoleError
	default:
		return ColorRoleTool
	}
}

type Line struct {
	LeadingSymbol *Span
	Spans         []Span
	Background    LineBackground
}

type Span struct {
	Text  string
	Style SpanStyle
}

type LineBackground uint8

const (
	LineBackgroundDefault LineBackground = iota
	LineBackgroundDiffAdded
	LineBackgroundDiffRemoved
)

type SpanStyleKind uint8

const (
	SpanStyleSemantic SpanStyleKind = iota
	SpanStyleExplicitRGB
)

type SpanAttribute uint8

const (
	SpanAttributeFaint SpanAttribute = 1 << iota
	SpanAttributeBold
	SpanAttributeItalic
	SpanAttributeUnderline
)

type RGBColor struct {
	Red   uint8
	Green uint8
	Blue  uint8
}

func (c RGBColor) Hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.Red, c.Green, c.Blue)
}

type SpanStyle struct {
	Kind         SpanStyleKind
	SemanticRole StyleRole
	Foreground   RGBColor
	Attributes   SpanAttribute
}

func SemanticStyle(role StyleRole, attributes ...SpanAttribute) SpanStyle {
	return SpanStyle{
		Kind:         SpanStyleSemantic,
		SemanticRole: role,
		Attributes:   combineSpanAttributes(attributes),
	}
}

func ExplicitRGBStyle(foreground RGBColor, attributes ...SpanAttribute) SpanStyle {
	return SpanStyle{
		Kind:       SpanStyleExplicitRGB,
		Foreground: foreground,
		Attributes: combineSpanAttributes(attributes),
	}
}

func SemanticSpan(text string, role StyleRole, attributes ...SpanAttribute) Span {
	return Span{Text: text, Style: SemanticStyle(role, attributes...)}
}

func ExplicitRGBSpan(text string, foreground RGBColor, attributes ...SpanAttribute) Span {
	return Span{Text: text, Style: ExplicitRGBStyle(foreground, attributes...)}
}

func combineSpanAttributes(attributes []SpanAttribute) SpanAttribute {
	var combined SpanAttribute
	for _, attribute := range attributes {
		combined |= attribute
	}
	return combined
}

func (s SpanStyle) Has(attribute SpanAttribute) bool {
	return s.Attributes&attribute != 0
}

func (s SpanStyle) With(attribute SpanAttribute) SpanStyle {
	s.Attributes |= attribute
	return s
}

func (s SpanStyle) Role() (StyleRole, bool) {
	if s.Kind != SpanStyleSemantic {
		return 0, false
	}
	return s.SemanticRole, true
}

type ResolvedForegroundKind uint8

const (
	ResolvedForegroundTheme ResolvedForegroundKind = iota
	ResolvedForegroundRGB
)

type ResolvedForeground struct {
	Kind  ResolvedForegroundKind
	Theme theme.Color
	RGB   RGBColor
}

func (f ResolvedForeground) TrueColor() string {
	if f.Kind == ResolvedForegroundRGB {
		return f.RGB.Hex()
	}
	return f.Theme.TrueColor
}

type ResolvedSpanStyle struct {
	Foreground ResolvedForeground
	Faint      bool
	Bold       bool
	Italic     bool
	Underline  bool
}

func ResolveSpanStyle(span Span, themeName string) ResolvedSpanStyle {
	foreground := ResolvedForeground{}
	switch span.Style.Kind {
	case SpanStyleSemantic:
		foreground = ResolvedForeground{
			Kind:  ResolvedForegroundTheme,
			Theme: ColorForRole(ColorRoleForStyle(span.Style.SemanticRole), themeName),
		}
	case SpanStyleExplicitRGB:
		foreground = ResolvedForeground{
			Kind: ResolvedForegroundRGB,
			RGB:  span.Style.Foreground,
		}
	default:
		panic(fmt.Sprintf("resolve transcript span with invalid style kind %d", span.Style.Kind))
	}
	return ResolvedSpanStyle{
		Foreground: foreground,
		Faint:      span.Style.Has(SpanAttributeFaint),
		Bold:       span.Style.Has(SpanAttributeBold),
		Italic:     span.Style.Has(SpanAttributeItalic),
		Underline:  span.Style.Has(SpanAttributeUnderline),
	}
}

func PlainLines(lines []Line) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Plain())
	}
	return out
}

func (l Line) Plain() string {
	out := ""
	if l.LeadingSymbol != nil {
		out += l.LeadingSymbol.Text
	}
	for _, span := range l.Spans {
		out += span.Text
	}
	return out
}

func (l Line) WithLeadingSymbolText(text string) Line {
	if l.LeadingSymbol == nil {
		return l
	}
	symbol := *l.LeadingSymbol
	symbol.Text = text
	l.LeadingSymbol = &symbol
	return l
}
