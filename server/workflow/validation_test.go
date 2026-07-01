package workflow_test

import (
	"slices"
	"testing"

	"core/server/workflow"
)

func TestValidateDefaultWorkflowPasses(t *testing.T) {
	def := validWorkflow()

	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextTaskCreation,
		RoleResolver: workflow.StaticRoleResolver{"coder": true},
	})

	if result.HasErrors() {
		t.Fatalf("expected valid workflow, got errors: %+v", result.Errors)
	}
	if result.HasBlockingErrors() {
		t.Fatalf("expected result.Valid()")
	}
}

func TestCanonicalContextSourceNormalizesBoundaryValues(t *testing.T) {
	selected := workflow.CanonicalContextSource(workflow.ContextSource{Kind: " selected_node ", NodeKey: " implementation "})
	if selected.Kind != workflow.ContextSourceSelectedNode || selected.NodeKey != "implementation" {
		t.Fatalf("selected context source = %+v, want trimmed selected node", selected)
	}

	immediate := workflow.CanonicalContextSource(workflow.ContextSource{Kind: " immediate_source ", NodeKey: " implementation "})
	if immediate.Kind != workflow.ContextSourceImmediateSource || immediate.NodeKey != "" {
		t.Fatalf("immediate context source = %+v, want normalized immediate source", immediate)
	}
}

func TestValidateWorkflowAllowsDefaultAgentRole(t *testing.T) {
	def := validWorkflow()
	def.Nodes[1].SubagentRole = workflow.DefaultAgentRole

	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextTaskCreation,
		RoleResolver: workflow.StaticRoleResolver{"coder": true},
	})

	if result.HasErrors() {
		t.Fatalf("expected default role to be valid, got errors: %+v", result.Errors)
	}
}

func TestDraftValidationAllowsSemanticErrorsButBlocksHardStorageErrors(t *testing.T) {
	def := validWorkflow()
	def.Nodes = append(def.Nodes, workflow.Node{
		WorkflowID:     def.ID,
		ID:             "node_detached",
		Key:            "detached",
		DisplayName:    "Detached",
		Kind:           workflow.NodeKindAgent,
		SubagentRole:   "missing",
		PromptTemplate: "Do detached work.",
	})

	draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft})

	assertHasCodes(t, draft, workflow.CodeNodeUnreachableFromStart, workflow.CodeNonTerminalCannotReachTerminal, workflow.CodeAgentRoleMissing)
	if draft.HasBlockingErrors() {
		t.Fatalf("draft semantic errors should not block saving: %+v", draft.BlockingErrors())
	}

	def.Nodes[0].Kind = workflow.NodeKindAgent
	def.Nodes[1].Kind = workflow.NodeKindStart
	def.Nodes[2].Kind = workflow.NodeKindStart

	hard := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft})

	assertHasCodes(t, hard, workflow.CodeMultipleStartNodes)
	if !hard.HasBlockingErrors() {
		t.Fatalf("draft hard storage error should block saving")
	}
}

func TestTaskCreationValidationRejectsInvalidGraphWithAccumulatedErrors(t *testing.T) {
	def := validWorkflow()
	def.Nodes = append(def.Nodes, workflow.Node{
		WorkflowID:     def.ID,
		ID:             "node_detached",
		Key:            "detached",
		DisplayName:    "Detached",
		Kind:           workflow.NodeKindAgent,
		SubagentRole:   "missing",
		PromptTemplate: "Do detached work.",
	})
	def.TransitionGroups = nil
	def.Edges = nil

	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextTaskCreation})

	assertHasCodes(t, result,
		workflow.CodeInvalidStartOutgoingShape,
		workflow.CodeNodeUnreachableFromStart,
		workflow.CodeNonTerminalCannotReachTerminal,
		workflow.CodeAgentRoleMissing,
	)
	if !result.HasBlockingErrors() {
		t.Fatalf("task creation errors should block")
	}
}

func TestStartNodeRules(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workflow.Definition)
		code workflow.ValidationErrorCode
	}{
		{
			name: "missing start",
			edit: func(def *workflow.Definition) {
				def.Nodes[0].Kind = workflow.NodeKindAgent
				def.Nodes[0].SubagentRole = "coder"
				def.Nodes[0].PromptTemplate = "Work."
			},
			code: workflow.CodeMissingStartNode,
		},
		{
			name: "multiple starts",
			edit: func(def *workflow.Definition) {
				def.Nodes[1].Kind = workflow.NodeKindStart
			},
			code: workflow.CodeMultipleStartNodes,
		},
		{
			name: "start execution config",
			edit: func(def *workflow.Definition) {
				def.Nodes[0].SubagentRole = "coder"
				def.Nodes[0].PromptTemplate = "Do work."
				def.Nodes[0].OutputFields = []workflow.OutputField{{Name: "summary", Description: "Summary."}}
			},
			code: workflow.CodeInvalidStartNode,
		},
		{
			name: "start incoming edge",
			edit: func(def *workflow.Definition) {
				addTransitionForValidationTest(def, "group_restart", "node_agent", "restart", "Restart", "edge_restart", "restart", "node_start")
			},
			code: workflow.CodeInvalidStartNode,
		},
		{
			name: "start has two groups",
			edit: func(def *workflow.Definition) {
				addTransitionForValidationTest(def, "group_alt", "node_start", "alt", "Alternative", "edge_alt", "alt", "node_agent")
			},
			code: workflow.CodeInvalidStartOutgoingShape,
		},
		{
			name: "start group has two edges",
			edit: func(def *workflow.Definition) {
				def.Edges = append(def.Edges, workflow.Edge{
					WorkflowID:        def.ID,
					ID:                "edge_start_second",
					Key:               "second",
					TransitionGroupID: "group_start",
					TargetNodeID:      "node_agent",
					ContextMode:       workflow.ContextModeNewSession,
				})
			},
			code: workflow.CodeInvalidStartOutgoingShape,
		},
		{
			name: "start targets terminal",
			edit: func(def *workflow.Definition) {
				def.Edges[0].TargetNodeID = "node_done"
			},
			code: workflow.CodeInvalidStartOutgoingShape,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validWorkflow()
			tt.edit(&def)

			result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
				Context:      workflow.ValidationContextTaskCreation,
				RoleResolver: workflow.StaticRoleResolver{"coder": true},
			})

			assertHasCodes(t, result, tt.code)
		})
	}
}

