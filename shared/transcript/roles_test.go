package transcript

import "testing"

func TestReviewerEntryRolesIncludeErrors(t *testing.T) {
	for _, role := range []EntryRole{EntryRoleReviewerStatus, EntryRoleReviewerError, EntryRoleReviewerSuggestions} {
		if !IsReviewerEntryRole(string(role)) {
			t.Fatalf("role %q was not classified as reviewer entry role", role)
		}
	}
}
