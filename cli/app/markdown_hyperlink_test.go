package app

import (
	"bytes"
	"strings"
	"testing"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	tuitest "core/internal/testharness/pty"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestStartupMarkdownRendererEmitsMarkdownHyperlinks(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"

	for _, presentation := range []struct {
		name           string
		links          transcriptrender.MarkdownLinkPresentation
		wantLinkedText string
	}{
		{
			name:           "supported terminal",
			links:          transcriptrender.MarkdownLinkLabelOnly,
			wantLinkedText: "PR #456",
		},
		{
			name:           "fallback terminal",
			links:          transcriptrender.MarkdownLinkLabelAndDestination,
			wantLinkedText: "PR #456" + target,
		},
	} {
		for _, theme := range []string{"dark", "light"} {
			t.Run(presentation.name+"/"+theme, func(t *testing.T) {
				renderer := newStartupMarkdownRendererWithLinkPresentation(
					theme,
					presentation.links,
				)
				rendered := renderer.Render("[PR #456](https://github.com/org/repo/pull/456)", 80)

				trace := tuitest.TraceTerminalHyperlinks(t, rendered)
				if got := trace.LinkedText(target); got != presentation.wantLinkedText {
					t.Fatalf("linked text = %q, want %q", got, presentation.wantLinkedText)
				}
			})
		}
	}
}

func TestStartupMarkdownRendererUsesLiveRenderWidth(t *testing.T) {
	renderer := newStartupMarkdownRendererWithLinkPresentation(
		"dark",
		transcriptrender.MarkdownLinkLabelOnly,
	)
	const source = "alpha beta gamma delta"
	narrow := strings.Split(strings.TrimSpace(xansi.Strip(renderer.Render(source, 8))), "\n")
	wide := strings.Split(strings.TrimSpace(xansi.Strip(renderer.Render(source, 80))), "\n")
	if len(narrow) <= len(wide) {
		t.Fatalf("narrow lines = %q, wide lines = %q; want live width to change wrapping", narrow, wide)
	}
	for index, line := range narrow {
		if width := lipgloss.Width(line); width > 8 {
			t.Fatalf("narrow line %d width = %d, want <= 8: %q", index, width, line)
		}
	}
}

func TestStartupMarkdownHeadersUseCurrentSurfaceWidths(t *testing.T) {
	const width = 8
	const source = "**alpha beta gamma delta**"
	tests := []struct {
		name   string
		render func() string
	}{
		{
			name: "auth picker",
			render: func() string {
				model := newStartupPickerModel(source, "fallback", "dark", startupPickerNotice{}, nil)
				model.width = width
				return model.renderHeader()
			},
		},
		{
			name: "project picker",
			render: func() string {
				model := newProjectBindingPickerModel(nil, "dark", projectPickerOptions{
					HeaderMarkdown: source,
					HeaderFallback: "fallback",
				})
				model.width = width
				return model.renderHeader()
			},
		},
		{
			name: "workspace picker",
			render: func() string {
				model := newProjectWorkspacePickerModel(nil, "dark")
				model.width = width
				return model.renderHeader()
			},
		},
		{
			name: "project name prompt",
			render: func() string {
				model := newProjectNamePromptModel("", "dark")
				model.width = width
				return model.renderHeader()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines := strings.Split(strings.TrimSpace(xansi.Strip(test.render())), "\n")
			if len(lines) < 2 {
				t.Fatalf("header lines = %q, want wrapping at current width %d", lines, width)
			}
			for index, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("header line %d width = %d, want <= %d: %q", index, got, width, line)
				}
			}
		})
	}
}

func TestResolvedTerminalCapabilitiesControlBoundedAndOngoingMarkdownLinks(t *testing.T) {
	const target = "https://example.com/composition"
	for _, test := range []struct {
		name           string
		environment    map[string]string
		wantLinkedText string
	}{
		{
			name:           "whitelisted terminal",
			environment:    map[string]string{"TERM_PROGRAM": "ghostty"},
			wantLinkedText: "label",
		},
		{
			name:           "fallback terminal",
			environment:    map[string]string{"TERM_PROGRAM": "Apple_Terminal"},
			wantLinkedText: "label" + target,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := resolveTerminalCapabilities(func(name string) (string, bool) {
				value, present := test.environment[name]
				return value, present
			})

			bounded := newStartupMarkdownRendererWithLinkPresentation(
				"dark",
				capabilities.MarkdownLinks,
			).Render("[label]("+target+")", 80)
			if got := tuitest.TraceTerminalHyperlinks(t, bounded).LinkedText(target); got != test.wantLinkedText {
				t.Fatalf("bounded linked text = %q, want %q", got, test.wantLinkedText)
			}

			var output bytes.Buffer
			surface := ongoing.NewSurfaceWithOptions(&output, ongoing.SurfaceOptions{
				TerminalResize: capabilities.ResizePolicy,
				MarkdownLinks:  capabilities.MarkdownLinks,
			})
			_, err := surface.ApplyTerminalMessage(clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptAssistantDelta{
				StepID:   mustMarkdownHyperlinkStepID(t),
				StreamID: runtimeids.NewAssistantStreamID(),
				Delta:    "[label](" + target + ")\n\n",
				Phase:    transcript.AssistantPhaseCommentary,
			})), ongoing.FrameInput{Size: ongoing.Size{Width: 80, Height: 12}},
			)
			if err != nil {
				t.Fatalf("render ongoing Markdown: %v", err)
			}
			if got := tuitest.TraceTerminalHyperlinks(t, output.String()).LinkedText(target); got != test.wantLinkedText {
				t.Fatalf("ongoing linked text = %q, want %q", got, test.wantLinkedText)
			}
		})
	}
}

func mustMarkdownHyperlinkStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Markdown hyperlink step ID: %v", err)
	}
	return id
}

func TestStartupMarkdownRendererLeavesPlainPRReferenceUnlinked(t *testing.T) {
	renderer := newStartupMarkdownRendererWithLinkPresentation(
		"dark",
		transcriptrender.MarkdownLinkLabelOnly,
	)
	rendered := renderer.Render("PR #456", 80)

	if trace := tuitest.TraceTerminalHyperlinks(t, rendered); len(trace.Events) != 0 {
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

	trace := tuitest.TraceTerminalHyperlinks(t, truncated+" tail")
	if linked := trace.LinkedText(target); linked != "abcd" {
		t.Fatalf("retained linked text=%q want %q", linked, "abcd")
	}
	afterEllipsis := false
	for _, fragment := range trace.Fragments {
		if fragment.Text == "…" {
			afterEllipsis = true
		}
		if afterEllipsis && fragment.Link != nil {
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
			tuitest.TraceTerminalHyperlinks(t, got+" plain")
		})
	}
}

func TestGoalMarkdownLinksStayBoundedAndDoNotReachPadding(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"

	for _, presentation := range []struct {
		name           string
		links          transcriptrender.MarkdownLinkPresentation
		wantLinkedText string
	}{
		{
			name:           "supported terminal",
			links:          transcriptrender.MarkdownLinkLabelOnly,
			wantLinkedText: "PR #456",
		},
		{
			name:           "fallback terminal",
			links:          transcriptrender.MarkdownLinkLabelAndDestination,
			wantLinkedText: "PR #456" + target,
		},
	} {
		for _, theme := range []string{"dark", "light"} {
			t.Run(presentation.name+"/"+theme, func(t *testing.T) {
				m := newProjectedStaticUIModel(
					WithUIMarkdownLinkPresentation(presentation.links),
				)
				m.theme = theme
				m.goal.goal = runtimeGoalFixture("", "[PR #456](https://github.com/org/repo/pull/456)", clientui.RuntimeGoalStatusActive, false)

				var linked strings.Builder
				for _, line := range m.layout().goalOverlayContentLines(12) {
					if width := lipgloss.Width(line); width != 12 {
						t.Fatalf("goal row width=%d want 12: %q", width, line)
					}
					trace := tuitest.TraceTerminalHyperlinks(t, line)
					linked.WriteString(trace.LinkedText(target))
					assertTrailingPaddingIsUnlinked(t, trace)
				}
				if got := linked.String(); got != presentation.wantLinkedText {
					t.Fatalf("goal linked content = %q, want %q", got, presentation.wantLinkedText)
				}
			})
		}
	}
}

func TestGoalOverlayRendersPendingPreviewWithoutDurableID(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.goal.pending = &clientui.GoalPreview{Objective: "ship the queued goal", Status: clientui.RuntimeGoalStatusPaused}
	content := strings.Join(m.layout().goalOverlayContentLines(80), "\n")
	if !strings.Contains(content, "ship the queued goal") || !strings.Contains(content, "paused") || strings.Contains(content, noGoalHint) || strings.Contains(content, "ID:") {
		t.Fatalf("pending Goal overlay = %q, want objective/status without no-goal or durable ID presentation", content)
	}
}

func TestStartupHeaderTrimmingPreservesMarkdownHyperlinks(t *testing.T) {
	const target = "https://github.com/org/repo/pull/456"
	for _, presentation := range []struct {
		name           string
		links          transcriptrender.MarkdownLinkPresentation
		wantLinkedText string
	}{
		{
			name:           "supported terminal",
			links:          transcriptrender.MarkdownLinkLabelOnly,
			wantLinkedText: "PR #456",
		},
		{
			name:           "fallback terminal",
			links:          transcriptrender.MarkdownLinkLabelAndDestination,
			wantLinkedText: "PR #456" + target,
		},
	} {
		t.Run(presentation.name, func(t *testing.T) {
			m := newStartupPickerModel("[PR #456](https://github.com/org/repo/pull/456)", "PR #456", "dark", startupPickerNotice{}, nil)
			m.headerMD = newStartupMarkdownRendererWithLinkPresentation(
				"dark",
				presentation.links,
			)

			got := tuitest.TraceTerminalHyperlinks(t, m.renderHeader()).LinkedText(target)
			if got != presentation.wantLinkedText {
				t.Fatalf("header linked content = %q, want %q", got, presentation.wantLinkedText)
			}
		})
	}
}

func assertTrailingPaddingIsUnlinked(t *testing.T, trace tuitest.HyperlinkTrace) {
	t.Helper()
	for index := len(trace.Fragments) - 1; index >= 0; index-- {
		fragment := trace.Fragments[index]
		if fragment.Text != " " {
			return
		}
		if fragment.Link != nil {
			t.Fatalf("trailing padding inherited hyperlink target: %+v", fragment)
		}
	}
}
