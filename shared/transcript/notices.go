package transcript

import "strings"

const (
	NoticeReasonCacheWarning        = "cache_warning"
	NoticeReasonCompaction          = "compaction"
	NoticeReasonLegacyUntypedNotice = "legacy_untyped_notice"
	NoticeReasonRuntimeDiagnostic   = "runtime_diagnostic"

	NoticeSeverityInfo    = "info"
	NoticeSeverityWarning = "warning"
	NoticeSeverityError   = "error"
)

func LegacyNoticeReasonForRole(role string) string {
	if strings.TrimSpace(role) == NoticeReasonCacheWarning {
		return NoticeReasonCacheWarning
	}
	return NoticeReasonLegacyUntypedNotice
}

func LegacyNoticeSeverityForRole(role string) string {
	switch strings.TrimSpace(role) {
	case NoticeSeverityError,
		string(EntryRoleDeveloperErrorFeedback),
		string(EntryRoleInterruption),
		string(EntryRoleReviewerError):
		return NoticeSeverityError
	case NoticeSeverityWarning, NoticeReasonCacheWarning:
		return NoticeSeverityWarning
	default:
		return NoticeSeverityInfo
	}
}
