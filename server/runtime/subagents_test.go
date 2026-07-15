package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestSubagentsMetaMessageRendersCallableNonNoopRoles(t *testing.T) {
	settings := config.Settings{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: "medium",
		EnabledTools: map[toolspec.ID]bool{
			toolspec.ToolExecCommand: true,
			toolspec.ToolPatch:       true,
		},
		Subagents: map[string]config.SubagentRole{
			"research": {
				Settings: config.Settings{
					Model:         "gpt-5.4-mini",
					ThinkingLevel: "high",
					EnabledTools: map[toolspec.ID]bool{
						toolspec.ToolExecCommand: true,
						toolspec.ToolPatch:       false,
					},
				},
				Sources:     map[string]string{"model": "file", "thinking_level": "file", "tools.patch": "file"},
				Description: "Repo research specialist.",
			},
			"placebo": {
				Settings:    config.Settings{Model: "gpt-5.6-sol"},
				Sources:     map[string]string{"model": "file"},
				Description: "Sounds useful, but no behavior differs.",
			},
			"blocked": {
				Settings:         config.Settings{Model: "gpt-5.4-mini"},
				Sources:          map[string]string{"model": "file"},
				AgentCallable:    false,
				AgentCallableSet: true,
			},
			config.BuiltInSubagentRoleFast: {
				Description: "ignored for now",
			},
		},
	}
	builder := newMetaContextBuilder("/tmp/work", "gpt-5.6-sol", "medium", nil, time.Unix(0, 0)).
		withSubagents(settings, []toolspec.ID{toolspec.ToolExecCommand})
	result, err := builder.Build(metaContextBuildOptions{
		IncludeSubagents:          true,
		SubagentInvocationContext: config.SubagentInvocationContextOrdinary,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Subagents) != 1 {
		t.Fatalf("subagent messages = %d, want 1", len(result.Subagents))
	}
	content := result.Subagents[0].Content
	for _, want := range []string{
		"Available subagent roles:",
		"- `default`: not specifying any role will invoke the default general-purpose agent",
		"- `research`: Repo research specialist.",
		"Invoke with `",
		"--agent=<role> \"<prompt>\"`.",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"placebo", "blocked", "fast:"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("content should not include %q:\n%s", unwanted, content)
		}
	}
}

