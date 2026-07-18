package main

import (
	"bytes"
	"strings"
	"testing"
)

func runRootCommand(args ...string) (stdout, stderr string, exitCode int) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	exitCode = rootCommand(args, strings.NewReader(""), &stdoutBuffer, &stderrBuffer)
	return stdoutBuffer.String(), stderrBuffer.String(), exitCode
}

func runRootCommandOK(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, exitCode := runRootCommand(args...)
	if exitCode != 0 {
		t.Fatalf("%q exit=%d stdout=%q stderr=%q", args, exitCode, stdout, stderr)
	}
	return stdout, stderr
}
