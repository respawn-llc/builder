package migrationcheck

import (
	"testing"

	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	promptcommandpb "core/shared/protoapi/gen/kent/api/prompt_command"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestOnboardingModelChoiceRejectsBlankIdentifiers(t *testing.T) {
	for _, choice := range []*onboardingpb.ModelChoice{
		{
			Kind:    onboardingpb.ModelKind_MODEL_KIND_KNOWN,
			ModelId: stringPointer(" "),
		},
		{
			Kind:  onboardingpb.ModelKind_MODEL_KIND_CUSTOM,
			Alias: stringPointer(" "),
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(choice); err == nil {
			t.Fatal("blank onboarding model identifier accepted")
		}
	}
}

func TestCapabilityDefaultsRejectUnknownCompactionMode(t *testing.T) {
	defaults := &capabilitypb.DefaultFacts{
		PrimaryModelId: "model",
		Thinking:       &capabilitypb.ThinkingDefaultFact{Mode: "none"},
		CompactionMode: "bogus",
	}
	if err := protoapi.ValidateGeneratedMessage(defaults); err == nil {
		t.Fatal("unknown capability compaction mode accepted")
	}
}

func TestProjectWorkspaceContractsRejectBlankAndNoncanonicalValues(t *testing.T) {
	for name, message := range map[string]proto.Message{
		"selector ID": &projectpb.ProjectWorkspaceSelector{
			Selector: &projectpb.ProjectWorkspaceSelector_WorkspaceId{WorkspaceId: " "},
		},
		"selector root": &projectpb.ProjectWorkspaceSelector{
			Selector: &projectpb.ProjectWorkspaceSelector_WorkspaceRoot{WorkspaceRoot: " "},
		},
		"get selector ID": &projectpb.GetProjectWorkspaceRequest{
			ProjectId: "project",
			Selector:  &projectpb.GetProjectWorkspaceRequest_WorkspaceId{WorkspaceId: " "},
		},
		"get selector root": &projectpb.GetProjectWorkspaceRequest{
			ProjectId: "project",
			Selector:  &projectpb.GetProjectWorkspaceRequest_WorkspaceRoot{WorkspaceRoot: " "},
		},
		"blank catalog display name": &projectpb.ProjectWorkspaceCatalogSummary{
			WorkspaceId: "workspace",
			DisplayName: " ",
			RootPath:    "/workspace",
		},
		"spaced catalog workspace ID": &projectpb.ProjectWorkspaceCatalogSummary{
			WorkspaceId: " workspace ",
			DisplayName: "Workspace",
			RootPath:    "/workspace",
		},
		"spaced catalog root": &projectpb.ProjectWorkspaceCatalogSummary{
			WorkspaceId: "workspace",
			DisplayName: "Workspace",
			RootPath:    " /workspace ",
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}

	if err := protoapi.ValidateGeneratedMessage(&projectpb.ProjectWorkspaceCatalogSummary{
		WorkspaceId: "workspace",
		RootPath:    "/",
	}); err != nil {
		t.Fatalf("filesystem root without display name: %v", err)
	}
}

func TestSessionPlanActiveSettingsRejectInvalidZeroValues(t *testing.T) {
	valid := validGeneratedActiveSettings()
	if err := protoapi.ValidateGeneratedMessage(valid); err != nil {
		t.Fatalf("valid active settings: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*sessionlaunchpb.Settings)
	}{
		{name: "model request timeout", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.Timeouts.ModelRequestSeconds = 0
		}},
		{name: "server port", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.ServerPort = 0
		}},
		{name: "model context window", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.ModelContextWindow = 0
		}},
		{name: "compaction threshold", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.ContextCompactionThresholdTokens = 0
		}},
		{name: "pre-submit compaction lead", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.PreSubmitCompactionLeadTokens = 0
		}},
		{name: "minimum exec to background", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.MinimumExecToBgSeconds = 0
		}},
		{name: "shell output limit", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.ShellOutputMaxChars = 0
		}},
		{name: "reviewer context window", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.Reviewer.ModelContextWindow = 0
		}},
		{name: "reviewer timeout", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.Reviewer.TimeoutSeconds = 0
		}},
		{name: "unknown theme", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.Theme = "bogus"
		}},
		{name: "threshold equals model window", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.ContextCompactionThresholdTokens = settings.ModelContextWindow
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := proto.Clone(valid).(*sessionlaunchpb.Settings)
			test.mutate(settings)
			if err := protoapi.ValidateGeneratedMessage(settings); err == nil {
				t.Fatal("invalid zero value accepted")
			}
		})
	}
}

