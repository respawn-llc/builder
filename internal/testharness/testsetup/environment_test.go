package testsetup

import "testing"

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
