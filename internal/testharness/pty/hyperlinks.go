package pty

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/cellbuf"
)

type Hyperlink struct {
	URL string
}

type HyperlinkEvent struct {
	Link *Hyperlink
}

type HyperlinkFragment struct {
	Text string
	Link *Hyperlink
}

type HyperlinkTrace struct {
	Events    []HyperlinkEvent
	Fragments []HyperlinkFragment
}

func (t HyperlinkTrace) LinkedText(target string) string {
	var out strings.Builder
	for _, fragment := range t.Fragments {
		if fragment.Link != nil && fragment.Link.URL == target {
			out.WriteString(fragment.Text)
		}
	}
	return out.String()
}

func (t HyperlinkTrace) VisibleText() string {
	var out strings.Builder
	for _, fragment := range t.Fragments {
		out.WriteString(fragment.Text)
	}
	return out.String()
}

func (t HyperlinkTrace) OpenCount(target string) int {
	count := 0
	for _, event := range t.Events {
		if event.Link != nil && event.Link.URL == target {
			count++
		}
	}
	return count
}

func TraceTerminalHyperlinks(t testing.TB, output string) HyperlinkTrace {
	t.Helper()

	var trace HyperlinkTrace
	var active *Hyperlink
	parser := xansi.NewParser()
	parser.SetHandler(xansi.Handler{
		Print: func(r rune) {
			trace.Fragments = append(trace.Fragments, HyperlinkFragment{
				Text: string(r),
				Link: cloneHyperlink(active),
			})
		},
		HandleOsc: func(command int, data []byte) {
			if command != 8 {
				return
			}
			var link cellbuf.Link
			cellbuf.ReadLink(data, &link)
			if link.Empty() {
				active = nil
				trace.Events = append(trace.Events, HyperlinkEvent{})
				return
			}
			active = &Hyperlink{URL: link.URL}
			trace.Events = append(trace.Events, HyperlinkEvent{Link: cloneHyperlink(active)})
		},
	})
	parser.Parse([]byte(output))
	if active != nil {
		t.Fatalf("unbalanced OSC 8 hyperlink target %q", active.URL)
	}
	return trace
}

func cloneHyperlink(link *Hyperlink) *Hyperlink {
	if link == nil {
		return nil
	}
	cloned := *link
	return &cloned
}