func TestSessionPlanActiveSettingsRejectUnspecifiedEnums(t *testing.T) {
	valid := validGeneratedActiveSettings()
	for _, test := range []struct {
		name   string
		mutate func(*sessionlaunchpb.Settings)
	}{
		{name: "compaction mode", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.CompactionMode = sessionlaunchpb.CompactionMode_COMPACTION_MODE_UNSPECIFIED
		}},
		{name: "background shell output", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.BgShellsOutput = sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_UNSPECIFIED
		}},
		{name: "cache warning mode", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.CacheWarningMode = sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_UNSPECIFIED
		}},
		{name: "sleep prevention", mutate: func(settings *sessionlaunchpb.Settings) {
			settings.PreventSleep = sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_UNSPECIFIED
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := proto.Clone(valid).(*sessionlaunchpb.Settings)
			test.mutate(settings)
			if err := protoapi.ValidateGeneratedMessage(settings); err == nil {
				t.Fatal("unspecified active-setting enum accepted")
			}
		})
	}
}

func validGeneratedActiveSettings() *sessionlaunchpb.Settings {
	return &sessionlaunchpb.Settings{
		ModelCapabilities:                &sessionlaunchpb.ModelCapabilitiesOverride{},
		ProviderCapabilities:             &sessionlaunchpb.ProviderCapabilitiesOverride{},
		Theme:                            "auto",
		ServerPort:                       53082,
		ModelContextWindow:               40000,
		ContextCompactionThresholdTokens: 38000,
		PreSubmitCompactionLeadTokens:    1000,
		MinimumExecToBgSeconds:           15,
		CompactionMode:                   sessionlaunchpb.CompactionMode_COMPACTION_MODE_LOCAL,
		Timeouts:                         &sessionlaunchpb.TimeoutSettings{ModelRequestSeconds: 120},
		ShellOutputMaxChars:              16000,
		BgShellsOutput:                   sessionlaunchpb.BackgroundShellOutputMode_BACKGROUND_SHELL_OUTPUT_MODE_DEFAULT,
		Shell:                            &sessionlaunchpb.ShellSettings{PostprocessingMode: sessionlaunchpb.ShellPostprocessingMode_SHELL_POSTPROCESSING_MODE_BUILTIN},
		CacheWarningMode:                 sessionlaunchpb.CacheWarningMode_CACHE_WARNING_MODE_DEFAULT,
		Worktrees:                        &sessionlaunchpb.WorktreeSettings{},
		Workflow:                         &sessionlaunchpb.WorkflowSettings{},
		Reviewer:                         &sessionlaunchpb.ReviewerSettings{ModelCapabilities: &sessionlaunchpb.ModelCapabilitiesOverride{}, ProviderCapabilities: &sessionlaunchpb.ProviderCapabilitiesOverride{}, ModelContextWindow: 40000, TimeoutSeconds: 120},
		PreventSleep:                     sessionlaunchpb.SleepPreventionMode_SLEEP_PREVENTION_MODE_ACTIVE,
	}
}

