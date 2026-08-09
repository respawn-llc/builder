package transcript

import "strings"

const (
	NoticeReasonCacheWarning        = "cache_warning"
	NoticeReasonCompaction          = "compaction"
	NoticeReasonLegacyUntypedNotice = "legacy_untyped_notice"
	NoticeReasonRuntimeDiagnostic   = "runtime_diagnostic"
	NoticeReasonToolOutputRepair    = "tool_output_repair"

	NoticeSeverityInfo    = "info"
	NoticeSeverityWarning = "warning"
	NoticeSeverityError   = "error"
)

type ToolOutputRepairKind string

const (
	ToolOutputRepairFreshResource         ToolOutputRepairKind = "fresh_resource"
	ToolOutputRepairLiveProviderRejection ToolOutputRepairKind = "live_provider_rejection"
)

type ToolOutputRepairNotice struct {
	Kind  ToolOutputRepairKind `json:"kind"`
	Count int                  `json:"count"`
}

func (n ToolOutputRepairNotice) Valid() bool {
	switch n.Kind {
	case ToolOutputRepairFreshResource, ToolOutputRepairLiveProviderRejection:
		return n.Count > 0
	default:
		return false
	}
}

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