func TestNodeKindRules(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workflow.Definition)
		code workflow.ValidationErrorCode
	}{
		{
			name: "terminal outgoing edge",
			edit: func(def *workflow.Definition) {
				addTransitionForValidationTest(def, "group_done_again", "node_done", "again", "Again", "edge_done_again", "again", "node_agent")
			},
			code: workflow.CodeTerminalHasOutgoingEdge,
		},
		{
			name: "terminal executable",
			edit: func(def *workflow.Definition) {
				def.Nodes[2].SubagentRole = "coder"
				def.Nodes[2].PromptTemplate = "No."
			},
			code: workflow.CodeTerminalIsExecutable,
		},
		{
			name: "join executable",
			edit: func(def *workflow.Definition) {
				def.Nodes[1].Kind = workflow.NodeKindJoin
				def.Nodes[1].SubagentRole = "coder"
				def.Nodes[1].PromptTemplate = "No."
			},
			code: workflow.CodeJoinIsExecutable,
		},
		{
			name: "join also terminal",
			edit: func(def *workflow.Definition) {
				def.Nodes[2].Kind = workflow.NodeKindJoin
			},
			code: workflow.CodeInvalidJoinNode,
		},
		{
			name: "join invalid outgoing shape",
			edit: func(def *workflow.Definition) {
				def.Nodes[1].Kind = workflow.NodeKindJoin
				def.Nodes[1].SubagentRole = ""
				def.Nodes[1].PromptTemplate = ""
				addTransitionForValidationTest(def, "group_join_alt", "node_agent", "alt", "Alternative", "edge_join_alt", "alt", "node_done")
			},
			code: workflow.CodeInvalidJoinOutgoingShape,
		},
		{
			name: "unknown kind",
			edit: func(def *workflow.Definition) {
				def.Nodes[1].Kind = workflow.NodeKind("robot")
			},
			code: workflow.CodeInvalidNodeKind,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validWorkflow()
			tt.edit(&def)

			result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
				Context:      workflow.ValidationContextTaskCreation,
				RoleResolver: workflow.StaticRoleResolver{"coder": true},
			})

			assertHasCodes(t, result, tt.code)
		})
	}

	t.Run("legacy node contract fields are inert metadata", func(t *testing.T) {
		def := validWorkflow()
		def.Nodes[0].PromptTemplate = "{{.Inputs.legacy_start}}"
		def.Nodes[0].InputFields = []workflow.InputField{{Name: "Bad Field", Description: " "}}
		def.Nodes[1].PromptTemplate = "{{.Nodes.deleted.summary}}"
		def.Nodes[1].InputFields = []workflow.InputField{{Name: "Bad Field", Description: " "}}
		def.Nodes[1].OutputFields = []workflow.OutputField{{Name: "transition_id", Description: " "}}
		def.Nodes[2].PromptTemplate = "{{.Params.missing}}"
		def.Nodes[2].InputFields = []workflow.InputField{{Name: "Bad Field", Description: " "}}

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidInputField)
		assertNoCode(t, result, workflow.CodeDuplicateInputField)
		assertNoCode(t, result, workflow.CodeInputFieldDescriptionRequired)
		assertNoCode(t, result, workflow.CodeInputSchemaTooLarge)
		assertNoCode(t, result, workflow.CodeInvalidOutputField)
		assertNoCode(t, result, workflow.CodeDuplicateOutputField)
		assertNoCode(t, result, workflow.CodeOutputFieldDescriptionRequired)
		assertNoCode(t, result, workflow.CodeOutputSchemaTooLarge)
		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})
}

func TestGraphReachabilityAndCycles(t *testing.T) {
	t.Run("detached island rejected", func(t *testing.T) {
		def := validWorkflow()
		def.Nodes = append(def.Nodes, workflow.Node{
			WorkflowID:     def.ID,
			ID:             "node_island",
			Key:            "island",
			DisplayName:    "Island",
			Kind:           workflow.NodeKindAgent,
			SubagentRole:   "coder",
			PromptTemplate: "Island.",
		})

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeNodeUnreachableFromStart, workflow.CodeNonTerminalCannotReachTerminal)
	})

	t.Run("cycle allowed when terminal reachable", func(t *testing.T) {
		def := validWorkflow()
		addAgentLoop(&def, "node_agent", "loop", "edge_loop", "loop")

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeNonTerminalCannotReachTerminal)
		assertNoCode(t, result, workflow.CodeInvalidFanoutJoinTopology)
	})

	t.Run("self loop allowed when terminal reachable", func(t *testing.T) {
		def := validWorkflow()
		addAgentLoop(&def, "node_agent", "self", "edge_self", "self")

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeNonTerminalCannotReachTerminal)
		assertNoCode(t, result, workflow.CodeInvalidFanoutJoinTopology)
	})
}

func TestValidationMessagesIncludeNodeDisplayName(t *testing.T) {
	t.Run("parameters identify the transition branch and ordinal", func(t *testing.T) {
		def := validWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_done")
		edge.Key = "complete"
		edge.Parameters = []workflow.Parameter{
			{Key: "Bad Field", Description: "Field with invalid identifier."},
			{Key: "missing_description", Description: " "},
			{Key: "long_description", Description: stringOf("a", workflow.MaxParameterDescriptionChars+1)},
		}

		result := validateForTask(def)

		assertValidationMessageOnEdge(t, result, workflow.CodeInvalidParameter, "edge_done", "Transition branch complete: parameter #1 key is invalid")
		assertValidationMessageOnEdge(t, result, workflow.CodeParameterDescriptionRequired, "edge_done", "Transition branch complete: parameter #2 description is required")
		assertValidationMessageOnEdge(t, result, workflow.CodeParameterSchemaTooLarge, "edge_done", "Transition branch complete: parameter #3 description is too large")
	})

	t.Run("reachability identifies the node", func(t *testing.T) {
		def := validWorkflow()
		def.Nodes = append(def.Nodes, workflow.Node{
			WorkflowID:     def.ID,
			ID:             "node_planning_recon",
			Key:            "planning_recon",
			DisplayName:    "Planning Recon",
			Kind:           workflow.NodeKindAgent,
			SubagentRole:   "coder",
			PromptTemplate: "Plan the work.",
		})

		result := validateForTask(def)

		assertValidationMessage(t, result, workflow.CodeNodeUnreachableFromStart, "node_planning_recon", "Node Planning Recon not reachable")
		assertValidationMessage(t, result, workflow.CodeNonTerminalCannotReachTerminal, "node_planning_recon", "Node Planning Recon cannot reach a terminal")
	})
}

