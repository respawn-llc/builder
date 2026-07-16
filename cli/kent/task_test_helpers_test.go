package main

import (
	"strings"
	"testing"
)

func labeledOutputValue(t *testing.T, output string, label string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 2 && fields[0] == label {
			return fields[1]
		}
	}
	if strings.TrimSpace(output) == "" {
		t.Fatalf("label %q not found in empty output", label)
	}
	return ""
}

func taskDetailHeadingShortID(t *testing.T, output string) string {
	t.Helper()
	firstLine, _, _ := strings.Cut(output, "\n")
	shortID, _, ok := strings.Cut(firstLine, ": ")
	if !ok || strings.TrimSpace(shortID) == "" {
		t.Fatalf("task detail heading not found in output %q", output)
	}
	return shortID
}
