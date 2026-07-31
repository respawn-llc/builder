package workflow_test

import (
	"slices"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

func TestStartNodeRules(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workflow.Definition)
		code workflow.ValidationErrorCode
	}{
		{
			name: "missing start",
			edit: func(def *workflow.Definition) {
				updateNodeAt(def, 0, func(_ *workflow.NodeIdentity, kind *workflow.NodeKind, fields *workflow.NodeFields) {
					*kind = workflow.NodeKindAgent
					fields.SubagentRole = "coder"
					fields.PromptTemplate = "Work."
				})
			},
			code: workflow.CodeMissingStartNode,
		},
		{
			name: "multiple starts",
			edit: func(def *workflow.Definition) {
				updateNodeAt(def, 1, func(_ *workflow.NodeIdentity, kind *workflow.NodeKind, _ *workflow.NodeFields) {
					*kind = workflow.NodeKindStart
				})
			},
			code: workflow.CodeMultipleStartNodes,
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
				RoleResolver: testsetup.QuestionsEnabled("coder"),
			})

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
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "a" + strings.Repeat("b", workflow.MaxParameterKeyChars), Description: "Too long."}}
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
			edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{Key: "summary", Description: strings.Repeat("a", workflow.MaxParameterDescriptionChars+1)}}
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
		{name: "agent role required", edit: func(t *testing.T, def *workflow.Definition) {
			updateNodeAt(def, 1, func(_ *workflow.NodeIdentity, _ *workflow.NodeKind, fields *workflow.NodeFields) {
				fields.SubagentRole = ""
			})
		}, code: workflow.CodeAgentRoleRequired},
		{name: "agent role missing", edit: func(t *testing.T, def *workflow.Definition) {
			updateNodeAt(def, 1, func(_ *workflow.NodeIdentity, _ *workflow.NodeKind, fields *workflow.NodeFields) {
				fields.SubagentRole = "reviewer"
			})
		}, code: workflow.CodeAgentRoleMissing},
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

		result := workflow.ValidateDefinition(def, workflow.ValidationOptions{Context: workflow.ValidationContextDraft, RoleResolver: testsetup.QuestionsEnabled("coder")})

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

	t.Run("legacy prior node placeholder is unsupported even when the node is guaranteed prior", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edgeByIDForValidationTest(t, &def, "edge_join_accept").PromptTemplate = "Accept {{.Nodes.implementation.summary}}."

		result := validateForTask(def)

		assertHasCodes(t, result, workflow.CodeInvalidTemplatePlaceholder)
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
			testAgentNode(def.ID, "node_impl_a_late", "impl_a_late", "Implement A Late", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "A late."}),
			testAgentNode(def.ID, "node_impl_b_late", "impl_b_late", "Implement B Late", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "B late."}),
			testJoinNode(def.ID, "node_join_late", "join_late", "Join Late"),
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
				def.Nodes = append(def.Nodes, testAgentNode(def.ID, "node_extra", "extra", "Extra", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Extra."}))
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
				def.Nodes = append(def.Nodes, testJoinNode(def.ID, "node_join2", "join2", "Join 2"))
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

func TestJoinOutgoingApprovalIsUnsupported(t *testing.T) {
	def := fanoutWorkflow()
	edgeByIDForValidationTest(t, &def, "edge_join_done").RequiresApproval = true

	result := validateForTask(def)

	assertHasCodes(t, result, workflow.CodeUnsupportedApprovalExecution)
}

func TestContextSourceValidation(t *testing.T) {
	t.Run("default immediate source preserves existing workflows", func(t *testing.T) {
		def := validWorkflow()

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("direct selected source node validates", func(t *testing.T) {
		def := validWorkflow()
		def.Nodes = append(def.Nodes, testAgentNode(def.ID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Review."}))
		def.TransitionGroups[1] = workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_review", SourceNodeID: "node_agent", TransitionID: "review", DisplayName: "Review"}
		def.Edges[1] = workflow.Edge{WorkflowID: def.ID, ID: "edge_review", Key: "review", TransitionGroupID: "group_review", TargetNodeID: "node_review", ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implement"}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_done", SourceNodeID: "node_review", TransitionID: "done", DisplayName: "Done"})
		def.Edges = append(def.Edges, workflow.Edge{WorkflowID: def.ID, ID: "edge_done", Key: "done", TransitionGroupID: "group_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession})

		result := validateForTask(def)

		assertNoCode(t, result, workflow.CodeInvalidContextSource)
	})

	t.Run("optional branch is invalid", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		def.Nodes = append(def.Nodes, testAgentNode(def.ID, "node_optional", "optional", "Optional", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Optional."}))
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

	selected := func(key workflow.ModelKey) workflow.ContextSource {
		return workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: key}
	}
	tests := []struct {
		name         string
		edgeID       workflow.EdgeID
		mode         workflow.ContextMode
		source       workflow.ContextSource
		roleMismatch bool
		valid        bool
	}{
		{name: "post join selected dominator", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("implementation"), valid: true},
		{name: "future selected node", edgeID: "edge_implementation_review", mode: workflow.ContextModeContinueSession, source: selected("open_pr")},
		{name: "sibling fanout branch after join", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("code_review")},
		{name: "selected start node", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("backlog")},
		{name: "selected join node", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("review_join")},
		{name: "selected terminal node", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("done")},
		{name: "missing selected node", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("missing")},
		{name: "selected target node", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("open_pr")},
		{name: "explicit source on start edge", edgeID: "edge_start", mode: workflow.ContextModeContinueSession, source: selected("implementation")},
		{name: "selected source with new session", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeNewSession, source: selected("implementation")},
		{name: "continuation role mismatch", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeContinueSession, source: selected("implementation"), roleMismatch: true, valid: true},
		{name: "compact continuation role mismatch", edgeID: "edge_accept_open_pr", mode: workflow.ContextModeCompactAndContinueSession, source: selected("implementation"), roleMismatch: true, valid: true},
		{name: "immediate source after join", edgeID: "edge_join_accept", mode: workflow.ContextModeContinueSession},
		{name: "previous target terminal target", edgeID: "edge_open_pr_done", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}},
		{name: "previous target without target dominance", edgeID: "edge_implementation_review", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}},
		{name: "previous target or new", edgeID: "edge_implementation_review", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, valid: true},
		{name: "previous target or new compact", edgeID: "edge_implementation_review", mode: workflow.ContextModeCompactAndContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, valid: true},
		{name: "previous target or new with new session", edgeID: "edge_implementation_review", mode: workflow.ContextModeNewSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}},
		{name: "previous target or new terminal target", edgeID: "edge_open_pr_done", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}},
		{name: "previous target or new on start edge", edgeID: "edge_start", mode: workflow.ContextModeContinueSession, source: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := reviewAcceptanceWorkflow()
			edge := edgeByIDForValidationTest(t, &def, tt.edgeID)
			edge.ContextMode = tt.mode
			edge.ContextSource = tt.source
			if tt.roleMismatch {
				updateNodeByKeyForValidationTest(t, &def, "open_pr", func(_ *workflow.NodeIdentity, _ *workflow.NodeKind, fields *workflow.NodeFields) {
					fields.SubagentRole = workflow.DefaultAgentRole
				})
			}
			result := validateForTask(def)
			if tt.valid {
				assertNoCode(t, result, workflow.CodeInvalidContextSource)
			} else {
				assertHasCodes(t, result, workflow.CodeInvalidContextSource)
			}
		})
	}

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

	t.Run("draft reports nonblocking context source semantics", func(t *testing.T) {
		def := reviewAcceptanceWorkflow()
		edge := edgeByIDForValidationTest(t, &def, "edge_accept_open_pr")
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "code_review"}

		result := validateDraftForTest(def)

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
			testStartNode("workflow_default", "node_start", "backlog", "Backlog"),
			testAgentNode("workflow_default", "node_agent", "implement", "Implement", workflow.NodeFields{
				SubagentRole:   "coder",
				PromptTemplate: "Implement task.",
				OutputFields:   []workflow.OutputField{{Name: "summary", Description: "Summary of completed work."}},
			}),
			testTerminalNode("workflow_default", "node_done", "done", "Done"),
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
			testStartNode("workflow_fanout", "node_start", "backlog", "Backlog"),
			testAgentNode("workflow_fanout", "node_plan", "plan", "Plan", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}),
			testAgentNode("workflow_fanout", "node_impl_a", "impl_a", "Implement A", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "A."}),
			testAgentNode("workflow_fanout", "node_impl_b", "impl_b", "Implement B", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "B."}),
			testJoinNode("workflow_fanout", "node_join", "join", "Join"),
			testTerminalNode("workflow_fanout", "node_done", "done", "Done"),
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
			testStartNode("workflow_review_acceptance", "node_start", "backlog", "Backlog"),
			testAgentNode("workflow_review_acceptance", "node_implementation", "implementation", "Implementation", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Implement.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}),
			testAgentNode("workflow_review_acceptance", "node_code_review", "code_review", "Code Review", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Review."}),
			testAgentNode("workflow_review_acceptance", "node_qa_test", "qa_test", "QA Test", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "QA."}),
			testJoinNode("workflow_review_acceptance", "node_review_join", "review_join", "Review Join"),
			testAgentNode("workflow_review_acceptance", "node_final_acceptance", "final_acceptance", "Final Acceptance", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Accept."}),
			testAgentNode("workflow_review_acceptance", "node_open_pr", "open_pr", "Open PR", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Open PR."}),
			testTerminalNode("workflow_review_acceptance", "node_done", "done", "Done"),
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
		RoleResolver: testsetup.QuestionsEnabled("coder"),
	})
}

func validateDraftForTest(def workflow.Definition) workflow.ValidationResult {
	return workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextDraft,
		RoleResolver: testsetup.QuestionsEnabled("coder"),
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

func updateNodeByKeyForValidationTest(t *testing.T, def *workflow.Definition, key workflow.ModelKey, edit func(*workflow.NodeIdentity, *workflow.NodeKind, *workflow.NodeFields)) {
	t.Helper()
	for i := range def.Nodes {
		if workflow.NodeKey(def.Nodes[i]) == key {
			updateNodeAt(def, i, edit)
			return
		}
	}
	t.Fatalf("node %q not found", key)
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
		if workflow.NodeIDOf(out[index]) == nodeID {
			out[index] = updateNode(out[index], func(identity *workflow.NodeIdentity, _ *workflow.NodeKind, _ *workflow.NodeFields) {
				identity.GroupID = groupID
			})
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
