package testsetup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const processEnvironmentProbeName = "KENT_TEST_PROCESS_ENVIRONMENT_PROBE"

// ClearEnvironmentAtTestProcessStart clears environmentName before package tests run.
func ClearEnvironmentAtTestProcessStart(environmentName string) {
	if err := os.Unsetenv(environmentName); err != nil {
		panic(fmt.Sprintf("clear test process environment %s: %v", environmentName, err))
	}
}

// AssertEnvironmentUnsetAtProcessStart verifies that package initialization
// clears environmentName before the selected test starts in a child process.
func AssertEnvironmentUnsetAtProcessStart(
	t testing.TB,
	rootTestName string,
	environmentName string,
) {
	t.Helper()
	if probe, runningProbe := os.LookupEnv(processEnvironmentProbeName); runningProbe {
		if probe != environmentName {
			t.Fatalf("environment probe = %q, want %q", probe, environmentName)
		}
		if _, present := os.LookupEnv(environmentName); present {
			t.Fatalf("%s remained set after test-process initialization", environmentName)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	command := exec.Command(executable, "-test.run=^"+rootTestName+"$")
	command.Env = replaceEnvironment(
		os.Environ(),
		processEnvironmentProbeName+"="+environmentName,
		environmentName+"=unexpected-process-start-value",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("verify %s is cleared at process start: %v output=%q", environmentName, err, output)
	}
}

func replaceEnvironment(environment []string, replacements ...string) []string {
	replacementValues := make(map[string]string, len(replacements))
	for _, replacement := range replacements {
		name, value, found := strings.Cut(replacement, "=")
		if !found {
			panic(fmt.Sprintf("test process environment replacement %q has no value", replacement))
		}
		replacementValues[name] = value
	}
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			panic(fmt.Sprintf("test process environment entry %q has no value", entry))
		}
		if _, replaced := replacementValues[name]; !replaced {
			result = append(result, entry)
		}
	}
	return append(result, replacements...)
}
