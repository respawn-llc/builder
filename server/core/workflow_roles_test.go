package core

import (
	"slices"
	"testing"

	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/toolspec"
)

func TestConfigRoleResolverUsesConfiguredRoleIdentity(t *testing.T) {
	settings := config.Settings{
		ThinkingLevel: "medium",
		Workflow:      config.WorkflowSettings{Subagents: false},
		EnabledTools:  map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
		Subagents: map[string]config.SubagentRole{
			"planner": {
				Settings: config.Settings{ThinkingLevel: "medium"},
				Sources:  map[string]string{"thinking_level": "file"},
			},
			"blocked": {
				AgentCallable:    false,
				AgentCallableSet: true,
				Sources:          map[string]string{"agent_callable": "file"},
			},
			"workflow_hidden": {
				Settings: config.Settings{ThinkingLevel: "high"},
				Sources:  map[string]string{"thinking_level": "file"},
			},
			"role_hidden": {
				Settings:            config.Settings{ThinkingLevel: "high"},
				Sources:             map[string]string{"thinking_level": "file"},
				WorkflowSubagent:    false,
				WorkflowSubagentSet: true,
			},
		},
	}
	resolver := configRoleResolver{settings: settings}

	tests := []struct {
		name string
		role string
		want bool
	}{
		{name: "configured no-op role", role: " Planner ", want: true},
		{name: "built-in fast role", role: config.BuiltInSubagentRoleFast, want: true},
		{name: "configured non-callable role", role: "blocked", want: true},
		{name: "globally workflow-hidden role", role: "workflow_hidden", want: true},
		{name: "per-role workflow-hidden role", role: "role_hidden", want: true},
		{name: "workflow default role", role: workflow.DefaultAgentRole, want: true},
		{name: "missing valid role", role: "missing", want: false},
		{name: "reserved none role", role: "none", want: false},
		{name: "reserved self role", role: "self", want: false},
		{name: "invalid role", role: "plan/ner", want: false},
		{name: "empty role", role: " \t ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolver.RoleExists(tt.role); got != tt.want {
				t.Fatalf("RoleExists(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestWorkflowValidationUsesConfigRoleResolverIdentity(t *testing.T) {
	settings := config.Settings{
		ThinkingLevel: "medium",
		Workflow:      config.WorkflowSettings{Subagents: false},
		EnabledTools:  map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
		Subagents: map[string]config.SubagentRole{
			"planner": {
				Settings: config.Settings{ThinkingLevel: "medium"},
				Sources:  map[string]string{"thinking_level": "file"},
			},
			"role_hidden": {
				Settings:            config.Settings{ThinkingLevel: "high"},
				Sources:             map[string]string{"thinking_level": "file"},
				WorkflowSubagent:    false,
				WorkflowSubagentSet: true,
			},
		},
	}

	t.Run("configured no-op role", func(t *testing.T) {
		result := workflow.ValidateDefinition(coreWorkflowValidationDefinition("planner"), workflow.ValidationOptions{
			Context:      workflow.ValidationContextTaskCreation,
			RoleResolver: configRoleResolver{settings: settings},
		})

		assertCoreWorkflowHasNoCode(t, result, workflow.CodeAgentRoleMissing)
		if result.HasErrors() {
			t.Fatalf("configured role workflow should validate, got errors: %+v", result.Errors)
		}
	})

	t.Run("workflow-hidden configured role", func(t *testing.T) {
		result := workflow.ValidateDefinition(coreWorkflowValidationDefinition("role_hidden"), workflow.ValidationOptions{
			Context:      workflow.ValidationContextTaskCreation,
			RoleResolver: configRoleResolver{settings: settings},
		})
		assertCoreWorkflowHasNoCode(t, result, workflow.CodeAgentRoleMissing)
		if result.HasErrors() {
			t.Fatalf("workflow-hidden role workflow should validate, got errors: %+v", result.Errors)
		}
	})

	for _, role := range []string{"missing", "plan/ner"} {
		t.Run(role, func(t *testing.T) {
			result := workflow.ValidateDefinition(coreWorkflowValidationDefinition(role), workflow.ValidationOptions{
				Context:      workflow.ValidationContextTaskCreation,
				RoleResolver: configRoleResolver{settings: settings},
			})

			assertCoreWorkflowHasCode(t, result, workflow.CodeAgentRoleMissing)
		})
	}
}

func TestWorkflowValidationRejectsDefaultRoleWithoutAskQuestion(t *testing.T) {
	def := coreWorkflowValidationDefinition(workflow.DefaultAgentRole)
	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context: workflow.ValidationContextExecution,
		RoleResolver: configRoleResolver{settings: config.Settings{
			EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: false},
		}},
	})

	var diagnostics []workflow.ValidationError
	for _, diagnostic := range result.Errors {
		if diagnostic.Code == workflow.CodeAgentRoleRequiredToolDisabled {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	if len(diagnostics) != 1 {
		t.Fatalf("required-tool diagnostics = %+v, want exactly one", diagnostics)
	}
	diagnostic := diagnostics[0]
	if diagnostic.NodeID != workflow.NodeIDOf(def.Nodes[1]) {
		t.Fatalf("node id = %q, want %q", diagnostic.NodeID, workflow.NodeIDOf(def.Nodes[1]))
	}
	if diagnostic.AgentRole == nil || *diagnostic.AgentRole != workflow.DefaultAgentRole {
		t.Fatalf("agent role = %v, want %q", diagnostic.AgentRole, workflow.DefaultAgentRole)
	}
	if diagnostic.RequiredTool == nil || *diagnostic.RequiredTool != toolspec.ToolAskQuestion {
		t.Fatalf("required tool = %v, want %q", diagnostic.RequiredTool, toolspec.ToolAskQuestion)
	}
	if !diagnostic.BlocksContext {
		t.Fatal("execution-context diagnostic must block")
	}
}

func TestWorkflowValidationRejectsConfiguredRoleDisablingAskQuestion(t *testing.T) {
	settings := config.Settings{
		EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
		Subagents: map[string]config.SubagentRole{
			"planner": {
				Settings: config.Settings{EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: false}},
				Sources:  map[string]string{"tools.ask_question": "file"},
			},
		},
	}
	result := workflow.ValidateDefinition(coreWorkflowValidationDefinition("planner"), workflow.ValidationOptions{
		Context:      workflow.ValidationContextExecution,
		RoleResolver: configRoleResolver{settings: settings},
	})

	assertCoreWorkflowHasCode(t, result, workflow.CodeAgentRoleRequiredToolDisabled)
}

func TestWorkflowValidationAcceptsConfiguredRoleReenablingAskQuestion(t *testing.T) {
	settings := config.Settings{
		EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: false},
		Subagents: map[string]config.SubagentRole{
			"planner": {
				Settings: config.Settings{EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}},
				Sources:  map[string]string{"tools.ask_question": "file"},
			},
		},
	}
	result := workflow.ValidateDefinition(coreWorkflowValidationDefinition("planner"), workflow.ValidationOptions{
		Context:      workflow.ValidationContextExecution,
		RoleResolver: configRoleResolver{settings: settings},
	})

	assertCoreWorkflowHasNoCode(t, result, workflow.CodeAgentRoleRequiredToolDisabled)
}