func TestCompletedSchemaSlicePromptCommandValidationMatchesCanonicalNames(t *testing.T) {
	for _, command := range []string{"prompt:review", "prompt:review_plan", "prompt:a1"} {
		if err := protoapi.ValidateGeneratedMessage(&promptcommandpb.CatalogEntry{
			Name:    command,
			Preview: "preview",
		}); err != nil {
			t.Fatalf("canonical command %q: %v", command, err)
		}
	}
	for _, command := range []string{
		"prompt:review plan",
		"prompt:Review",
		"prompt:_review",
		"prompt:review_",
		"prompt:review__plan",
		"prompt:review-plan",
		"prompt:",
	} {
		if err := protoapi.ValidateGeneratedMessage(&promptcommandpb.CatalogEntry{
			Name:    command,
			Preview: "preview",
		}); err == nil {
			t.Fatalf("noncanonical catalog command %q accepted", command)
		}
		commandCopy := command
		for name, message := range map[string]proto.Message{
			"catalog_read":      &promptcommandpb.CatalogReadDetails{Command: &commandCopy},
			"command_not_found": &promptcommandpb.CommandNotFoundDetails{Command: command},
			"command_read":      &promptcommandpb.CommandReadDetails{Command: command},
		} {
			if err := protoapi.ValidateGeneratedMessage(message); err == nil {
				t.Fatalf("%s accepted noncanonical command %q", name, command)
			}
		}
	}
}