func TestIdentifierAndReferenceRules(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workflow.Definition)
		code workflow.ValidationErrorCode
	}{
		{name: "missing workflow id", edit: func(def *workflow.Definition) { def.ID = "" }, code: workflow.CodeMissingWorkflowID},
		{name: "invalid workflow display name", edit: func(def *workflow.Definition) { def.DisplayName = " " }, code: workflow.CodeInvalidDisplayName},
		{name: "missing node id", edit: func(def *workflow.Definition) { def.Nodes[1].ID = "" }, code: workflow.CodeMissingNodeID},
		{name: "duplicate node id", edit: func(def *workflow.Definition) { def.Nodes[1].ID = def.Nodes[0].ID }, code: workflow.CodeDuplicateNodeID},
		{name: "missing node key", edit: func(def *workflow.Definition) { def.Nodes[1].Key = "" }, code: workflow.CodeMissingNodeKey},
		{name: "invalid node key", edit: func(def *workflow.Definition) { def.Nodes[1].Key = "Bad-Key" }, code: workflow.CodeInvalidNodeKey},
		{name: "duplicate node key", edit: func(def *workflow.Definition) { def.Nodes[1].Key = def.Nodes[0].Key }, code: workflow.CodeDuplicateNodeKey},
		{name: "invalid node display name", edit: func(def *workflow.Definition) {
			def.Nodes[1].DisplayName = stringOf("a", workflow.MaxDisplayNameChars+1)
		}, code: workflow.CodeInvalidDisplayName},
		{name: "missing transition group id", edit: func(def *workflow.Definition) { def.TransitionGroups[0].ID = "" }, code: workflow.CodeMissingTransitionGroupID},
		{name: "duplicate transition group id", edit: func(def *workflow.Definition) { def.TransitionGroups[1].ID = def.TransitionGroups[0].ID }, code: workflow.CodeDuplicateTransitionGroupID},
		{name: "empty transition group", edit: func(def *workflow.Definition) { def.Edges = def.Edges[1:] }, code: workflow.CodeEmptyTransitionGroup},
		{name: "missing transition id", edit: func(def *workflow.Definition) { def.TransitionGroups[1].TransitionID = "" }, code: workflow.CodeMissingTransitionID},
		{name: "invalid transition id", edit: func(def *workflow.Definition) { def.TransitionGroups[1].TransitionID = "Done!" }, code: workflow.CodeInvalidTransitionID},
		{name: "invalid transition group display name", edit: func(def *workflow.Definition) { def.TransitionGroups[1].DisplayName = "" }, code: workflow.CodeInvalidDisplayName},
		{name: "duplicate transition id per source", edit: func(def *workflow.Definition) {
			addTransitionForValidationTest(def, "group_second_done", "node_agent", "done", "Done Again", "edge_second_done", "second_done", "node_done")
		}, code: workflow.CodeDuplicateTransitionID},
		{name: "edge transition group missing", edit: func(def *workflow.Definition) { def.Edges[1].TransitionGroupID = "missing" }, code: workflow.CodeEdgeTransitionGroupMissing},
		{name: "missing edge id", edit: func(def *workflow.Definition) { def.Edges[1].ID = "" }, code: workflow.CodeMissingEdgeID},
		{name: "duplicate edge id", edit: func(def *workflow.Definition) { def.Edges[1].ID = def.Edges[0].ID }, code: workflow.CodeDuplicateEdgeID},
		{name: "missing edge key", edit: func(def *workflow.Definition) { def.Edges[1].Key = "" }, code: workflow.CodeMissingEdgeKey},
		{name: "invalid edge key", edit: func(def *workflow.Definition) { def.Edges[1].Key = "Done!" }, code: workflow.CodeInvalidEdgeKey},
		{name: "duplicate edge key per group", edit: func(def *workflow.Definition) {
			def.Edges = append(def.Edges, workflow.Edge{WorkflowID: def.ID, ID: "edge_start_dup", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_agent", ContextMode: workflow.ContextModeNewSession})
		}, code: workflow.CodeDuplicateEdgeKey},
		{name: "edge target missing", edit: func(def *workflow.Definition) { def.Edges[1].TargetNodeID = "missing" }, code: workflow.CodeEdgeTargetMissing},
		{name: "cross workflow node reference", edit: func(def *workflow.Definition) { def.Nodes[1].WorkflowID = "other" }, code: workflow.CodeCrossWorkflowReference},
		{name: "cross workflow group reference", edit: func(def *workflow.Definition) { def.TransitionGroups[1].WorkflowID = "other" }, code: workflow.CodeCrossWorkflowReference},
		{name: "cross workflow edge reference", edit: func(def *workflow.Definition) { def.Edges[1].WorkflowID = "other" }, code: workflow.CodeCrossWorkflowReference},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validWorkflow()
			tt.edit(&def)

			result := validateForTask(def)

			assertHasCodes(t, result, tt.code)
		})
	}
}

