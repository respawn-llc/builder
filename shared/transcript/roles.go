package transcript

import "strings"

type EntryRole string

// EntryRoleSystem marks general runtime notices with typed message metadata.
const EntryRoleSystem EntryRole = "system"

// EntryRoleWarning marks non-error runtime warnings with typed message metadata.
const EntryRoleWarning EntryRole = "warning"

// EntryRoleCompactionSummary marks a persisted compaction or handoff summary.
const EntryRoleCompactionSummary EntryRole = "compaction_summary"

// EntryRoleManualCompactionCarryover marks the synthetic message that preserves
// the last user prompt across a manual compaction boundary.
const EntryRoleManualCompactionCarryover EntryRole = "manual_compaction_carryover"

// EntryRoleDeveloperContext marks developer/meta context that should only
// appear in verbose transcript views (AGENTS, skills, environment, headless prompts).
const EntryRoleDeveloperContext EntryRole = "developer_context"

// EntryRoleDeveloperFeedback marks developer feedback that should remain
// visible in compact transcript views.
const EntryRoleDeveloperFeedback EntryRole = "developer_feedback"

// EntryRoleDeveloperErrorFeedback marks operator-facing error feedback that
// should remain visible in compact transcript views.
const EntryRoleDeveloperErrorFeedback EntryRole = "developer_error_feedback"

// EntryRoleInterruption marks persisted interruption notices.
const EntryRoleInterruption EntryRole = "interruption"

// EntryRoleGoalFeedback marks user-facing goal lifecycle notices.
const EntryRoleGoalFeedback EntryRole = "goal_feedback"

// EntryRoleReviewerStatus marks reviewer lifecycle status entries.
const EntryRoleReviewerStatus EntryRole = "reviewer_status"

// EntryRoleReviewerError marks reviewer lifecycle status entries that report a
// failed supervisor/reviewer action.
const EntryRoleReviewerError EntryRole = "reviewer_error"

// EntryRoleReviewerSuggestions marks reviewer suggestion summary entries.
const EntryRoleReviewerSuggestions EntryRole = "reviewer_suggestions"

func IsReviewerEntryRole(role string) bool {
	switch EntryRole(strings.TrimSpace(role)) {
	case EntryRoleReviewerStatus, EntryRoleReviewerError, EntryRoleReviewerSuggestions:
		return true
	default:
		return false
	}
}
