package app

import (
	"strings"
	"testing"

	"core/shared/clientui"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
)

type terminalHyperlinkEvent struct {
	Target string
	Active bool
}

type terminalHyperlinkFragment struct {
	Text   string
	Target string
}

type terminalHyperlinkTrace struct {
	Events    []terminalHyperlinkEvent
	Fragments []terminalHyperlinkFragment
}

func (t terminalHyperlinkTrace) linkedText(target string) string {
	var out strings.Builder
	for _, fragment := range t.Fragments {
		if fragment.Target == target {
			out.WriteString(fragment.Text)
		}
	}
	return out.String()
}

func traceTerminalHyperlinks(t *testing.T, output string) terminalHyperlinkTrace {
	t.Helper()

	var trace terminalHyperlinkTrace
	activeTarget := ""
	parser := xansi.NewParser()
	parser.SetHandler(xansi.Handler{
		Print: func(r rune) {
			trace.Fragments = append(trace.Fragments, terminalHyperlinkFragment{
				Text:   string(r),
				Target: activeTarget,
			})
		},
		HandleOsc: func(command int, data []byte) {
			if command != 8 {
				return
			}
			var link cellbuf.Link
			cellbuf.ReadLink(data, &link)
			activeTarget = link.URL
			trace.Events = append(trace.Events, terminalHyperlinkEvent{
				Target: link.URL,
				Active: !link.Empty(),
			})
		},
	})
	parser.Parse([]byte(output))
	if activeTarget != "" {
		t.Fatalf("unbalanced OSC 8 hyperlink target %q", activeTarget)
	}
	return trace
}

func TestStartupMarkdownRendererEmitsMarkdownHyperlinks(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"

	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			renderer := newStartupMarkdownRendererWithWordWrap(theme, 80)
			if renderer == nil {
				t.Fatal("expected markdown renderer")
			}
			rendered, err := renderer.Render("[PR #456](https://github.com/org/repo/pull/456)")
			if err != nil {
				t.Fatalf("render markdown: %v", err)
			}

			trace := traceTerminalHyperlinks(t, rendered)
			if linked := trace.linkedText(target); !strings.Contains(linked, "PR #456") {
				t.Fatalf("linked text %q missing label", linked)
			}
			if linked := trace.linkedText(target); !strings.Contains(linked, target) {
				t.Fatalf("linked text %q missing displayed destination", linked)
			}
		})
	}
}

func TestStartupMarkdownRendererLeavesPlainPRReferenceUnlinked(t *testing.T) {
	renderer := newStartupMarkdownRendererWithWordWrap("dark", 80)
	if renderer == nil {
		t.Fatal("expected markdown renderer")
	}
	rendered, err := renderer.Render("PR #456")
	if err != nil {
		t.Fatalf("render markdown: %v", err)
	}

	if trace := traceTerminalHyperlinks(t, rendered); len(trace.Events) != 0 {
		t.Fatalf("plain PR reference emitted hyperlink events: %+v", trace.Events)
	}
}

func TestTruncateANSIRightClosesHyperlinksBeforeEllipsis(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"
	linked := xansi.SetHyperlink(target, "id=456") + "abcdef" + xansi.ResetHyperlink()

	truncated := truncateANSIRight(linked, 5)
	if got := lipgloss.Width(truncated); got != 5 {
		t.Fatalf("truncated width=%d want 5", got)
	}

	trace := traceTerminalHyperlinks(t, truncated+" tail")
	if linked := trace.linkedText(target); linked != "abcd" {
		t.Fatalf("retained linked text=%q want %q", linked, "abcd")
	}
	afterEllipsis := false
	for _, fragment := range trace.Fragments {
		if fragment.Text == "…" {
			afterEllipsis = true
		}
		if afterEllipsis && fragment.Target != "" {
			t.Fatalf("unrelated fragment %+v inherited hyperlink target", fragment)
		}
	}
}

func TestTruncateANSIRightPreservesGenericBounds(t *testing.T) {
	const target = "https://example.com/wide"
	linkedWide := xansi.SetHyperlink(target) + "界界界" + xansi.ResetHyperlink()
	styled := "\x1b[31mabcdef\x1b[0m"

	tests := []struct {
		name        string
		line        string
		width       int
		wantVisible string
		wantExact   string
	}{
		{name: "width one", line: linkedWide, width: 1, wantVisible: "…"},
		{name: "wide grapheme", line: linkedWide, width: 4},
		{name: "styled row", line: styled, width: 4},
		{name: "already within bounds", line: styled, width: 6, wantExact: styled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateANSIRight(tt.line, tt.width)
			if width := lipgloss.Width(got); width > tt.width {
				t.Fatalf("truncated width=%d want <=%d", width, tt.width)
			}
			if tt.wantVisible != "" && xansi.Strip(got) != tt.wantVisible {
				t.Fatalf("visible truncate=%q want %q", xansi.Strip(got), tt.wantVisible)
			}
			if tt.wantExact != "" && got != tt.wantExact {
				t.Fatalf("truncate=%q want %q", got, tt.wantExact)
			}
			traceTerminalHyperlinks(t, got+" plain")
		})
	}
}

func TestGoalMarkdownLinksStayBoundedAndDoNotReachPadding(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"

	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			m.theme = theme
			m.goal.goal = &clientui.RuntimeGoal{
				Objective: "[PR #456](https://github.com/org/repo/pull/456)",
				Status:    clientui.RuntimeGoalStatusActive,
			}

			var linked strings.Builder
			for _, line := range m.layout().goalOverlayContentLines(12) {
				if width := lipgloss.Width(line); width != 12 {
					t.Fatalf("goal row width=%d want 12: %q", width, line)
				}
				trace := traceTerminalHyperlinks(t, line)
				linked.WriteString(trace.linkedText(target))
				assertTrailingPaddingIsUnlinked(t, trace)
			}
			if got := linked.String(); !strings.Contains(got, "PR") || !strings.Contains(got, "#456") || !strings.Contains(got, target) {
				t.Fatalf("goal linked content=%q want label and destination", got)
			}
		})
	}
}

func TestStartupHeaderTrimmingPreservesMarkdownHyperlinks(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"
	m := newStartupPickerModel("[PR #456](https://github.com/org/repo/pull/456)", "PR #456", "dark", startupPickerNotice{}, nil)

	trace := traceTerminalHyperlinks(t, m.renderHeader())
	if got := trace.linkedText(target); !strings.Contains(got, "PR #456") || !strings.Contains(got, target) {
		t.Fatalf("header linked content=%q want label and destination", got)
	}
}

func assertTrailingPaddingIsUnlinked(t *testing.T, trace terminalHyperlinkTrace) {
	t.Helper()
	for index := len(trace.Fragments) - 1; index >= 0; index-- {
		fragment := trace.Fragments[index]
		if fragment.Text != " " {
			return
		}
		if fragment.Target != "" {
			t.Fatalf("trailing padding inherited hyperlink target: %+v", fragment)
		}
	}
}
