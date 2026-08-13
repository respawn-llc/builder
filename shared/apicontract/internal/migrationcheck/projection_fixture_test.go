package migrationcheck

import "testing"

func assertFocusedProjectionFixture(t *testing.T, fixture FocusedProjectionFixtureName) {
	t.Helper()
	if _, exists := requiredFocusedProjectionFixtures()[fixture]; !exists {
		t.Fatalf("focused projection fixture %q is not part of bounded coverage", fixture)
	}
}
