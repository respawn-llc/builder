package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestWriteCommandFlagDefaultsUsesDoubleDashLongFlags(t *testing.T) {
	var output bytes.Buffer
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&output)
	fs.Bool("json", false, "write JSON")
	fs.String("page-size", "", "page size")

	writeCommandFlagDefaults(fs)

	rendered := output.String()
	if !strings.Contains(rendered, "--json") || !strings.Contains(rendered, "--page-size") {
		t.Fatalf("help = %q, want double-dash long flags", rendered)
	}
	if strings.Contains(rendered, "\n  -json") || strings.Contains(rendered, "\n  -page-size") {
		t.Fatalf("help = %q, contains single-dash rendered long flags", rendered)
	}
}