func TestSubagentsMetaMessageUsesFallbackAndRequiresCallerShell(t *testing.T) {
	settings := config.Settings{
		Model:               "gpt-5.6-sol",
		ThinkingLevel:       "medium",
		PriorityRequestMode: true,
		EnabledTools: map[toolspec.ID]bool{
			toolspec.ToolExecCommand: true,
			toolspec.ToolPatch:       false,
		},
		Subagents: map[string]config.SubagentRole{
			"worker": {
				Settings: config.Settings{
					Model:               "gpt-5.4-mini",
					ThinkingLevel:       "high",
					PriorityRequestMode: true,
					EnabledTools: map[toolspec.ID]bool{
						toolspec.ToolExecCommand: true,
						toolspec.ToolPatch:       true,
					},
				},
				Sources: map[string]string{"model": "file", "thinking_level": "file", "priority_request_mode": "file", "tools.patch": "file"},
			},
		},
	}
	withShell := newMetaContextBuilder("/tmp/work", "gpt-5.6-sol", "medium", nil, time.Unix(0, 0)).
		withSubagents(settings, []toolspec.ID{toolspec.ToolExecCommand})
	result, err := withShell.Build(metaContextBuildOptions{
		IncludeSubagents:          true,
		SubagentInvocationContext: config.SubagentInvocationContextOrdinary,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Subagents) != 1 || !strings.Contains(result.Subagents[0].Content, "- `worker`: gpt-5.4-mini, thinking high, fast mode on, can edit, can call shell") {
		t.Fatalf("unexpected fallback content: %+v", result.Subagents)
	}

	withoutShell := newMetaContextBuilder("/tmp/work", "gpt-5.6-sol", "medium", nil, time.Unix(0, 0)).
		withSubagents(settings, []toolspec.ID{toolspec.ToolPatch})
	result, err = withoutShell.Build(metaContextBuildOptions{
		IncludeSubagents:          true,
		SubagentInvocationContext: config.SubagentInvocationContextOrdinary,
	})
	if err != nil {
		t.Fatalf("Build without shell: %v", err)
	}
	if len(result.Subagents) != 0 {
		t.Fatalf("expected no subagent context without caller shell, got %+v", result.Subagents)
	}
}

func TestSubagentsMetaMessageCurrentNonCallableRoleDoesNotDisableOtherRoles(t *testing.T) {
	settings := config.Settings{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: "medium",
		EnabledTools: map[toolspec.ID]bool{
			toolspec.ToolExecCommand: true,
		},
		Subagents: map[string]config.SubagentRole{
			"current": {
				Settings:         config.Settings{Model: "gpt-5.4-mini"},
				Sources:          map[string]string{"model": "file"},
				AgentCallable:    false,
				AgentCallableSet: true,
			},
			"worker": {
				Settings:    config.Settings{ThinkingLevel: "high"},
				Sources:     map[string]string{"thinking_level": "file"},
				Description: "Callable helper.",
			},
		},
	}
	builder := newMetaContextBuilder("/tmp/work", "gpt-5.6-sol", "medium", nil, time.Unix(0, 0)).
		withSubagents(settings, []toolspec.ID{toolspec.ToolExecCommand})
	result, err := builder.Build(metaContextBuildOptions{
		IncludeSubagents:          true,
		SubagentInvocationContext: config.SubagentInvocationContextOrdinary,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Subagents) != 1 {
		t.Fatalf("subagent messages = %d, want 1", len(result.Subagents))
	}
	roles := builder.renderableSubagentRoles(config.SubagentInvocationContextOrdinary)
	if !renderableSubagentRolesContain(roles, "worker") {
		t.Fatalf("renderable roles = %+v, want worker", roles)
	}
	if renderableSubagentRolesContain(roles, "current") {
		t.Fatalf("renderable roles = %+v, want current omitted", roles)
	}
}

func TestSubagentCatalogAppliesInvocationContextPolicy(t *testing.T) {
	baseSettings := func(globalEnabled bool, roleDisabled bool) config.Settings {
		return config.Settings{
			Model:         "gpt-5.5",
			ThinkingLevel: "medium",
			Workflow:      config.WorkflowSettings{Subagents: globalEnabled},
			Subagents: map[string]config.SubagentRole{
				"worker": {
					Settings:            config.Settings{ThinkingLevel: "high"},
					Sources:             map[string]string{"thinking_level": "file"},
					Description:         "Worker.",
					WorkflowSubagent:    !roleDisabled,
					WorkflowSubagentSet: roleDisabled,
				},
				"blocked": {
					Settings:         config.Settings{ThinkingLevel: "high"},
					Sources:          map[string]string{"thinking_level": "file"},
					Description:      "Blocked.",
					AgentCallableSet: true,
				},
				config.BuiltInSubagentRoleFast: {Description: "Fast."},
			},
		}
	}
	tests := []struct {
		name          string
		context       config.SubagentInvocationContext
		settings      config.Settings
		workerVisible bool
	}{
		{name: "ordinary ignores disabled workflow policy", context: config.SubagentInvocationContextOrdinary, settings: baseSettings(false, true), workerVisible: true},
		{name: "workflow defaults to disabled", context: config.SubagentInvocationContextWorkflow, settings: baseSettings(false, false)},
		{name: "workflow global enablement restores custom role", context: config.SubagentInvocationContextWorkflow, settings: baseSettings(true, false), workerVisible: true},
		{name: "workflow per role false suppresses custom role", context: config.SubagentInvocationContextWorkflow, settings: baseSettings(true, true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := newMetaContextBuilder("/tmp/work", "gpt-5.5", "medium", nil, time.Unix(0, 0)).
				withSubagents(tt.settings, []toolspec.ID{toolspec.ToolExecCommand})
			result, err := builder.Build(metaContextBuildOptions{
				IncludeSubagents:          true,
				SubagentInvocationContext: tt.context,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got := renderableSubagentRolesContain(builder.renderableSubagentRoles(tt.context), "worker"); got != tt.workerVisible {
				t.Fatalf("worker visibility = %t, want %t; messages=%+v", got, tt.workerVisible, result.Subagents)
			}
			if renderableSubagentRolesContain(builder.renderableSubagentRoles(tt.context), "blocked") {
				t.Fatalf("agent_callable=false role was rendered")
			}
			if renderableSubagentRolesContain(builder.renderableSubagentRoles(tt.context), config.BuiltInSubagentRoleFast) {
				t.Fatalf("fast should remain absent from the custom role catalog")
			}
		})
	}
}

func TestSubagentCatalogUsesSamePolicyOnBaseInjectionAndCompaction(t *testing.T) {
	settings := func(globalEnabled bool, roleDisabled bool) config.Settings {
		return config.Settings{
			Model:         "gpt-5.5",
			ThinkingLevel: "medium",
			Workflow:      config.WorkflowSettings{Subagents: globalEnabled},
			Subagents: map[string]config.SubagentRole{
				"worker": {
					Settings:            config.Settings{ThinkingLevel: "high"},
					Sources:             map[string]string{"thinking_level": "file"},
					Description:         "Worker.",
					WorkflowSubagent:    !roleDisabled,
					WorkflowSubagentSet: roleDisabled,
				},
			},
		}
	}
	tests := []struct {
		name          string
		workflow      bool
		settings      config.Settings
		workerVisible bool
	}{
		{name: "ordinary", settings: settings(false, false), workerVisible: true},
		{name: "workflow default disabled", workflow: true, settings: settings(false, false)},
		{name: "workflow globally enabled", workflow: true, settings: settings(true, false), workerVisible: true},
		{name: "workflow per role disabled", workflow: true, settings: settings(true, true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			cfg := Config{
				Model:                   "gpt-5.5",
				ThinkingLevel:           "medium",
				EnabledTools:            []toolspec.ID{toolspec.ToolExecCommand},
				SubagentCatalogSettings: tt.settings,
			}
			if tt.workflow {
				cfg.WorkflowRun = testWorkflowConfig(nil, config.WorkflowCompletionModeTool)
			}
			eng := mustNewExecTestEngine(t, store, &fakeClient{}, cfg)
			if err := eng.steerBaseMetaContextIfNeeded("base"); err != nil {
				t.Fatalf("steer base meta context: %v", err)
			}
			if got := hasSubagentMetaMessage(eng.transcriptRuntimeState().SnapshotMessages()); got != tt.workerVisible {
				t.Fatalf("base worker visibility = %t, want %t", got, tt.workerVisible)
			}
			compacted, err := eng.compactionReinjectedMetaMessages(context.Background())
			if err != nil {
				t.Fatalf("compaction reinjection: %v", err)
			}
			if got := hasSubagentMetaMessage(compacted); got != tt.workerVisible {
				t.Fatalf("compaction worker visibility = %t, want %t", got, tt.workerVisible)
			}
		})
	}
}

func TestCompactionReinjectsSubagentsMetaContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	store := mustCreateNamedTestSession(t, "ws", workspace)
	settings := config.Settings{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: "medium",
		EnabledTools: map[toolspec.ID]bool{
			toolspec.ToolExecCommand: true,
		},
		Subagents: map[string]config.SubagentRole{
			"worker": {
				Settings:    config.Settings{ThinkingLevel: "high"},
				Sources:     map[string]string{"thinking_level": "file"},
				Description: "Callable helper.",
			},
		},
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                   "gpt-5.6-sol",
		ThinkingLevel:           "medium",
		EnabledTools:            []toolspec.ID{toolspec.ToolExecCommand},
		SubagentCatalogSettings: settings,
	})

	messages, err := eng.compactionReinjectedMetaMessages(context.Background())
	if err != nil {
		t.Fatalf("compactionReinjectedMetaMessages: %v", err)
	}
	if !hasSubagentMetaMessage(messages) {
		t.Fatalf("expected compaction-reinjected subagent catalog, got %+v", messages)
	}
}

func TestManualCompactionPersistsSubagentCatalogInCanonicalTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	store := mustCreateNamedTestSession(t, "ws", workspace)
	settings := config.Settings{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: "medium",
		EnabledTools: map[toolspec.ID]bool{
			toolspec.ToolExecCommand: true,
		},
		Subagents: map[string]config.SubagentRole{
			"worker": {
				Settings:    config.Settings{ThinkingLevel: "high"},
				Sources:     map[string]string{"thinking_level": "file"},
				Description: "Callable helper.",
			},
		},
	}
	cfg := Config{
		Model:                   "gpt-5.6-sol",
		ThinkingLevel:           "medium",
		CompactionMode:          "local",
		EnabledTools:            []toolspec.ID{toolspec.ToolExecCommand},
		SubagentCatalogSettings: settings,
	}
	client := &fakeCompactionClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), cfg)
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !hasSubagentMetaMessage(eng.transcriptRuntimeState().SnapshotMessages()) {
		t.Fatalf("expected in-memory canonical transcript to keep subagent catalog, got %+v", eng.transcriptRuntimeState().SnapshotMessages())
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), cfg)
	if !hasSubagentMetaMessage(restored.transcriptRuntimeState().SnapshotMessages()) {
		t.Fatalf("expected persisted canonical transcript to keep subagent catalog, got %+v", restored.transcriptRuntimeState().SnapshotMessages())
	}
}