func TestWorkflowValidationRejectsBuiltInRoleDisablingAskQuestion(t *testing.T) {
	settings := config.Settings{
		EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
		Subagents: map[string]config.SubagentRole{
			config.BuiltInSubagentRoleFast: {
				Settings: config.Settings{EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: false}},
				Sources:  map[string]string{"tools.ask_question": "file"},
			},
		},
	}
	result := workflow.ValidateDefinition(coreWorkflowValidationDefinition(config.BuiltInSubagentRoleFast), workflow.ValidationOptions{
		Context:      workflow.ValidationContextExecution,
		RoleResolver: configRoleResolver{settings: settings},
	})

	assertCoreWorkflowHasCode(t, result, workflow.CodeAgentRoleRequiredToolDisabled)
}

func coreWorkflowValidationDefinition(role string) workflow.Definition {
	workflowID := coreWorkflowValidationWorkflowID()
	const (
		startNodeID       workflow.NodeID            = "0190f1aa-0000-7000-8000-000000000002"
		agentNodeID       workflow.NodeID            = "0190f1aa-0000-7000-8000-000000000003"
		doneNodeID        workflow.NodeID            = "0190f1aa-0000-7000-8000-000000000004"
		startGroupID      workflow.TransitionGroupID = "0190f1aa-0000-7000-8000-000000000005"
		doneGroupID       workflow.TransitionGroupID = "0190f1aa-0000-7000-8000-000000000006"
		startTransitionID workflow.TransitionID      = "start"
		doneTransitionID  workflow.TransitionID      = "done"
		startEdgeID       workflow.EdgeID            = "0190f1aa-0000-7000-8000-000000000009"
		doneEdgeID        workflow.EdgeID            = "0190f1aa-0000-7000-8000-00000000000a"
	)
	return workflow.Definition{
		ID:          workflowID,
		DisplayName: "Config Roles",
		Nodes: []workflow.Node{
			coreWorkflowNode(workflowID, startNodeID, "start", "Start", workflow.NodeKindStart, workflow.NodeFields{}),
			coreWorkflowNode(workflowID, agentNodeID, "agent", "Agent", workflow.NodeKindAgent, workflow.NodeFields{
				SubagentRole: role,
			}),
			coreWorkflowNode(workflowID, doneNodeID, "done", "Done", workflow.NodeKindTerminal, workflow.NodeFields{}),
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: workflowID, ID: startGroupID, SourceNodeID: startNodeID, TransitionID: startTransitionID, DisplayName: "Start"},
			{WorkflowID: workflowID, ID: doneGroupID, SourceNodeID: agentNodeID, TransitionID: doneTransitionID, DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: workflowID, ID: startEdgeID, Key: "start", TransitionGroupID: startGroupID, TargetNodeID: agentNodeID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."},
			{
				WorkflowID:         workflowID,
				ID:                 doneEdgeID,
				Key:                "done",
				TransitionGroupID:  doneGroupID,
				TargetNodeID:       doneNodeID,
				ContextMode:        workflow.ContextModeNewSession,
				Parameters:         []workflow.Parameter{{Key: "summary", Description: "Summary of completed work."}},
				OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}},
			},
		},
	}
}

