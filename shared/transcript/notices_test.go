package transcript

import "testing"

func TestLegacyNoticeNormalizationForRole(t *testing.T) {
	cases := []struct {
		role         string
		wantReason   string
		wantSeverity string
	}{
		{role: "error", wantReason: NoticeReasonLegacyUntypedNotice, wantSeverity: NoticeSeverityError},
		{role: "warning", wantReason: NoticeReasonLegacyUntypedNotice, wantSeverity: NoticeSeverityWarning},
		{role: "cache_warning", wantReason: NoticeReasonCacheWarning, wantSeverity: NoticeSeverityWarning},
		{role: "compaction_notice", wantReason: NoticeReasonLegacyUntypedNotice, wantSeverity: NoticeSeverityInfo},
	}

	for _, tt := range cases {
		t.Run(tt.role, func(t *testing.T) {
			if got := LegacyNoticeReasonForRole(tt.role); got != tt.wantReason {
				t.Fatalf("LegacyNoticeReasonForRole = %q, want %q", got, tt.wantReason)
			}
			if got := LegacyNoticeSeverityForRole(tt.role); got != tt.wantSeverity {
				t.Fatalf("LegacyNoticeSeverityForRole = %q, want %q", got, tt.wantSeverity)
			}
		})
	}
}