func TestSplitMetaContextMessagesTreatsSubagentsAsMeta(t *testing.T) {
	subagents := llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeSubagents, Content: "Available subagent roles:"}
	messages := []llm.Message{
		subagents,
		{Role: llm.RoleUser, Content: "request"},
	}
	meta, transcript := splitMetaContextMessages(messages)
	if len(meta) != 1 || meta[0].MessageType != llm.MessageTypeSubagents {
		t.Fatalf("expected subagents meta message, got %+v", meta)
	}
	if len(transcript) != 1 || transcript[0].Role != llm.RoleUser {
		t.Fatalf("expected user transcript, got %+v", transcript)
	}
}

func TestSubagentsMetaContextVisibilityIsDetailOnly(t *testing.T) {
	entry, ok := visibleDeveloperChatEntry(llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeSubagents,
		Content:     "Available subagent roles:",
	})

	if !ok {
		t.Fatal("subagents meta context did not project to transcript entry")
	}
	if entry.Visibility != transcript.EntryVisibilityDetail {
		t.Fatalf("subagents visibility = %q, want %q", entry.Visibility, transcript.EntryVisibilityDetail)
	}
}

func hasSubagentMetaMessage(messages []llm.Message) bool {
	for _, message := range messages {
		if message.MessageType == llm.MessageTypeSubagents {
			return true
		}
	}
	return false
}

func renderableSubagentRolesContain(roles []renderedSubagentRole, name string) bool {
	for _, role := range roles {
		if role.Name == name {
			return true
		}
	}
	return false
}

func TestReviewerPromptFiltersSubagentsMetaContext(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeSubagents, Content: "Available subagent roles:\n- worker: specialist"},
		{Role: llm.RoleUser, Content: "request"},
	}
	got, err := buildReviewerRequestMessagesWithBuilder(messages, newMetaContextBuilder(t.TempDir(), "gpt-5.6-sol", "medium", nil, time.Unix(0, 0)), false)
	if err != nil {
		t.Fatalf("buildReviewerRequestMessagesWithBuilder: %v", err)
	}
	for _, message := range got {
		if message.MessageType == llm.MessageTypeSubagents || strings.Contains(message.Content, "Available subagent roles") {
			t.Fatalf("reviewer messages leaked subagent context: %+v", got)
		}
	}
}
