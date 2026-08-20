package testenv

import "testing"

func TestProcessEnvironmentProbeRejectsMissingRequiredInputs(t *testing.T) {
	for name, testCase := range map[string]struct {
		rootTestName    string
		environmentName string
	}{
		"missing root test name": {
			environmentName: "KENT_PERSISTENCE_ROOT",
		},
		"missing environment name": {
			rootTestName: "TestProbe",
		},
		"unsupported root test name character": {
			rootTestName:    "TestProbe[run]+",
			environmentName: "KENT_PERSISTENCE_ROOT",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateProcessEnvironmentProbe(testCase.rootTestName, testCase.environmentName); err == nil {
				t.Fatal("validate process environment probe unexpectedly succeeded")
			}
		})
	}
}

func TestTestRunArgumentSelectsExactRootName(t *testing.T) {
	rootTestName := "TestProbe"
	if got, want := testRunArgument(rootTestName), "-test.run=^TestProbe$"; got != want {
		t.Fatalf("test run argument = %q, want %q", got, want)
	}
}

func TestReplaceEnvironmentReplacesOnlyNamedVariables(t *testing.T) {
	got := replaceEnvironment(
		[]string{"HOME=/home/test", "KENT_TEST_PROCESS_ENVIRONMENT_PROBE=stale", "UNCHANGED=value"},
		"KENT_TEST_PROCESS_ENVIRONMENT_PROBE=fresh",
		"KENT_PERSISTENCE_ROOT=isolated",
	)
	want := []string{
		"HOME=/home/test",
		"UNCHANGED=value",
		"KENT_TEST_PROCESS_ENVIRONMENT_PROBE=fresh",
		"KENT_PERSISTENCE_ROOT=isolated",
	}
	if len(got) != len(want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("environment = %v, want %v", got, want)
		}
	}
}