func TestTransitionInvocationContractsContextAndRoles(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *workflow.Definition)
		code workflow.ValidationErrorCode
	}{
		{name: "missing prompt into agent target", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").PromptTemplate = ""
		}, code: workflow.CodeTransitionPromptRequired},
		{name: "prompt forbidden into terminal target", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").PromptTemplate = "No."
		}, code: workflow.CodeTransitionPromptForbidden},
		{name: "prompt forbidden into join target", edit: func(t *testing.T, def *workflow.Definition) {
			*def = fanoutWorkflow()
			edgeByIDForValidationTest(t, def, "edge_impl_a_join").PromptTemplate = "No."
		}, code: workflow.CodeTransitionPromptForbidden},
		{name: "start transition parameters", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").Parameters = []workflow.Parameter{{Key: "task_context", Description: "Task context."}}
		}, code: workflow.CodeInvalidParameter},
		{name: "join outgoing transition parameters", edit: func(t *testing.T, def *workflow.Definition) {
			*def = fanoutWorkflow()
			edgeByIDForValidationTest(t, def, "edge_join_done").Parameters = []workflow.Parameter{{Key: "aggregate", Description: "Join aggregate."}}
		}, code: workflow.CodeInvalidParameter},
		{name: "invalid parameter key", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "Bad Key", Description: "Bad key."}}
		}, code: workflow.CodeInvalidParameter},
		{name: "too long parameter key", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "a" + stringOf("b", workflow.MaxParameterKeyChars), Description: "Too long."}}
		}, code: workflow.CodeInvalidParameter},
		{name: "reserved parameter key transition", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "transition", Description: "Reserved."}}
		}, code: workflow.CodeInvalidParameter},
		{name: "reserved parameter key commentary", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "commentary", Description: "Reserved."}}
		}, code: workflow.CodeInvalidParameter},
		{name: "duplicate parameter key", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{
				{Key: "summary", Description: "Summary."},
				{Key: "summary", Description: "Another summary."},
			}
		}, code: workflow.CodeDuplicateParameter},
		{name: "parameter description required", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "summary", Description: " "}}
		}, code: workflow.CodeParameterDescriptionRequired},
		{name: "parameter description too large", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "summary", Description: stringOf("a", workflow.MaxParameterDescriptionChars+1)}}
		}, code: workflow.CodeParameterSchemaTooLarge},
		{name: "invalid current parameter placeholder", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").PromptTemplate = "Use {{.Params.missing}}."
		}, code: workflow.CodeInvalidTemplatePlaceholder},
		{name: "legacy input placeholder", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").PromptTemplate = "Use {{.Inputs.task_title}}."
		}, code: workflow.CodeInvalidTemplatePlaceholder},
		{name: "legacy node placeholder", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").PromptTemplate = "Use {{.Nodes.plan.summary}}."
		}, code: workflow.CodeInvalidTemplatePlaceholder},
		{name: "dynamic parameter placeholder lookup", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").PromptTemplate = `Use {{index .Params "summary"}}.`
		}, code: workflow.CodeInvalidTemplatePlaceholder},
		{name: "invalid template syntax", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_start").PromptTemplate = "Use {{.Params.task_title"
		}, code: workflow.CodeInvalidTemplatePlaceholder},
		{name: "invalid context mode", edit: func(t *testing.T, def *workflow.Definition) {
			edgeByIDForValidationTest(t, def, "edge_done").ContextMode = workflow.ContextMode("reuse")
		}, code: workflow.CodeInvalidContextMode},
		{name: "agent role required", edit: func(t *testing.T, def *workflow.Definition) { def.Nodes[1].SubagentRole = "" }, code: workflow.CodeAgentRoleRequired},
		{name: "agent role missing", edit: func(t *testing.T, def *workflow.Definition) { def.Nodes[1].SubagentRole = "reviewer" }, code: workflow.CodeAgentRoleMissing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validWorkflow()
			tt.edit(t, &def)

			result := validateForTask(def)

			assertHasCodes(t, result, tt.code)
		})
	}

	t.Run("valid current parameter and template functions pass", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_implementation_review")
		edge.PromptTemplate = `{{if .Params.summary}}{{printf "%s" .Params.summary}}{{end}}`

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
		assertNoCode(t, result, workflow.CodeTransitionPromptRequired)
	})

	t.Run("draft validation allows empty agent transition prompt", func(t *testing.T) {
		def := validWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_start").PromptTemplate = ""

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertNoCode(t, result, workflow.CodeTransitionPromptRequired)
	})

	t.Run("valid start prompt built-ins pass", func(t *testing.T) {
		def := validWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_start").PromptTemplate = "Start {{.TaskId}} {{.TaskShortId}} {{.TaskTitle}} {{.TaskBody}} for {{.NodeId}} {{.NodeKey}} {{.NodeDisplayName}}."

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("valid commentary placeholder passes without declared parameter", func(t *testing.T) {
		def := validWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_start").PromptTemplate = "Start after {{.Params.commentary}}."

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("unknown current parameter placeholder exposes structured details", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_implementation_review").PromptTemplate = "Use {{.Params.missing}}."

		result := validateForTask(def)

		for _, err := range result.Errors {
			if err.Code == workflow.CodeInvalidTemplatePlaceholder {
				if err.InputName != "missing" || err.Placeholder != ".Params.missing" {
					t.Fatalf("placeholder details = %+v", err)
				}
				return
			}
		}
		t.Fatalf("missing %s in %+v", workflow.CodeInvalidTemplatePlaceholder, result.Errors)
	})

	t.Run("valid prior transition parameter placeholder passes", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_join_accept").PromptTemplate = "Accept {{.Params.review.summary}}."

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("missing prior transition parameter exposes structured details", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_join_accept").PromptTemplate = "Accept {{.Params.review.missing}}."

		result := validateForTask(def)

		for _, err := range result.Errors {
			if err.Code == workflow.CodeInvalidTemplatePlaceholder {
				if err.FieldName != "missing" || err.Placeholder != ".Params.review.missing" {
					t.Fatalf("placeholder details = %+v", err)
				}
				return
			}
		}
		t.Fatalf("missing %s in %+v", workflow.CodeInvalidTemplatePlaceholder, result.Errors)
	})

	t.Run("future transition parameter placeholder blocks task validation", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_accept_open_pr").Parameters = []workflow.Parameter{{Key: "summary", Description: "Approval summary."}}
		edgeByIDForValidationTest(t, &def, "edge_implementation_review").PromptTemplate = "Review {{.Params.approved.summary}}."

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("ambiguous prior transition parameter placeholder blocks task validation", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		transitionGroupByIDForValidationTest(t, &def, "group_start").TransitionID = "review"
		edgeByIDForValidationTest(t, &def, "edge_join_accept").PromptTemplate = "Accept {{.Params.review.summary}}."

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("join source prompt validates current parameters against join aggregate", func(t *testing.T) {
		def := joinParameterWorkflow()

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("prior transition parameter from join aggregate passes", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_code_review_join").Parameters = []workflow.Parameter{{Key: "code_review_findings", Description: "Code review findings."}}
		edgeByIDForValidationTest(t, &def, "edge_qa_test_join").Parameters = []workflow.Parameter{{Key: "qa_findings", Description: "QA findings."}}
		edgeByIDForValidationTest(t, &def, "edge_accept_open_pr").PromptTemplate = "Open PR {{.Params.accept.qa_findings}} and {{.Params.accept.code_review_findings}}."

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("missing prior transition parameter from join aggregate blocks task validation", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_code_review_join").Parameters = []workflow.Parameter{{Key: "code_review_findings", Description: "Code review findings."}}
		edgeByIDForValidationTest(t, &def, "edge_accept_open_pr").PromptTemplate = "Open PR {{.Params.accept.missing}}."

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})

	t.Run("join aggregate collision from different producing transitions blocks task validation", func(t *testing.T) {
		def := joinParameterWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_branch_b_join").Parameters = []workflow.Parameter{{Key: "plan", Description: "Implementation plan."}}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeProvisionFieldOverlap)
	})

	t.Run("legacy node prompt input and output fields are inert", func(t *testing.T) {
		def := validWorkflow()
		def.Nodes[0].PromptTemplate = "{{.Inputs.task_title}}"
		def.Nodes[0].InputFields = []workflow.InputField{{Name: "Bad Field", Description: " "}}
		def.Nodes[1].PromptTemplate = "{{.Nodes.deleted.summary}}"
		def.Nodes[1].InputFields = []workflow.InputField{{Name: "Bad Field", Description: " "}}
		def.Nodes[1].OutputFields = []workflow.OutputField{{Name: "transition_id", Description: " "}}

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidInputField)
		assertNoCode(t, result, workflow.CodeInvalidOutputField)
		assertNoCode(t, result, workflow.CodeInvalidTemplatePlaceholder)
	})
}

func TestRuntimeValidationBlocksUnimplementedExecutionFeatures(t *testing.T) {
	t.Run("approval-gated edges are valid runtime features", func(t *testing.T) {
		def := validWorkflow()
		def.Edges[1].RequiresApproval = true

		draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft})
		assertNoCode(t, draft, workflow.CodeUnsupportedApprovalExecution)
		if draft.HasBlockingErrors() {
			t.Fatalf("draft approval should not block saving: %+v", draft.BlockingErrors())
		}

		task := validateForTask(def)
		assertNoCode(t, task, workflow.CodeUnsupportedApprovalExecution)
		if task.HasBlockingErrors() {
			t.Fatalf("task approval should not block execution: %+v", task.BlockingErrors())
		}
	})

	t.Run("context modes are valid runtime features", func(t *testing.T) {
		for _, mode := range []workflow.ContextMode{workflow.ContextModeContinueSession, workflow.ContextModeCompactAndContinueSession} {
			t.Run(string(mode), func(t *testing.T) {
				def := validWorkflow()
				def.Edges[1].ContextMode = mode

				draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft})
				assertNoCode(t, draft, workflow.CodeUnsupportedContextMode)
				if draft.HasBlockingErrors() {
					t.Fatalf("draft context mode should not block saving: %+v", draft.BlockingErrors())
				}

				task := validateForTask(def)
				assertNoCode(t, task, workflow.CodeUnsupportedContextMode)
				if task.HasBlockingErrors() {
					t.Fatalf("task context mode should not block execution: %+v", task.BlockingErrors())
				}
			})
		}
	})

	t.Run("join targets are valid runtime features", func(t *testing.T) {
		def := fanoutWorkflow()

		draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft})
		assertNoCode(t, draft, workflow.CodeUnsupportedJoinExecution)
		if draft.HasBlockingErrors() {
			t.Fatalf("draft join should not block saving: %+v", draft.BlockingErrors())
		}

		task := validateForTask(def)
		assertNoCode(t, task, workflow.CodeUnsupportedJoinExecution)
		if task.HasBlockingErrors() {
			t.Fatalf("task join should not block execution: %+v", task.BlockingErrors())
		}
	})

	t.Run("join bindings are valid runtime features", func(t *testing.T) {
		def := validWorkflow()
		def.Edges[0].InputBindings = []workflow.InputBinding{{Name: "joined", Source: workflow.BindingSourceJoin, Field: "aggregate"}}

		draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft})
		assertNoCode(t, draft, workflow.CodeUnsupportedJoinBinding)
		if draft.HasBlockingErrors() {
			t.Fatalf("draft join binding should not block saving: %+v", draft.BlockingErrors())
		}

		task := validateForTask(def)
		assertNoCode(t, task, workflow.CodeUnsupportedJoinBinding)
		if task.HasBlockingErrors() {
			t.Fatalf("task join binding should not block execution: %+v", task.BlockingErrors())
		}
	})
}

