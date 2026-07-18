package main

import (
	"flag"
	"testing"
)

func TestCommandFlagDefaultMetadataUsesTypedGetterValues(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("path", "release/v1", "`ref` custom reference")
	fs.String("zero-string", "0", "string")
	fs.String("empty-string", "", "string")
	fs.Bool("false", false, "false")
	fs.Int("zero-int", 0, "zero")

	tests := map[string]commandFlagDefaultMetadata{
		"path":         {Value: "release/v1", Visible: true, Quote: true},
		"zero-string":  {Value: "0", Visible: true, Quote: true},
		"empty-string": {Value: "", Visible: false, Quote: true},
		"false":        {Value: "false", Visible: false},
		"zero-int":     {Value: "0", Visible: false},
	}
	for name, want := range tests {
		if got := commandFlagDefaultMetadataFor(fs.Lookup(name)); got != want {
			t.Fatalf("metadata for %q = %+v, want %+v", name, got, want)
		}
	}
}