func TestCompletedSchemaSliceProjectBlockersKeepOnlyClientIndependentFacts(t *testing.T) {
	if err := protoapi.ValidateGeneratedMessage(&projectpb.WorkspaceUnlinkBlocker{
		Code:  "active_sessions",
		Count: int32Pointer(1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := protoapi.ValidateGeneratedMessage(&projectpb.ProjectDeleteBlocker{
		Code:  "active_sessions",
		Count: 1,
	}); err != nil {
		t.Fatal(err)
	}
	assertExactFields(t, (&projectpb.WorkspaceUnlinkBlocker{}).ProtoReflect().Descriptor(), "code", "count")
	assertExactFields(t, (&projectpb.ProjectDeleteBlocker{}).ProtoReflect().Descriptor(), "code", "count")
}

func TestCompletedSchemaSliceProjectBlockerSummariesAreBounded(t *testing.T) {
	unlinkBlockers := make([]*projectpb.WorkspaceUnlinkBlocker, 6)
	for index := range unlinkBlockers {
		unlinkBlockers[index] = &projectpb.WorkspaceUnlinkBlocker{
			Code:  "active_sessions",
			Count: int32Pointer(1),
		}
	}
	validUnlink := &projectpb.UnlinkWorkspaceSuccess{
		ProjectId:   "project-1",
		WorkspaceId: "workspace-1",
		Blockers:    unlinkBlockers,
	}
	if err := protoapi.ValidateGeneratedMessage(validUnlink); err != nil {
		t.Fatalf("six unlink blockers rejected: %v", err)
	}
	invalidUnlink := proto.Clone(validUnlink).(*projectpb.UnlinkWorkspaceSuccess)
	invalidUnlink.Blockers = append(invalidUnlink.Blockers, &projectpb.WorkspaceUnlinkBlocker{
		Code:  "active_sessions",
		Count: int32Pointer(1),
	})
	if err := protoapi.ValidateGeneratedMessage(invalidUnlink); err == nil {
		t.Fatal("seven unlink blockers accepted")
	}

	deleteBlockers := []*projectpb.ProjectDeleteBlocker{
		{Code: "non_terminal_tasks", Count: 1},
		{Code: "active_sessions", Count: 1},
	}
	validDelete := &projectpb.DeleteProjectSuccess{
		ProjectId: "project-1",
		Blockers:  deleteBlockers,
	}
	if err := protoapi.ValidateGeneratedMessage(validDelete); err != nil {
		t.Fatalf("two delete blockers rejected: %v", err)
	}
	invalidDelete := proto.Clone(validDelete).(*projectpb.DeleteProjectSuccess)
	invalidDelete.Blockers = append(invalidDelete.Blockers, &projectpb.ProjectDeleteBlocker{
		Code:  "active_sessions",
		Count: 1,
	})
	if err := protoapi.ValidateGeneratedMessage(invalidDelete); err == nil {
		t.Fatal("three delete blockers accepted")
	}
}

func TestCompletedSchemaSliceAuthDisplayOriginBoundaries(t *testing.T) {
	for _, hostname := range []string{"example.com", "127.0.0.1", "2001:db8::1", "fe80::1%en0"} {
		if err := protoapi.ValidateGeneratedMessage(&authpb.ProviderDisplayOrigin{
			Scheme:   "https",
			Hostname: hostname,
		}); err != nil {
			t.Fatalf("valid hostname %q: %v", hostname, err)
		}
	}
	for _, hostname := range []string{
		"user@example.com",
		"example.com:8443",
		"example.com/path",
		"example.com?token=secret",
		"example.com#fragment",
		"example.com\nattacker",
		"fe80::1%eth0/path",
		"fe80::1%eth0?token=secret",
		"fe80::1%user@example.com",
	} {
		if err := protoapi.ValidateGeneratedMessage(&authpb.ProviderDisplayOrigin{
			Scheme:   "https",
			Hostname: hostname,
		}); err == nil {
			t.Fatalf("invalid hostname %q accepted", hostname)
		}
	}
	for _, port := range []string{"1", "443", "65535"} {
		portCopy := port
		if err := protoapi.ValidateGeneratedMessage(&authpb.ProviderDisplayOrigin{
			Scheme:   "https",
			Hostname: "example.com",
			Port:     &portCopy,
		}); err != nil {
			t.Fatalf("valid port %q: %v", port, err)
		}
	}
	for _, port := range []string{"0", "65536", "abc", " 443"} {
		portCopy := port
		if err := protoapi.ValidateGeneratedMessage(&authpb.ProviderDisplayOrigin{
			Scheme:   "https",
			Hostname: "example.com",
			Port:     &portCopy,
		}); err == nil {
			t.Fatalf("invalid port %q accepted", port)
		}
	}
}

func TestAuthUnavailableResolutionRejectsPartialFailure(t *testing.T) {
	failure := &authpb.StatusFailure{
		Cause: "unavailable",
	}
	resolution := &authpb.StatusResolution{
		Resolution: &authpb.StatusResolution_Unavailable{Unavailable: failure},
	}
	if err := protoapi.ValidateGeneratedMessage(resolution); err != nil {
		t.Fatalf("unavailable resolution: %v", err)
	}
	resolution.PartialFailure = failure
	if err := protoapi.ValidateGeneratedMessage(resolution); err == nil {
		t.Fatal("unavailable resolution accepted partial failure")
	}
}

func TestCompletedSchemaSliceGeneratedDescriptorsCoverLockedReshapes(t *testing.T) {
	assertExactOneofFields(t, (&connectionpb.AttachmentSuccess{}).ProtoReflect().Descriptor(), "attachment", "project", "session")
	assertExactOneofFields(t, (&sessionlaunchpb.SessionLaunchIntent{}).ProtoReflect().Descriptor(), "intent", "create_new", "open_existing_session_id")
	assertExactOneofFields(t, (&sessionlaunchpb.SessionDirective{}).ProtoReflect().Descriptor(), "directive", "stop", "select_session", "launch")
	assertExactFields(t, (&onboardingpb.RollbackFailedDetails{}).ProtoReflect().Descriptor(), "primary", "rollback")
	assertExactOneofFields(t, (&onboardingpb.RollbackPrimaryFailure{}).ProtoReflect().Descriptor(), "failure",
		"invalid_request",
		"config_already_exists",
		"import_unavailable",
		"import_failed",
		"config_write_failed",
		"canceled",
		"internal_failure",
	)

	handshake := (&connectionpb.HandshakeRequest{}).ProtoReflect().Descriptor()
	assertExactFields(t, handshake, "protocol_version")
	for _, omitted := range []protoreflect.Name{"client_capabilities", "capability_flags", "request_id"} {
		if handshake.Fields().ByName(omitted) != nil {
			t.Fatalf("%s unexpectedly contains projected field %s", handshake.FullName(), omitted)
		}
	}

	settings := (&sessionlaunchpb.Settings{}).ProtoReflect().Descriptor()
	for _, fieldName := range []protoreflect.Name{"enabled_tools", "skill_toggles", "subagents"} {
		field := settings.Fields().ByName(fieldName)
		if field == nil {
			t.Fatalf("%s missing retained field %s", settings.FullName(), fieldName)
		}
		if field.IsMap() || !field.IsList() || field.Message() == nil {
			t.Fatalf("%s.%s = map:%v list:%v message:%v, want repeated typed fact", settings.FullName(), fieldName, field.IsMap(), field.IsList(), field.Message())
		}
	}
	sourceReport := (&sessionlaunchpb.SourceReport{}).ProtoReflect().Descriptor()
	sources := sourceReport.Fields().ByName("sources")
	if sources == nil || sources.IsMap() || !sources.IsList() || sources.Message() == nil {
		t.Fatalf("%s.sources = %v, want repeated typed fact", sourceReport.FullName(), sources)
	}

	toolID := (&sessionlaunchpb.ToolEnabledFact{}).ProtoReflect().Descriptor().Fields().ByName("tool_id").Enum()
	assertExactEnumValues(t, toolID,
		"TOOL_ID_UNSPECIFIED",
		"TOOL_ID_EXEC_COMMAND",
		"TOOL_ID_WRITE_STDIN",
		"TOOL_ID_VIEW_IMAGE",
		"TOOL_ID_PATCH",
		"TOOL_ID_EDIT",
		"TOOL_ID_ASK_QUESTION",
		"TOOL_ID_COMPLETE_NODE",
		"TOOL_ID_TRIGGER_HANDOFF",
		"TOOL_ID_WEB_SEARCH",
	)
}

func assertExactFields(t *testing.T, message protoreflect.MessageDescriptor, names ...protoreflect.Name) {
	t.Helper()
	fields := message.Fields()
	if fields.Len() != len(names) {
		t.Fatalf("%s field count = %d, want %d", message.FullName(), fields.Len(), len(names))
	}
	for index, name := range names {
		if got := fields.Get(index).Name(); got != name {
			t.Fatalf("%s field %d = %s, want %s", message.FullName(), index, got, name)
		}
	}
}

func assertExactOneofFields(t *testing.T, message protoreflect.MessageDescriptor, oneofName protoreflect.Name, names ...protoreflect.Name) {
	t.Helper()
	oneof := message.Oneofs().ByName(oneofName)
	if oneof == nil {
		t.Fatalf("%s missing oneof %s", message.FullName(), oneofName)
	}
	fields := oneof.Fields()
	if fields.Len() != len(names) {
		t.Fatalf("%s.%s field count = %d, want %d", message.FullName(), oneofName, fields.Len(), len(names))
	}
	for index, name := range names {
		if got := fields.Get(index).Name(); got != name {
			t.Fatalf("%s.%s field %d = %s, want %s", message.FullName(), oneofName, index, got, name)
		}
	}
}

func assertExactEnumValues(t *testing.T, enum protoreflect.EnumDescriptor, names ...protoreflect.Name) {
	t.Helper()
	values := enum.Values()
	if values.Len() != len(names) {
		t.Fatalf("%s value count = %d, want %d", enum.FullName(), values.Len(), len(names))
	}
	for index, name := range names {
		if got := values.Get(index).Name(); got != name {
			t.Fatalf("%s value %d = %s, want %s", enum.FullName(), index, got, name)
		}
	}
}

func int32Pointer(value int32) *int32 {
	return &value
}