func TestFanoutJoinTopology(t *testing.T) {
	t.Run("valid fanout has one nearest common join", func(t *testing.T) {
		def := fanoutWorkflow()

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidFanoutJoinTopology)
	})

	t.Run("valid fanout allows farther common join after unique nearest join", func(t *testing.T) {
		def := fanoutWorkflow()
		def.Nodes = append(def.Nodes,
			workflow.Node{WorkflowID: def.ID, ID: "node_impl_a_late", Key: "impl_a_late", DisplayName: "Implement A Late", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "A late."},
			workflow.Node{WorkflowID: def.ID, ID: "node_impl_b_late", Key: "impl_b_late", DisplayName: "Implement B Late", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "B late."},
			workflow.Node{WorkflowID: def.ID, ID: "node_join_late", Key: "join_late", DisplayName: "Join Late", Kind: workflow.NodeKindJoin},
		)
		def.TransitionGroups = append(def.TransitionGroups,
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_a_late", SourceNodeID: "node_impl_a", TransitionID: "join_late", DisplayName: "Join Late"},
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_a_late_join", SourceNodeID: "node_impl_a_late", TransitionID: "join_late", DisplayName: "Join Late"},
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_b_late", SourceNodeID: "node_impl_b", TransitionID: "join_late", DisplayName: "Join Late"},
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_b_late_join", SourceNodeID: "node_impl_b_late", TransitionID: "join_late", DisplayName: "Join Late"},
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_join_late_done", SourceNodeID: "node_join_late", TransitionID: "done", DisplayName: "Done"},
		)
		def.Edges = append(def.Edges,
			workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_a_late", Key: "late_a", TransitionGroupID: "group_impl_a_late", TargetNodeID: "node_impl_a_late", ContextMode: workflow.ContextModeNewSession},
			workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_a_late_join", Key: "late_join_a", TransitionGroupID: "group_impl_a_late_join", TargetNodeID: "node_join_late", ContextMode: workflow.ContextModeNewSession},
			workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_b_late", Key: "late_b", TransitionGroupID: "group_impl_b_late", TargetNodeID: "node_impl_b_late", ContextMode: workflow.ContextModeNewSession},
			workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_b_late_join", Key: "late_join_b", TransitionGroupID: "group_impl_b_late_join", TargetNodeID: "node_join_late", ContextMode: workflow.ContextModeNewSession},
			workflow.Edge{WorkflowID: def.ID, ID: "edge_join_late_done", Key: "done", TransitionGroupID: "group_join_late_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession},
		)

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidFanoutJoinTopology)
	})

	tests := []struct {
		name string
		edit func(*workflow.Definition)
	}{
		{
			name: "terminal before join",
			edit: func(def *workflow.Definition) {
				def.Edges[2].TargetNodeID = "node_done"
			},
		},
		{
			name: "nested fanout before join",
			edit: func(def *workflow.Definition) {
				def.Nodes = append(def.Nodes, workflow.Node{WorkflowID: def.ID, ID: "node_extra", Key: "extra", DisplayName: "Extra", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Extra."})
				def.Edges[2].TargetNodeID = "node_extra"
				def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_extra_fanout", SourceNodeID: "node_extra", TransitionID: "split", DisplayName: "Split"})
				def.Edges = append(def.Edges,
					workflow.Edge{WorkflowID: def.ID, ID: "edge_extra_a", Key: "extra_a", TransitionGroupID: "group_extra_fanout", TargetNodeID: "node_impl_a", ContextMode: workflow.ContextModeNewSession},
					workflow.Edge{WorkflowID: def.ID, ID: "edge_extra_b", Key: "extra_b", TransitionGroupID: "group_extra_fanout", TargetNodeID: "node_impl_b", ContextMode: workflow.ContextModeNewSession},
				)
			},
		},
		{
			name: "cycle before join",
			edit: func(def *workflow.Definition) {
				addAgentLoop(def, "node_impl_a", "cycle", "edge_cycle", "cycle")
			},
		},
		{
			name: "ambiguous nearest join",
			edit: func(def *workflow.Definition) {
				def.Nodes = append(def.Nodes, workflow.Node{WorkflowID: def.ID, ID: "node_join2", Key: "join2", DisplayName: "Join 2", Kind: workflow.NodeKindJoin})
				def.TransitionGroups = append(def.TransitionGroups,
					workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_a_join2", SourceNodeID: "node_impl_a", TransitionID: "join2", DisplayName: "Join 2"},
					workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_b_join2", SourceNodeID: "node_impl_b", TransitionID: "join2", DisplayName: "Join 2"},
					workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_join2_done", SourceNodeID: "node_join2", TransitionID: "done", DisplayName: "Done"},
				)
				def.Edges = append(def.Edges,
					workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_a_join2", Key: "join2", TransitionGroupID: "group_impl_a_join2", TargetNodeID: "node_join2", ContextMode: workflow.ContextModeNewSession},
					workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_b_join2", Key: "join2", TransitionGroupID: "group_impl_b_join2", TargetNodeID: "node_join2", ContextMode: workflow.ContextModeNewSession},
					workflow.Edge{WorkflowID: def.ID, ID: "edge_join2_done", Key: "done", TransitionGroupID: "group_join2_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession},
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := fanoutWorkflow()
			tt.edit(&def)

			result := validateForTask(def)

			assertHasCodes(t, result, workflow.CodeInvalidFanoutJoinTopology)
		})
	}
}

func TestNodeGroupV1ParallelGroupValidation(t *testing.T) {
	t.Run("valid group has branches join fanout and branch join edges", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidNodeGroup)
	})

	t.Run("one branch draft group is invalid but non-blocking", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		def.Nodes = setNodeGroup(def.Nodes, "node_impl_b", "")

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertHasCodes(t, result, workflow.CodeInvalidNodeGroup)
		if !result.HasBlockingErrors() {
			t.Fatalf("invalid draft node group shape should block graph save")
		}
	})

	t.Run("missing join is invalid", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		def.Nodes = setNodeGroup(def.Nodes, "node_join", "")

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidNodeGroup)
	})

	t.Run("missing fanout is invalid", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		def.Edges = def.Edges[:1]

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidNodeGroup)
	})

	t.Run("start backed fanout explains split agent workaround", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		transitionGroupByIDForValidationTest(t, &def, "group_split").SourceNodeID = "node_start"

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertValidationMessage(
			t,
			result,
			workflow.CodeInvalidNodeGroup,
			"",
			"Node Backlog cannot directly fan out into a node group yet; insert one split agent after it, fan out from that agent into the group, then join the branches",
		)
	})

	t.Run("start backed fanout message uses renamed start node", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		nodeByKeyForValidationTest(t, &def, "backlog").DisplayName = "Inbox"
		transitionGroupByIDForValidationTest(t, &def, "group_split").SourceNodeID = "node_start"

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertValidationMessage(
			t,
			result,
			workflow.CodeInvalidNodeGroup,
			"",
			"Node Inbox cannot directly fan out into a node group yet; insert one split agent after it, fan out from that agent into the group, then join the branches",
		)
	})

	t.Run("separate start backed branch transitions explain split agent workaround", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		transitionGroupByIDForValidationTest(t, &def, "group_split").SourceNodeID = "node_start"
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
			WorkflowID:   def.ID,
			ID:           "group_start_impl_b",
			SourceNodeID: "node_start",
			TransitionID: "split_b",
			DisplayName:  "Split B",
		})
		edgeByIDForValidationTest(t, &def, "edge_split_b").TransitionGroupID = "group_start_impl_b"

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertValidationMessage(
			t,
			result,
			workflow.CodeInvalidNodeGroup,
			"",
			"Node Backlog cannot directly fan out into a node group yet; insert one split agent after it, fan out from that agent into the group, then join the branches",
		)
	})

	t.Run("separate source branch transitions explain single fanout repair", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
			WorkflowID:   def.ID,
			ID:           "group_plan_impl_b",
			SourceNodeID: "node_plan",
			TransitionID: "implement_b",
			DisplayName:  "Implement B",
		})
		edgeByIDForValidationTest(t, &def, "edge_split_b").TransitionGroupID = "group_plan_impl_b"

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertValidationMessage(
			t,
			result,
			workflow.CodeInvalidNodeGroup,
			"",
			"Node Plan uses separate transitions into the node group branches; use one transition from Plan with one edge to each branch, then connect every branch to the join",
		)
	})

	t.Run("stale same source branch transition with valid fanout explains single fanout repair", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
			WorkflowID:   def.ID,
			ID:           "group_plan_impl_b_stale",
			SourceNodeID: "node_plan",
			TransitionID: "implement_b_stale",
			DisplayName:  "Implement B",
		})
		def.Edges = append(def.Edges, workflow.Edge{
			WorkflowID:        def.ID,
			ID:                "edge_plan_impl_b_stale",
			Key:               "implement_b_stale",
			TransitionGroupID: "group_plan_impl_b_stale",
			TargetNodeID:      "node_impl_b",
			ContextMode:       workflow.ContextModeNewSession,
		})

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertValidationMessage(
			t,
			result,
			workflow.CodeInvalidNodeGroup,
			"",
			"Node Plan uses separate transitions into the node group branches; use one transition from Plan with one edge to each branch, then connect every branch to the join",
		)
	})

	t.Run("duplicate fanout branch edge is invalid", func(t *testing.T) {
		def := fanoutWorkflow()
		addV1NodeGroup(&def)
		def.Edges = append(def.Edges, workflow.Edge{
			WorkflowID:        def.ID,
			ID:                "edge_split_b_duplicate",
			Key:               "split_b_duplicate",
			TransitionGroupID: "group_split",
			TargetNodeID:      "node_impl_b",
			ContextMode:       workflow.ContextModeNewSession,
		})

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertValidationMessage(
			t,
			result,
			workflow.CodeInvalidNodeGroup,
			"",
			"node group must be represented by one fan-out transition group and branch edges into its join",
		)
	})
}

