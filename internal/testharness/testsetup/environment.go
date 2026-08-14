package testsetup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode"

	"core/shared/config"
)

const processEnvironmentProbeName = "KENT_TEST_PROCESS_ENVIRONMENT_PROBE"

// init applies persistence isolation to every test process that uses testsetup.
func init() {
	ClearEnvironmentAtTestProcessStart(config.PersistenceRootEnvName)
}

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
	if err := validateProcessEnvironmentProbe(rootTestName, environmentName); err != nil {
		t.Fatal(err)
	}
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
	command := exec.Command(executable, testRunArgument(rootTestName))
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

func validateProcessEnvironmentProbe(rootTestName string, environmentName string) error {
	if rootTestName == "" {
		return fmt.Errorf("test process root test name is required")
	}
	for _, character := range rootTestName {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' {
			return fmt.Errorf("test process root test name %q contains unsupported character %q", rootTestName, character)
		}
	}
	if environmentName == "" {
		return fmt.Errorf("test process environment name is required")
	}
	return nil
}

func testRunArgument(rootTestName string) string {
	return "-test.run=^" + rootTestName + "$"
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