func coreWorkflowValidationWorkflowID() runtimeids.WorkflowID {
	workflowID, err := runtimeids.ParseWorkflowID("0190f1aa-0000-4000-8000-000000000001")
	if err != nil {
		panic(err)
	}
	return workflowID
}

func coreWorkflowNode(workflowID runtimeids.WorkflowID, id workflow.NodeID, key workflow.ModelKey, displayName string, kind workflow.NodeKind, fields workflow.NodeFields) workflow.Node {
	node, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  workflowID,
		ID:          id,
		Key:         key,
		DisplayName: displayName,
	}, kind, fields)
	if err != nil {
		panic(err)
	}
	return node
}

func assertCoreWorkflowHasCode(t *testing.T, result workflow.ValidationResult, want workflow.ValidationErrorCode) {
	t.Helper()
	if !slices.Contains(result.Codes(), want) {
		t.Fatalf("missing validation code %q in %v; errors: %+v", want, result.Codes(), result.Errors)
	}
}

func assertCoreWorkflowHasNoCode(t *testing.T, result workflow.ValidationResult, code workflow.ValidationErrorCode) {
	t.Helper()
	if slices.Contains(result.Codes(), code) {
		t.Fatalf("unexpected validation code %q in %v; errors: %+v", code, result.Codes(), result.Errors)
	}
}
