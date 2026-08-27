package transcriptrender

import (
	"strings"
	"testing"
	"unicode"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestUserAndAssistantRowsAlwaysRenderTheirFullSource(t *testing.T) {
	condensed := "forbidden compact preview"
	source := "Complete first paragraph with enough words to wrap.\n\nComplete second paragraph."
	rows := []clientui.TranscriptCommittedRow{
		{
			Kind: clientui.TranscriptRowUser,
			User: &clientui.TranscriptUserRow{Text: source, CondensedText: &condensed},
		},
		{
			Kind: clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				Text:          source,
				CondensedText: &condensed,
				Phase:         transcript.AssistantPhaseCommentary,
			},
		},
	}
	modes := []Mode{
		ModeOngoing,
		ModeOngoingCollapsed,
		ModeOngoingFull,
		ModeOngoingStable,
		ModeDetailCollapsed,
		ModeDetailExpanded,
	}

	for _, row := range rows {
		role := StyleRoleUser
		if row.Kind == clientui.TranscriptRowAssistant {
			role = StyleRoleAssistant
		}
		for _, mode := range modes {
			rendered := RenderCommittedRow(row, 24, "dark", mode)
			text := strings.Join(PlainLines(rendered.Lines), "\n")
			var sourceText strings.Builder
			for _, line := range rendered.Lines {
				for _, span := range line.Spans {
					spanRole, ok := span.Style.Role()
					if ok && spanRole == role {
						sourceText.WriteString(span.Text)
					}
				}
			}
			compactText := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, sourceText.String())
			compactSource := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, source)
			if !strings.Contains(compactText, compactSource) {
				t.Fatalf("kind=%s mode=%d omitted source content: %q", row.Kind, mode, text)
			}
			if strings.Contains(text, condensed) || strings.Contains(text, "…") {
				t.Fatalf("kind=%s mode=%d used compact or ellipsized content: %q", row.Kind, mode, text)
			}
		}
	}
}
