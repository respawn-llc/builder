package sessionview

import (
	"reflect"
	"sort"
	"testing"

	"core/shared/clientui"
)

func TestSessionSnapshotCapabilitiesCoverReadModelFields(t *testing.T) {
	covered := map[reflect.Type]map[string]struct{}{
		reflect.TypeOf(clientui.RuntimeMainView{}): fieldSet("Status", "Session", "ActiveRun", "ExternalRuntime"),
		reflect.TypeOf(clientui.RuntimeStatus{}): fieldSet(
			"ReviewerFrequency",
			"ReviewerEnabled",
			"AutoCompactionEnabled",
			"QuestionsEnabled",
			"FastModeAvailable",
			"FastModeEnabled",
			"ConversationFreshness",
			"ParentSessionID",
			"LastCommittedAssistantFinalAnswer",
			"ThinkingLevel",
			"CompactionMode",
			"ContextUsage",
			"CompactionCount",
			"Goal",
			"WorkflowActive",
			"WorkflowSession",
			"Update",
		),
		reflect.TypeOf(clientui.WorkflowSessionStatus{}): fieldSet("RunID", "TaskID", "WorkflowID"),
		reflect.TypeOf(clientui.ExternalRuntimeStatus{}): fieldSet("State", "QueueAccepting"),
		reflect.TypeOf(clientui.RuntimeContextUsage{}):   fieldSet("UsedTokens", "WindowTokens", "CacheHitPercent", "HasCacheHitPercentage"),
		reflect.TypeOf(clientui.RuntimeGoal{}):           fieldSet("ID", "Objective", "Status", "Suspended"),
		reflect.TypeOf(clientui.UpdateStatus{}):          fieldSet("Checked", "Available", "CurrentVersion", "LatestVersion"),
		reflect.TypeOf(clientui.RuntimeSessionView{}):    fieldSet("SessionID", "SessionName", "ConversationFreshness", "ExecutionTarget", "Transcript", "Chat"),
		reflect.TypeOf(clientui.SessionExecutionTarget{}): fieldSet(
			"WorkspaceID",
			"WorkspaceName",
			"WorkspaceRoot",
			"WorkspaceAvailability",
			"WorktreeID",
			"WorktreeName",
			"WorktreeRoot",
			"WorktreeAvailability",
			"CwdRelpath",
			"EffectiveWorkdir",
		),
		reflect.TypeOf(clientui.TranscriptMetadata{}):               fieldSet("Revision", "CommittedEntryCount"),
		reflect.TypeOf(clientui.RunView{}):                          fieldSet("RunID", "SessionID", "StepID", "Status", "Lifecycle", "StartedAt", "FinishedAt"),
		reflect.TypeOf(clientui.TranscriptPage{}):                   fieldSet("SessionID", "SessionName", "ConversationFreshness", "Revision", "CommittedEntryCount", "StartEntryCount", "OlderCursor", "HasMoreAbove", "NewerCursor", "HasMoreBelow", "Entries", "Streaming", "StreamingError"),
		reflect.TypeOf(clientui.CommittedTranscriptSuffix{}):        fieldSet("SessionID", "SessionName", "ConversationFreshness", "Revision", "CommittedEntryCount", "StartEntryCount", "NextEntryCount", "HasMoreCommittedEntries", "Entries"),
		reflect.TypeOf(clientui.CommittedTranscriptSuffixRequest{}): fieldSet(),
		reflect.TypeOf(clientui.ChatEntry{}): fieldSet(
			"Visibility",
			"RollbackTargetID",
			"Role",
			"Text",
			"CondensedText",
			"Phase",
			"MessageType",
			"SourcePath",
			"CompactLabel",
			"ToolResultSummary",
			"ToolCallID",
			"NoticeID",
			"ToolCall",
		),
		reflect.TypeOf(clientui.ToolCallMeta{}): fieldSet(
			"ToolName",
			"Presentation",
			"RenderBehavior",
			"IsShell",
			"UserInitiated",
			"Command",
			"CompactText",
			"InlineMeta",
			"TimeoutLabel",
			"PatchSummary",
			"PatchDetail",
			"PatchRender",
			"RenderHint",
			"Question",
			"Suggestions",
			"RecommendedOptionIndex",
			"OmitSuccessfulResult",
			"RawOutputRequested",
			"OutputTruncated",
		),
		reflect.TypeOf(clientui.ToolRenderHint{}): fieldSet("Kind", "Path", "ResultOnly", "ShellDialect"),
		reflect.TypeOf(clientui.TranscriptPageRequest{}): fieldSet(
			"Cursor",
			"NewerCursor",
		),
	}

	for typ, allowed := range covered {
		t.Run(typ.String(), func(t *testing.T) {
			assertAllFieldsCovered(t, typ, allowed)
		})
	}
}

func TestSessionSnapshotSourcesDeclareRequiredCapabilities(t *testing.T) {
	sources := map[string]SessionSnapshotCapabilities{
		"live":     enrichedSessionSnapshot{base: liveRuntimeSessionSnapshot{}}.Capabilities(),
		"dormant":  enrichedSessionSnapshot{base: dormantSessionSnapshot{}}.Capabilities(),
		"required": requiredSessionSnapshotCapabilities(),
	}
	for name, capabilities := range sources {
		t.Run(name, func(t *testing.T) {
			assertAllCapabilityFlagsEnabled(t, capabilities)
		})
	}
}

func TestBaseSessionSnapshotAdaptersDeclareCommonEnrichmentUnsupported(t *testing.T) {
	want := coreSessionSnapshotCapabilities()
	assertEqualCapabilitySet(t, "live", liveRuntimeSessionSnapshot{}.Capabilities(), want)
	assertEqualCapabilitySet(t, "dormant", dormantSessionSnapshot{}.Capabilities(), want)
	if want.ExecutionTarget || want.UpdateStatus {
		t.Fatalf("base adapters should leave common enrichment unsupported: %+v", want)
	}
}

func fieldSet(fields ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		out[field] = struct{}{}
	}
	return out
}

func assertEqualCapabilitySet(t *testing.T, label string, got, want SessionSnapshotCapabilities) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s capabilities = %+v, want %+v", label, got, want)
	}
}

func assertAllFieldsCovered(t *testing.T, typ reflect.Type, covered map[string]struct{}) {
	t.Helper()
	var missing []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if _, ok := covered[field.Name]; !ok {
			missing = append(missing, field.Name)
		}
	}
	var stale []string
	for field := range covered {
		if _, ok := typ.FieldByName(field); !ok {
			stale = append(stale, field)
		}
	}
	if len(missing) > 0 || len(stale) > 0 {
		sort.Strings(missing)
		sort.Strings(stale)
		t.Fatalf("read model field coverage drift for %s: missing=%v stale=%v", typ, missing, stale)
	}
}

func assertAllCapabilityFlagsEnabled(t *testing.T, capabilities SessionSnapshotCapabilities) {
	t.Helper()
	value := reflect.ValueOf(capabilities)
	typ := value.Type()
	var disabled []string
	for i := 0; i < value.NumField(); i++ {
		if !value.Field(i).Bool() {
			disabled = append(disabled, typ.Field(i).Name)
		}
	}
	if len(disabled) > 0 {
		sort.Strings(disabled)
		t.Fatalf("disabled snapshot capabilities: %v", disabled)
	}
}