func TestContextSourceValidation(t *testing.T) {
	t.Run("default immediate source preserves existing workflows", func(t *testing.T) {
		def := validWorkflow()

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("direct selected source node validates", func(t *testing.T) {
		def := validWorkflow()
		def.Nodes = append(def.Nodes, workflow.Node{WorkflowID: def.ID, ID: "node_review", Key: "review", DisplayName: "Review", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Review."})
		def.TransitionGroups[1] = workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_review", SourceNodeID: "node_agent", TransitionID: "review", DisplayName: "Review"}
		def.Edges[1] = workflow.Edge{WorkflowID: def.ID, ID: "edge_review", Key: "review", TransitionGroupID: "group_review", TargetNodeID: "node_review", ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implement"}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_done", SourceNodeID: "node_review", TransitionID: "done", DisplayName: "Done"})
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: def.ID, ID: "edge_done", Key: "done", TransitionGroupID: "group_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession})

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("post join selected dominator source validates", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}
		edge.ContextMode = workflow.ContextModeContinueSession

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("future node is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_implementation_review")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "open_pr"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("optional branch is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		def.Nodes = append(def.Nodes, workflow.Node{WorkflowID: def.ID, ID: "node_optional", Key: "optional", DisplayName: "Optional", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Optional."})
		def.TransitionGroups = append(def.TransitionGroups,
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_implementation_optional", SourceNodeID: "node_implementation", TransitionID: "optional", DisplayName: "Optional"},
			workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_optional_review", SourceNodeID: "node_optional", TransitionID: "review", DisplayName: "Review"},
		)
		def.Edges = append(def.Edges,
			workflow.Edge{WorkflowID: def.ID, ID: "edge_implementation_optional", Key: "optional", TransitionGroupID: "group_implementation_optional", TargetNodeID: "node_optional", ContextMode: workflow.ContextModeNewSession, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}},
			workflow.Edge{WorkflowID: def.ID, ID: "edge_optional_review", Key: "review", TransitionGroupID: "group_optional_review", TargetNodeID: "node_code_review", ContextMode: workflow.ContextModeNewSession},
		)
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "optional"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("sibling fanout branch after join is invalid in v1", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "code_review"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("selected source must be agent", func(t *testing.T) {
		for _, key := range []workflow.ModelKey{"backlog", "review_join", "done"} {
			t.Run(string(key), func(t *testing.T) {
				def := reviewAcceptanceWorkflow()
				edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
				edge.ContextMode = workflow.ContextModeContinueSession
				edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: key}

				result := validateForTask(def)

				assertHasCodes(t, result, workflow.CodeInvalidContextSource)
			})
		}
	})

	t.Run("missing node key is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "missing"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("selected target node is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "open_pr"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("start edge explicit context source is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_start")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("new session cannot select context source", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeNewSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("continuation role mismatch is valid", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			contextMode workflow.ContextMode
		}{
			{name: "continue", contextMode: workflow.ContextModeContinueSession},
			{name: "compact", contextMode: workflow.ContextModeCompactAndContinueSession},
		} {
			t.Run(tc.name, func(t *testing.T) {
				def := reviewAcceptanceWorkflow()
				edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
				edge.ContextMode = tc.contextMode
				edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}
				nodeByKeyForValidationTest(t, &def, "open_pr").SubagentRole = workflow.DefaultAgentRole

				result := validateForTask(def)

				assertNoCode(t, result, workflow.CodeInvalidContextSource)
			})
		}
	})

	t.Run("immediate source continuation after join is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_join_accept")
		edge.ContextMode = workflow.ContextModeContinueSession

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("rework loop remains statically valid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_accept_rework", SourceNodeID: "node_final_acceptance", TransitionID: "needs_changes", DisplayName: "Needs Changes"})
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: def.ID, ID: "edge_accept_rework", Key: "rework", TransitionGroupID: "group_accept_rework", TargetNodeID: "node_implementation", ContextMode: workflow.ContextModeCompactAndContinueSession})
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("previous target loop source validates when target dominates source", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_accept_rework", SourceNodeID: "node_final_acceptance", TransitionID: "needs_changes", DisplayName: "Needs Changes"})
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: def.ID, ID: "edge_accept_rework", Key: "rework", TransitionGroupID: "group_accept_rework", TargetNodeID: "node_implementation", ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}})

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("previous target requires continuation mode", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_accept_rework", SourceNodeID: "node_final_acceptance", TransitionID: "needs_changes", DisplayName: "Needs Changes"})
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: def.ID, ID: "edge_accept_rework", Key: "rework", TransitionGroupID: "group_accept_rework", TargetNodeID: "node_implementation", ContextMode: workflow.ContextModeNewSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}})

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("previous target requires an agent target", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_open_pr_done")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("previous target requires target to dominate source", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_implementation_review")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("draft reports nonblocking context source semantics", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "code_review"}

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: workflow.StaticRoleResolver{"coder": true}})

		assertHasCodes(t, result, workflow.CodeInvalidContextSource)
		if result.HasBlockingErrors() {
			t.Fatalf("draft context source semantics should not block saving: %+v", result.BlockingErrors())
		}
	})
}

