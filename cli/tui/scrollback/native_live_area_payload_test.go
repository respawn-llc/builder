package scrollback

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestNativeLiveAreaPayloadDoesNotCommitStyledPadding(t *testing.T) {
	line := xansi.SetHyperlink("https://example.invalid") + "\x1b[4mhello     \x1b[0m" + xansi.ResetHyperlink()
	payload := nativeLiveAreaPayload([]string{line})

	if strings.Contains(payload, "     ") {
		t.Fatalf("payload kept styled trailing padding: %q", payload)
	}
	if !strings.Contains(payload, xansi.ResetHyperlink()) {
		t.Fatalf("payload does not reset hyperlink state: %q", payload)
	}
	if !strings.Contains(payload, xansi.ResetStyle) {
		t.Fatalf("payload does not reset style state: %q", payload)
	}
}

func TestNativeLiveAreaPayloadDoesNotCommitRuleChrome(t *testing.T) {
	payload := nativeLiveAreaPayload([]string{"hello────────"})

	if strings.Contains(payload, "────────") {
		t.Fatalf("payload kept trailing rule chrome: %q", payload)
	}
	if !strings.Contains(payload, "hello") {
		t.Fatalf("payload removed live content: %q", payload)
	}
}