func validWorkflow() workflow.Definition {
	return workflow.Definition{
		ID:          "workflow_default",
		DisplayName: "Default Workflow",
		Nodes: []workflow.Node{
			{WorkflowID: "workflow_default", ID: "node_start", Key: "backlog", DisplayName: "Backlog", Kind: workflow.NodeKindStart},
			{
				WorkflowID:     "workflow_default",
				ID:             "node_agent",
				Key:            "implement",
				DisplayName:    "Implement",
				Kind:           workflow.NodeKindAgent,
				SubagentRole:   "coder",
				PromptTemplate: "Implement task.",
				OutputFields:   []workflow.OutputField{{Name: "summary", Description: "Summary of completed work."}},
			},
			{WorkflowID: "workflow_default", ID: "node_done", Key: "done", DisplayName: "Done", Kind: workflow.NodeKindTerminal},
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: "workflow_default", ID: "group_start", SourceNodeID: "node_start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: "workflow_default", ID: "group_done", SourceNodeID: "node_agent", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: "workflow_default", ID: "edge_start", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_agent", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement task."},
			{
				WorkflowID:         "workflow_default",
				ID:                 "edge_done",
				Key:                "done",
				TransitionGroupID:  "group_done",
				TargetNodeID:       "node_done",
				ContextMode:        workflow.ContextModeNewSession,
				Parameters:         []workflow.Parameter{{Key: "summary", Description: "Summary of completed work."}},
				OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}},
			},
		},
	}
}

func fanoutWorkflow() workflow.Definition {
	def := workflow.Definition{
		ID:          "workflow_fanout",
		DisplayName: "Fanout Workflow",
		Nodes: []workflow.Node{
			{WorkflowID: "workflow_fanout", ID: "node_start", Key: "backlog", DisplayName: "Backlog", Kind: workflow.NodeKindStart},
			{WorkflowID: "workflow_fanout", ID: "node_plan", Key: "plan", DisplayName: "Plan", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
			{WorkflowID: "workflow_fanout", ID: "node_impl_a", Key: "impl_a", DisplayName: "Implement A", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "A."},
			{WorkflowID: "workflow_fanout", ID: "node_impl_b", Key: "impl_b", DisplayName: "Implement B", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "B."},
			{WorkflowID: "workflow_fanout", ID: "node_join", Key: "join", DisplayName: "Join", Kind: workflow.NodeKindJoin},
			{WorkflowID: "workflow_fanout", ID: "node_done", Key: "done", DisplayName: "Done", Kind: workflow.NodeKindTerminal},
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: "workflow_fanout", ID: "group_start", SourceNodeID: "node_start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: "workflow_fanout", ID: "group_split", SourceNodeID: "node_plan", TransitionID: "split", DisplayName: "Split"},
			{WorkflowID: "workflow_fanout", ID: "group_impl_a_join", SourceNodeID: "node_impl_a", TransitionID: "join", DisplayName: "Join"},
			{WorkflowID: "workflow_fanout", ID: "group_impl_b_join", SourceNodeID: "node_impl_b", TransitionID: "join", DisplayName: "Join"},
			{WorkflowID: "workflow_fanout", ID: "group_join_done", SourceNodeID: "node_join", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: "workflow_fanout", ID: "edge_start", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_plan", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
			{WorkflowID: "workflow_fanout", ID: "edge_split_a", Key: "split_a", TransitionGroupID: "group_split", TargetNodeID: "node_impl_a", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement A."},
			{WorkflowID: "workflow_fanout", ID: "edge_split_b", Key: "split_b", TransitionGroupID: "group_split", TargetNodeID: "node_impl_b", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement B."},
			{WorkflowID: "workflow_fanout", ID: "edge_impl_a_join", Key: "join_a", TransitionGroupID: "group_impl_a_join", TargetNodeID: "node_join", ContextMode: workflow.ContextModeNewSession},
			{WorkflowID: "workflow_fanout", ID: "edge_impl_b_join", Key: "join_b", TransitionGroupID: "group_impl_b_join", TargetNodeID: "node_join", ContextMode: workflow.ContextModeNewSession},
			{WorkflowID: "workflow_fanout", ID: "edge_join_done", Key: "done", TransitionGroupID: "group_join_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession},
		},
	}
	return def
}

func reviewAcceptanceWorkflow() workflow.Definition {
	return workflow.Definition{
		ID:          "workflow_review_acceptance",
		DisplayName: "Review Acceptance Workflow",
		Nodes: []workflow.Node{
			{WorkflowID: "workflow_review_acceptance", ID: "node_start", Key: "backlog", DisplayName: "Backlog", Kind: workflow.NodeKindStart},
			{WorkflowID: "workflow_review_acceptance", ID: "node_implementation", Key: "implementation", DisplayName: "Implementation", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Implement.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
			{WorkflowID: "workflow_review_acceptance", ID: "node_code_review", Key: "code_review", DisplayName: "Code Review", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Review."},
			{WorkflowID: "workflow_review_acceptance", ID: "node_qa_test", Key: "qa_test", DisplayName: "QA Test", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "QA."},
			{WorkflowID: "workflow_review_acceptance", ID: "node_review_join", Key: "review_join", DisplayName: "Review Join", Kind: workflow.NodeKindJoin},
			{WorkflowID: "workflow_review_acceptance", ID: "node_final_acceptance", Key: "final_acceptance", DisplayName: "Final Acceptance", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Accept."},
			{WorkflowID: "workflow_review_acceptance", ID: "node_open_pr", Key: "open_pr", DisplayName: "Open PR", Kind: workflow.NodeKindAgent, SubagentRole: "coder", PromptTemplate: "Open PR."},
			{WorkflowID: "workflow_review_acceptance", ID: "node_done", Key: "done", DisplayName: "Done", Kind: workflow.NodeKindTerminal},
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: "workflow_review_acceptance", ID: "group_start", SourceNodeID: "node_start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: "workflow_review_acceptance", ID: "group_implementation_review", SourceNodeID: "node_implementation", TransitionID: "review", DisplayName: "Review"},
			{WorkflowID: "workflow_review_acceptance", ID: "group_code_review_join", SourceNodeID: "node_code_review", TransitionID: "reviewed", DisplayName: "Reviewed"},
			{WorkflowID: "workflow_review_acceptance", ID: "group_qa_test_join", SourceNodeID: "node_qa_test", TransitionID: "reviewed", DisplayName: "Reviewed"},
			{WorkflowID: "workflow_review_acceptance", ID: "group_join_accept", SourceNodeID: "node_review_join", TransitionID: "accept", DisplayName: "Accept"},
			{WorkflowID: "workflow_review_acceptance", ID: "group_accept_open_pr", SourceNodeID: "node_final_acceptance", TransitionID: "approved", DisplayName: "Approved"},
			{WorkflowID: "workflow_review_acceptance", ID: "group_open_pr_done", SourceNodeID: "node_open_pr", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: "workflow_review_acceptance", ID: "edge_start", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_implementation", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement."},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_implementation_review", Key: "code_review", TransitionGroupID: "group_implementation_review", TargetNodeID: "node_code_review", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_implementation_qa", Key: "qa_test", TransitionGroupID: "group_implementation_review", TargetNodeID: "node_qa_test", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "QA {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_code_review_join", Key: "code_review_done", TransitionGroupID: "group_code_review_join", TargetNodeID: "node_review_join", ContextMode: workflow.ContextModeNewSession},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_qa_test_join", Key: "qa_test_done", TransitionGroupID: "group_qa_test_join", TargetNodeID: "node_review_join", ContextMode: workflow.ContextModeNewSession},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_join_accept", Key: "final_acceptance", TransitionGroupID: "group_join_accept", TargetNodeID: "node_final_acceptance", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Accept."},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_accept_open_pr", Key: "open_pr", TransitionGroupID: "group_accept_open_pr", TargetNodeID: "node_open_pr", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Open PR."},
			{WorkflowID: "workflow_review_acceptance", ID: "edge_open_pr_done", Key: "done", TransitionGroupID: "group_open_pr_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession},
		},
	}
}

func validateForTask(def workflow.Definition) workflow.ValidationResult {
	return workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextTaskCreation,
		RoleResolver: workflow.StaticRoleResolver{"coder": true},
	})
}

func edgeByIDForValidationTest(t *testing.T, def *workflow.Definition, id workflow.EdgeID) *workflow.Edge {
	t.Helper()
	for i := range def.Edges {
		if def.Edges[i].ID == id {
			return &def.Edges[i]
		}
	}
	t.Fatalf("edge %q not found", id)
	return nil
}

func transitionGroupByIDForValidationTest(t *testing.T, def *workflow.Definition, id workflow.TransitionGroupID) *workflow.TransitionGroup {
	t.Helper()
	for i := range def.TransitionGroups {
		if def.TransitionGroups[i].ID == id {
			return &def.TransitionGroups[i]
		}
	}
	t.Fatalf("transition group %q not found", id)
	return nil
}

func nodeByKeyForValidationTest(t *testing.T, def *workflow.Definition, key workflow.ModelKey) *workflow.Node {
	t.Helper()
	for i := range def.Nodes {
		if def.Nodes[i].Key == key {
			return &def.Nodes[i]
		}
	}
	t.Fatalf("node %q not found", key)
	return nil
}

func addTransitionForValidationTest(def *workflow.Definition, groupID, sourceNodeID, transitionID, displayName, edgeID, edgeKey, targetNodeID string) {
	def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
		WorkflowID:   def.ID,
		ID:           workflow.TransitionGroupID(groupID),
		SourceNodeID: workflow.NodeID(sourceNodeID),
		TransitionID: workflow.TransitionID(transitionID),
		DisplayName:  displayName,
	})
	def.Edges = append(def.Edges, workflow.Edge{
		WorkflowID:        def.ID,
		ID:                workflow.EdgeID(edgeID),
		Key:               workflow.ModelKey(edgeKey),
		TransitionGroupID: workflow.TransitionGroupID(groupID),
		TargetNodeID:      workflow.NodeID(targetNodeID),
		ContextMode:       workflow.ContextModeNewSession,
	})
}

func addV1NodeGroup(def *workflow.Definition) {
	def.NodeGroups = append(def.NodeGroups, workflow.NodeGroup{
		WorkflowID:  def.ID,
		ID:          "group_parallel",
		Key:         "parallel",
		DisplayName: "Parallel",
	})
	def.Nodes = setNodeGroup(def.Nodes, "node_impl_a", "group_parallel")
	def.Nodes = setNodeGroup(def.Nodes, "node_impl_b", "group_parallel")
	def.Nodes = setNodeGroup(def.Nodes, "node_join", "group_parallel")
}

func setNodeGroup(nodes []workflow.Node, nodeID workflow.NodeID, groupID string) []workflow.Node {
	out := append([]workflow.Node(nil), nodes...)
	for index := range out {
		if out[index].ID == nodeID {
			out[index].GroupID = groupID
		}
	}
	return out
}

func addAgentLoop(def *workflow.Definition, source workflow.NodeID, groupSuffix string, edgeID workflow.EdgeID, transitionID string) {
	groupID := workflow.TransitionGroupID("group_" + groupSuffix)
	def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
		WorkflowID:   def.ID,
		ID:           groupID,
		SourceNodeID: source,
		TransitionID: workflow.TransitionID(transitionID),
		DisplayName:  "Loop",
	})
	def.Edges = append(def.Edges, workflow.Edge{
		WorkflowID:        def.ID,
		ID:                edgeID,
		Key:               workflow.ModelKey(groupSuffix),
		TransitionGroupID: groupID,
		TargetNodeID:      source,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Loop.",
	})
}

func assertHasCodes(t *testing.T, result workflow.ValidationResult, want ...workflow.ValidationErrorCode) {
	t.Helper()
	got := result.Codes()
	for _, code := range want {
		if !slices.Contains(got, code) {
			t.Fatalf("missing validation code %q in %v; errors: %+v", code, got, result.Errors)
		}
	}
}

func assertNoCode(t *testing.T, result workflow.ValidationResult, code workflow.ValidationErrorCode) {
	t.Helper()
	got := result.Codes()
	if slices.Contains(got, code) {
		t.Fatalf("unexpected validation code %q in %v; errors: %+v", code, got, result.Errors)
	}
}

func assertValidationMessage(t *testing.T, result workflow.ValidationResult, code workflow.ValidationErrorCode, nodeID workflow.NodeID, want string) {
	t.Helper()
	for _, err := range result.Errors {
		if err.Code == code && err.NodeID == nodeID {
			if err.Message != want {
				t.Fatalf("message for %s on %s = %q, want %q", code, nodeID, err.Message, want)
			}
			return
		}
	}
	t.Fatalf("missing validation error %s on %s in %+v", code, nodeID, result.Errors)
}

func assertValidationMessageOnEdge(t *testing.T, result workflow.ValidationResult, code workflow.ValidationErrorCode, edgeID workflow.EdgeID, want string) {
	t.Helper()
	for _, err := range result.Errors {
		if err.Code == code && err.EdgeID == edgeID {
			if err.Message != want {
				t.Fatalf("message for %s on %s = %q, want %q", code, edgeID, err.Message, want)
			}
			return
		}
	}
	t.Fatalf("missing validation error %s on %s in %+v", code, edgeID, result.Errors)
}

func stringOf(value string, count int) string {
	out := ""
	for range count {
		out += value
	}
	return out
}
