package migrationcheck

import (
	"testing"

	workflowpb "core/shared/protoapi/gen/kent/api/workflow_definition"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestWorkflowDefinitionExhaustiveEnumDescriptors(t *testing.T) {
	expected := map[protoreflect.Name][]protoreflect.Name{
		"NodeKind": {
			"WORKFLOW_NODE_KIND_UNSPECIFIED",
			"WORKFLOW_NODE_KIND_START",
			"WORKFLOW_NODE_KIND_AGENT",
			"WORKFLOW_NODE_KIND_SCRIPT",
			"WORKFLOW_NODE_KIND_JOIN",
			"WORKFLOW_NODE_KIND_TERMINAL",
		},
		"ValidationMode": {
			"WORKFLOW_VALIDATION_MODE_UNSPECIFIED",
			"WORKFLOW_VALIDATION_MODE_DRAFT",
			"WORKFLOW_VALIDATION_MODE_TASK_CREATION",
			"WORKFLOW_VALIDATION_MODE_EXECUTION",
		},
		"ProjectLinkDefaultMode": {
			"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_UNSPECIFIED",
			"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_NEVER",
			"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_ALWAYS",
			"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_IF_PROJECT_HAS_NONE",
		},
		"ExecutionTargetMode": {
			"WORKFLOW_EXECUTION_TARGET_MODE_UNSPECIFIED",
			"WORKFLOW_EXECUTION_TARGET_MODE_NONE",
			"WORKFLOW_EXECUTION_TARGET_MODE_HEAD",
			"WORKFLOW_EXECUTION_TARGET_MODE_DEFAULT_BRANCH",
			"WORKFLOW_EXECUTION_TARGET_MODE_CUSTOM_REF",
			"WORKFLOW_EXECUTION_TARGET_MODE_ASK_ON_FIRST_EXECUTION",
		},
		"CompletionMode": {
			"WORKFLOW_COMPLETION_MODE_UNSPECIFIED",
			"WORKFLOW_COMPLETION_MODE_AUTO",
			"WORKFLOW_COMPLETION_MODE_STRUCTURED_OUTPUT",
			"WORKFLOW_COMPLETION_MODE_TOOL",
			"WORKFLOW_COMPLETION_MODE_SHELL_COMMAND",
			"WORKFLOW_COMPLETION_MODE_UNSTRUCTURED_OUTPUT",
		},
		"AssigneeSelection": {
			"WORKFLOW_ASSIGNEE_SELECTION_UNSPECIFIED",
			"WORKFLOW_ASSIGNEE_SELECTION_CONFIGURED",
			"WORKFLOW_ASSIGNEE_SELECTION_PREVIOUS_NODE",
		},
		"ThinkingSelection": {
			"WORKFLOW_THINKING_SELECTION_UNSPECIFIED",
			"WORKFLOW_THINKING_SELECTION_CONFIGURED",
			"WORKFLOW_THINKING_SELECTION_PREVIOUS_NODE",
		},
		"ContextMode": {
			"WORKFLOW_CONTEXT_MODE_UNSPECIFIED",
			"WORKFLOW_CONTEXT_MODE_NEW_SESSION",
			"WORKFLOW_CONTEXT_MODE_CONTINUE_SESSION",
			"WORKFLOW_CONTEXT_MODE_COMPACT_AND_CONTINUE_SESSION",
		},
		"ContextSourceKind": {
			"WORKFLOW_CONTEXT_SOURCE_KIND_UNSPECIFIED",
			"WORKFLOW_CONTEXT_SOURCE_KIND_IMMEDIATE_SOURCE",
			"WORKFLOW_CONTEXT_SOURCE_KIND_SELECTED_NODE",
			"WORKFLOW_CONTEXT_SOURCE_KIND_PREVIOUS_TARGET",
			"WORKFLOW_CONTEXT_SOURCE_KIND_PREVIOUS_TARGET_OR_NEW",
		},
		"ParameterPurpose": {
			"WORKFLOW_PARAMETER_PURPOSE_UNSPECIFIED",
			"WORKFLOW_PARAMETER_PURPOSE_ORDINARY",
			"WORKFLOW_PARAMETER_PURPOSE_TARGET_ASSIGNEE",
			"WORKFLOW_PARAMETER_PURPOSE_TARGET_THINKING",
		},
		"SelectorApplicabilityReason": {
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_UNSPECIFIED",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_ELIGIBLE",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_TOPOLOGY",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_CONTEXT_SOURCE",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_NO_CALLABLE_ROLES",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_NO_THINKING_SUPPORT",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_UNAVAILABLE_CONFIGURATION",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_SOLE_CALLABLE_ROLE",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_NO_THINKING_LEVELS",
			"WORKFLOW_SELECTOR_APPLICABILITY_REASON_SOLE_THINKING_LEVEL",
		},
		"GraphEntityType": {
			"WORKFLOW_GRAPH_ENTITY_TYPE_UNSPECIFIED",
			"WORKFLOW_GRAPH_ENTITY_TYPE_EDGE",
			"WORKFLOW_GRAPH_ENTITY_TYPE_NODE",
			"WORKFLOW_GRAPH_ENTITY_TYPE_NODE_GROUP",
			"WORKFLOW_GRAPH_ENTITY_TYPE_TRANSITION_GROUP",
		},
		"RequestValidationCode": {
			"REQUEST_VALIDATION_CODE_UNSPECIFIED",
			"REQUEST_VALIDATION_CODE_REQUIRED",
			"REQUEST_VALIDATION_CODE_INVALID_KEY",
			"REQUEST_VALIDATION_CODE_INVALID_VALUE",
			"REQUEST_VALIDATION_CODE_INVALID_MODE",
			"REQUEST_VALIDATION_CODE_TOO_LONG",
		},
		"ValidationErrorCode": {
			"VALIDATION_ERROR_CODE_UNSPECIFIED",
			"VALIDATION_ERROR_CODE_MISSING_WORKFLOW_ID",
			"VALIDATION_ERROR_CODE_MISSING_NODE_ID",
			"VALIDATION_ERROR_CODE_DUPLICATE_NODE_ID",
			"VALIDATION_ERROR_CODE_MISSING_NODE_KEY",
			"VALIDATION_ERROR_CODE_INVALID_NODE_KEY",
			"VALIDATION_ERROR_CODE_DUPLICATE_NODE_KEY",
			"VALIDATION_ERROR_CODE_MISSING_START_NODE",
			"VALIDATION_ERROR_CODE_MULTIPLE_START_NODES",
			"VALIDATION_ERROR_CODE_INVALID_START_NODE",
			"VALIDATION_ERROR_CODE_INVALID_START_OUTGOING_SHAPE",
			"VALIDATION_ERROR_CODE_TERMINAL_HAS_OUTGOING_EDGE",
			"VALIDATION_ERROR_CODE_TERMINAL_IS_EXECUTABLE",
			"VALIDATION_ERROR_CODE_JOIN_IS_EXECUTABLE",
			"VALIDATION_ERROR_CODE_INVALID_JOIN_NODE",
			"VALIDATION_ERROR_CODE_INVALID_JOIN_OUTGOING_SHAPE",
			"VALIDATION_ERROR_CODE_NODE_UNREACHABLE_FROM_START",
			"VALIDATION_ERROR_CODE_NON_TERMINAL_CANNOT_REACH_TERMINAL",
			"VALIDATION_ERROR_CODE_MISSING_TRANSITION_GROUP_ID",
			"VALIDATION_ERROR_CODE_DUPLICATE_TRANSITION_GROUP_ID",
			"VALIDATION_ERROR_CODE_EMPTY_TRANSITION_GROUP",
			"VALIDATION_ERROR_CODE_MISSING_TRANSITION_ID",
			"VALIDATION_ERROR_CODE_INVALID_TRANSITION_ID",
			"VALIDATION_ERROR_CODE_DUPLICATE_TRANSITION_ID",
			"VALIDATION_ERROR_CODE_EDGE_TRANSITION_GROUP_MISSING",
			"VALIDATION_ERROR_CODE_MISSING_EDGE_ID",
			"VALIDATION_ERROR_CODE_DUPLICATE_EDGE_ID",
			"VALIDATION_ERROR_CODE_MISSING_EDGE_KEY",
			"VALIDATION_ERROR_CODE_INVALID_EDGE_KEY",
			"VALIDATION_ERROR_CODE_DUPLICATE_EDGE_KEY",
			"VALIDATION_ERROR_CODE_EDGE_TARGET_MISSING",
			"VALIDATION_ERROR_CODE_CROSS_WORKFLOW_REFERENCE",
			"VALIDATION_ERROR_CODE_INVALID_OUTPUT_FIELD",
			"VALIDATION_ERROR_CODE_DUPLICATE_OUTPUT_FIELD",
			"VALIDATION_ERROR_CODE_OUTPUT_FIELD_DESCRIPTION_REQUIRED",
			"VALIDATION_ERROR_CODE_OUTPUT_SCHEMA_TOO_LARGE",
			"VALIDATION_ERROR_CODE_INVALID_INPUT_FIELD",
			"VALIDATION_ERROR_CODE_DUPLICATE_INPUT_FIELD",
			"VALIDATION_ERROR_CODE_INPUT_FIELD_DESCRIPTION_REQUIRED",
			"VALIDATION_ERROR_CODE_INPUT_SCHEMA_TOO_LARGE",
			"VALIDATION_ERROR_CODE_INVALID_PARAMETER",
			"VALIDATION_ERROR_CODE_DUPLICATE_PARAMETER",
			"VALIDATION_ERROR_CODE_PARAMETER_DESCRIPTION_REQUIRED",
			"VALIDATION_ERROR_CODE_PARAMETER_SCHEMA_TOO_LARGE",
			"VALIDATION_ERROR_CODE_TRANSITION_PROMPT_REQUIRED",
			"VALIDATION_ERROR_CODE_TRANSITION_PROMPT_FORBIDDEN",
			"VALIDATION_ERROR_CODE_UNKNOWN_OUTPUT_REQUIREMENT",
			"VALIDATION_ERROR_CODE_INVALID_INPUT_BINDING",
			"VALIDATION_ERROR_CODE_INVALID_TEMPLATE_PLACEHOLDER",
			"VALIDATION_ERROR_CODE_PROVISION_FIELD_OVERLAP",
			"VALIDATION_ERROR_CODE_MISSING_JOIN_INPUT_PROVIDER",
			"VALIDATION_ERROR_CODE_DUPLICATE_JOIN_INPUT_PROVIDER",
			"VALIDATION_ERROR_CODE_INVALID_JOIN_INPUT_PROVIDER",
			"VALIDATION_ERROR_CODE_INVALID_FIRST_NODE_INPUT",
			"VALIDATION_ERROR_CODE_INVALID_CONTEXT_MODE",
			"VALIDATION_ERROR_CODE_INVALID_ASSIGNEE_SELECTION",
			"VALIDATION_ERROR_CODE_INVALID_THINKING_SELECTION",
			"VALIDATION_ERROR_CODE_INVALID_PARAMETER_PURPOSE",
			"VALIDATION_ERROR_CODE_MISSING_PROTECTED_PARAMETER",
			"VALIDATION_ERROR_CODE_DUPLICATE_PROTECTED_PARAMETER",
			"VALIDATION_ERROR_CODE_INVALID_CONTEXT_SOURCE",
			"VALIDATION_ERROR_CODE_INVALID_CONTINUE_SESSION_ROLE",
			"VALIDATION_ERROR_CODE_ASSIGNEE_SELECTION_INAPPLICABLE",
			"VALIDATION_ERROR_CODE_ASSIGNEE_SELECTION_UNAVAILABLE",
			"VALIDATION_ERROR_CODE_THINKING_SELECTION_INAPPLICABLE",
			"VALIDATION_ERROR_CODE_THINKING_SELECTION_UNAVAILABLE",
			"VALIDATION_ERROR_CODE_INVALID_FANOUT_JOIN_TOPOLOGY",
			"VALIDATION_ERROR_CODE_INVALID_NODE_GROUP",
			"VALIDATION_ERROR_CODE_UNSUPPORTED_CONTEXT_MODE",
			"VALIDATION_ERROR_CODE_UNSUPPORTED_APPROVAL_EXECUTION",
			"VALIDATION_ERROR_CODE_UNSUPPORTED_JOIN_EXECUTION",
			"VALIDATION_ERROR_CODE_UNSUPPORTED_JOIN_BINDING",
			"VALIDATION_ERROR_CODE_AGENT_ROLE_REQUIRED",
			"VALIDATION_ERROR_CODE_AGENT_ROLE_MISSING",
			"VALIDATION_ERROR_CODE_AGENT_ROLE_REQUIRED_TOOL_DISABLED",
			"VALIDATION_ERROR_CODE_INVALID_NODE_KIND",
			"VALIDATION_ERROR_CODE_INVALID_DISPLAY_NAME",
			"VALIDATION_ERROR_CODE_INVALID_EXECUTION_TARGET_POLICY",
			"VALIDATION_ERROR_CODE_EXECUTION_TARGET_CUSTOM_REF_REQUIRED",
			"VALIDATION_ERROR_CODE_SCRIPT_PATH_MISSING",
			"VALIDATION_ERROR_CODE_SCRIPT_PATH_RELATIVE_CHECK_SKIPPED",
			"VALIDATION_ERROR_CODE_SCRIPT_WORKTREE_ROOT_MISSING",
			"VALIDATION_ERROR_CODE_SCRIPT_PATH_NOT_FOUND",
			"VALIDATION_ERROR_CODE_SCRIPT_PATH_INACCESSIBLE",
			"VALIDATION_ERROR_CODE_SCRIPT_PATH_IS_DIRECTORY",
			"VALIDATION_ERROR_CODE_SCRIPT_PATH_NOT_EXECUTABLE",
		},
		"ProjectEventResource": {
			"WORKFLOW_PROJECT_EVENT_RESOURCE_UNSPECIFIED",
			"WORKFLOW_PROJECT_EVENT_RESOURCE_WORKFLOW",
			"WORKFLOW_PROJECT_EVENT_RESOURCE_WORKFLOW_LINK",
			"WORKFLOW_PROJECT_EVENT_RESOURCE_TASK",
			"WORKFLOW_PROJECT_EVENT_RESOURCE_LABEL",
		},
		"ProjectEventAction": {
			"WORKFLOW_PROJECT_EVENT_ACTION_UNSPECIFIED",
			"WORKFLOW_PROJECT_EVENT_ACTION_CREATED",
			"WORKFLOW_PROJECT_EVENT_ACTION_UPDATED",
			"WORKFLOW_PROJECT_EVENT_ACTION_RENAMED",
			"WORKFLOW_PROJECT_EVENT_ACTION_REORDERED",
			"WORKFLOW_PROJECT_EVENT_ACTION_DELETED",
			"WORKFLOW_PROJECT_EVENT_ACTION_GRAPH_SAVED",
			"WORKFLOW_PROJECT_EVENT_ACTION_LINKED",
			"WORKFLOW_PROJECT_EVENT_ACTION_DEFAULT_CHANGED",
			"WORKFLOW_PROJECT_EVENT_ACTION_UNLINKED",
			"WORKFLOW_PROJECT_EVENT_ACTION_STARTED",
			"WORKFLOW_PROJECT_EVENT_ACTION_INTERRUPTED",
			"WORKFLOW_PROJECT_EVENT_ACTION_RESUMED",
			"WORKFLOW_PROJECT_EVENT_ACTION_APPROVED",
			"WORKFLOW_PROJECT_EVENT_ACTION_MOVED",
			"WORKFLOW_PROJECT_EVENT_ACTION_CANCELED",
			"WORKFLOW_PROJECT_EVENT_ACTION_COMPLETED",
			"WORKFLOW_PROJECT_EVENT_ACTION_COMMENT_ADDED",
			"WORKFLOW_PROJECT_EVENT_ACTION_COMMENT_UPDATED",
			"WORKFLOW_PROJECT_EVENT_ACTION_COMMENT_DELETED",
			"WORKFLOW_PROJECT_EVENT_ACTION_QUESTION_WAITING",
			"WORKFLOW_PROJECT_EVENT_ACTION_QUESTION_CLEARED",
			"WORKFLOW_PROJECT_EVENT_ACTION_LABELS_CHANGED",
			"WORKFLOW_PROJECT_EVENT_ACTION_DEPENDENCIES_CHANGED",
		},
	}

	file := workflowpb.File_kent_api_workflow_definition_workflow_definition_proto
	enums := file.Enums()
	if enums.Len() != len(expected) {
		t.Fatalf("workflow_definition enum count = %d, want %d", enums.Len(), len(expected))
	}
	for index := 0; index < enums.Len(); index++ {
		enum := enums.Get(index)
		values, ok := expected[enum.Name()]
		if !ok {
			t.Errorf("unexpected workflow_definition enum %s", enum.FullName())
			continue
		}
		assertExactEnumValues(t, enum, values...)
		delete(expected, enum.Name())
	}
	for name := range expected {
		t.Errorf("missing workflow_definition enum %s", name)
	}
}

func TestWorkflowDefinitionExhaustiveMessageFields(t *testing.T) {
	expected := map[protoreflect.Name][]protoreflect.Name{
		"RequestValidationDetails":          {"code", "field"},
		"ExecutionTargetConfiguration":      {"mode", "custom_ref"},
		"DraftExecutionTargetConfiguration": {"mode", "custom_ref"},
		"WorkflowListProjectLink":           {"default"},
		"WorkflowRecord":                    {"id", "name", "description", "version", "execution_target_policy", "project_link"},
		"OutputField":                       {"name", "description"},
		"Parameter":                         {"key", "description", "purpose"},
		"JoinInputProvider":                 {"input_name", "provider_edge_id"},
		"DraftJoinInputProvider":            {"input_name", "provider_edge_id"},
		"OutputRequirement":                 {"field_name"},
		"InputBinding":                      {"name", "source", "field"},
		"ContextSource":                     {"kind", "node_key"},
		"WorkflowNodeGroup":                 {"group_id", "workflow_id", "group_key", "display_name", "sort_order"},
		"WorkflowNode":                      {"id", "workflow_id", "key", "kind", "display_name", "group_id", "group_key", "subagent_role", "completion_mode", "script_path", "join_input_providers"},
		"WorkflowTransitionGroup":           {"id", "workflow_id", "source_node_id", "transition_id", "display_name", "description"},
		"WorkflowEdge":                      {"id", "workflow_id", "transition_group_id", "key", "target_node_id", "assignee_selection", "thinking_selection", "requires_approval", "context_mode", "context_source", "prompt_template", "parameters", "input_bindings", "output_requirements"},
		"SelectorApplicability":             {"available", "parameter_visible", "reason"},
		"DerivedNodeWiring":                 {"node_id", "possible_provision_fields", "join_output_fields"},
		"DerivedTransitionGroupWiring":      {"transition_group_id", "required_provision_fields"},
		"DerivedEdgeWiring":                 {"edge_id", "input_bindings", "required_provision_fields", "required_provider_fields", "assignee_selection_applicability", "thinking_selection_applicability"},
		"DerivedWiring":                     {"nodes", "transition_groups", "edges", "diagnostics"},
		"WorkflowDefinition":                {"node_groups", "workflow", "nodes", "transition_groups", "edges", "derived_wiring"},
		"GraphDraftNodeGroup":               {"id", "key", "display_name"},
		"GraphDraftNode":                    {"id", "key", "kind", "display_name", "group_id", "group_key", "subagent_role", "completion_mode", "script_path", "join_input_providers"},
		"GraphDraftTransitionGroup":         {"id", "source_node_id", "transition_id", "display_name", "description"},
		"GraphDraftEdge":                    {"id", "transition_group_id", "key", "target_node_id", "assignee_selection", "thinking_selection", "requires_approval", "context_mode", "context_source", "prompt_template", "parameters"},
		"GraphDraft":                        {"node_groups", "nodes", "transition_groups", "edges"},
		"GraphMetadata":                     {"name", "description", "execution_target_policy"},
		"GraphSaveConfirmation":             {"expected_removed_node_group_count", "expected_removed_node_count", "expected_removed_transition_group_count", "expected_removed_edge_count", "expected_node_task_reference_count", "expected_edge_task_reference_count"},
		"GraphEntityReference":              {"entity_type", "entity_id"},
		"GraphSaveImpact":                   {"removed_node_group_count", "removed_node_count", "removed_transition_group_count", "removed_edge_count", "removed_entities", "node_task_reference_count", "edge_task_reference_count", "active_current_node_count", "pending_approval_count", "start_node_change_count", "last_terminal_change_count", "task_referenced_node_kind_change_count"},
		"GraphSaveBlocker":                  {"code", "message", "count", "affected_entities"},
		"WorkflowValidationErrorDetails":    {"field_name", "input_name", "placeholder", "provider_edge_id", "role", "required_tool"},
		"WorkflowValidationError":           {"code", "message", "workflow_id", "node_id", "transition_group_id", "edge_id", "details", "related_ids", "blocks_context"},
		"ValidateResponse":                  {"valid", "errors"},
		"ModeValidationResult":              {"mode", "result"},
		"CreateRequest":                     {"name", "description"},
		"CreateSuccess":                     {"workflow"},
		"CreateAndLinkProjectRequest":       {"name", "description", "project_id", "default_policy"},
		"ProjectWorkflowLink":               {"id", "project_id", "workflow_id", "default"},
		"CreateAndLinkProjectSuccess":       {"workflow", "link"},
		"UpdateRequest":                     {"workflow_id", "name", "description"},
		"GetSuccess":                        {"definition"},
		"ListRequest":                       {"offset", "limit", "query", "project_id", "workflow_id"},
		"ListSuccess":                       {"workflows", "project_id", "next_offset"},
		"GetRequest":                        {"workflow_id"},
		"LinkProjectRequest":                {"project_id", "workflow_id", "default_policy"},
		"LinkProjectSuccess":                {"link"},
		"ListProjectLinksRequest":           {"project_id"},
		"ListProjectLinksSuccess":           {"links"},
		"SetDefaultProjectLinkRequest":      {"project_id", "workflow_id"},
		"SetDefaultProjectLinkSuccess":      {"link"},
		"UnlinkProjectRequest":              {"link_id", "replacement_default_link_id"},
		"UnlinkTaskReference":               {"task_id", "short_id", "title"},
		"UnlinkProjectBlocker":              {"code", "message", "count", "tasks"},
		"UnlinkProjectSuccess":              {"link_id", "unlinked", "blockers"},
		"DeletePreviewRequest":              {"workflow_id"},
		"DeleteImpact":                      {"workflow_id", "version", "project_count", "link_count", "default_replacement_project_count", "task_count", "current_node_count", "pending_approval_count", "blocked_task_count"},
		"DeletePreviewSuccess":              {"impact"},
		"DeleteRequest":                     {"workflow_id", "confirmed", "expected_version", "expected_project_count", "expected_link_count", "expected_task_count", "cleanup_artifacts"},
		"DeleteBlocker":                     {"code", "message", "count"},
		"DeleteSuccess":                     {"deleted", "impact", "blockers"},
		"ValidateRequest":                   {"workflow_id", "mode"},
		"ScriptPathValidateRequest":         {"workflow_id", "node_id", "script_path"},
		"GraphValidateDraftRequest":         {"workflow_id", "metadata", "graph", "modes"},
		"GraphValidateDraftSuccess":         {"results", "derived_wiring"},
		"GraphDeriveWiringRequest":          {"workflow_id", "graph"},
		"GraphDeriveWiringSuccess":          {"derived_wiring"},
		"GraphSavePreviewRequest":           {"workflow_id", "expected_version", "metadata", "graph"},
		"GraphSaveRequest":                  {"workflow_id", "expected_version", "metadata", "graph", "confirmation"},
		"GraphSavePreviewSuccess":           {"current_version", "changed", "validation_results", "impact", "blockers", "can_save", "confirmation_required"},
		"GraphSaveSuccess":                  {"saved", "changed", "definition", "current_version", "validation_results", "impact", "blockers", "can_save", "confirmation_required"},
		"ProjectLabel":                      {"id", "name"},
		"ProjectLabelCatalog":               {"project_id", "labels"},
		"ProjectLabelCatalogRequest":        {"project_id"},
		"ProjectLabelCatalogSuccess":        {"catalog"},
		"ProjectLabelCreateRequest":         {"project_id", "name"},
		"ProjectLabelCreateSuccess":         {"label"},
		"ProjectLabelRenameRequest":         {"project_id", "label_id", "name"},
		"ProjectLabelRenameSuccess":         {"label"},
		"ProjectLabelDeleteRequest":         {"project_id", "label_id"},
		"ProjectLabelDeleteSuccess":         {"label_id"},
		"ProjectLabelReorderRequest":        {"project_id", "label_ids"},
		"ProjectLabelReorderSuccess":        {"catalog"},
		"LabelInvalidNameDetails":           {"project_id", "field"},
		"LabelNameConflictDetails":          {"project_id"},
		"LabelCatalogLimitDetails":          {"project_id", "limit"},
		"LabelProjectNotFoundDetails":       {"project_id"},
		"LabelNotFoundDetails":              {"project_id", "label_id"},
		"LabelInvalidMutationDetails":       {"project_id", "field"},
		"WorkflowNotFoundDetails":           {"workflow_id"},
		"ReplacementDefaultInvalidDetails":  {"link_id", "replacement_default_link_id"},
		"CreateError":                       {"code", "invalid_request", "internal_failure"},
		"CreateAndLinkProjectError":         {"code", "invalid_request", "project_not_found", "internal_failure"},
		"UpdateError":                       {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"ListError":                         {"code", "invalid_request", "internal_failure"},
		"GetError":                          {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"ProjectLinkError":                  {"code", "invalid_request", "project_not_found", "workflow_not_found", "internal_failure"},
		"ProjectLinksListError":             {"code", "invalid_request", "project_not_found", "internal_failure"},
		"SetDefaultProjectLinkError":        {"code", "invalid_request", "project_not_found", "workflow_not_found", "internal_failure"},
		"UnlinkProjectError":                {"code", "invalid_request", "replacement_default_invalid", "internal_failure"},
		"DeletePreviewError":                {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"DeleteError":                       {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"ValidateError":                     {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"GraphValidateDraftError":           {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"GraphDeriveWiringError":            {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"GraphSavePreviewError":             {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"GraphSaveError":                    {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"ProjectLabelCatalogError":          {"code", "invalid_request", "project_not_found", "internal_failure"},
		"ProjectLabelCreateError":           {"code", "invalid_name", "name_conflict", "catalog_limit", "project_not_found", "invalid_mutation", "internal_failure"},
		"ProjectLabelRenameError":           {"code", "invalid_name", "name_conflict", "label_not_found", "project_not_found", "invalid_mutation", "internal_failure"},
		"ProjectLabelDeleteError":           {"code", "label_not_found", "invalid_mutation", "project_not_found", "internal_failure"},
		"ProjectLabelReorderError":          {"code", "invalid_mutation", "project_not_found", "internal_failure"},
		"CreateResult":                      {"success", "error"},
		"CreateAndLinkProjectResult":        {"success", "error"},
		"UpdateResult":                      {"success", "error"},
		"ListResult":                        {"success", "error"},
		"GetResult":                         {"success", "error"},
		"LinkProjectResult":                 {"success", "error"},
		"ListProjectLinksResult":            {"success", "error"},
		"SetDefaultProjectLinkResult":       {"success", "error"},
		"UnlinkProjectResult":               {"success", "error"},
		"DeletePreviewResult":               {"success", "error"},
		"DeleteResult":                      {"success", "error"},
		"ValidateResult":                    {"success", "error"},
		"GraphValidateDraftResult":          {"success", "error"},
		"GraphDeriveWiringResult":           {"success", "error"},
		"GraphSavePreviewResult":            {"success", "error"},
		"GraphSaveResult":                   {"success", "error"},
		"ProjectLabelCatalogResult":         {"success", "error"},
		"ProjectLabelCreateResult":          {"success", "error"},
		"ProjectLabelRenameResult":          {"success", "error"},
		"ProjectLabelDeleteResult":          {"success", "error"},
		"ProjectLabelReorderResult":         {"success", "error"},
		"ProjectSubscribeRequest":           {"project_id"},
		"WorkflowSubscribeRequest":          {"workflow_id"},
		"SubscriptionStartError":            {"code", "invalid_request", "workflow_not_found", "internal_failure"},
		"ProjectSubscriptionStartResult":    {"success", "error"},
		"WorkflowSubscriptionStartResult":   {"success", "error"},
		"ProjectEvent":                      {"project_id", "workflow_id", "resource", "action", "primary_entity_id", "related_ids", "occurred_at"},
	}

	file := workflowpb.File_kent_api_workflow_definition_workflow_definition_proto
	messages := file.Messages()
	if messages.Len() != len(expected) {
		t.Fatalf("workflow_definition message count = %d, want %d", messages.Len(), len(expected))
	}
	for index := 0; index < messages.Len(); index++ {
		message := messages.Get(index)
		fields, ok := expected[message.Name()]
		if !ok {
			t.Errorf("unexpected workflow_definition message %s", message.FullName())
			continue
		}
		assertExactFields(t, message, fields...)
		delete(expected, message.Name())
	}
	for name := range expected {
		t.Errorf("missing workflow_definition message %s", name)
	}
}

func TestWorkflowDefinitionExhaustiveMessageOneofs(t *testing.T) {
	expected := map[protoreflect.Name]map[protoreflect.Name][]protoreflect.Name{
		"CreateError":                     {"detail": {"invalid_request", "internal_failure"}},
		"CreateAndLinkProjectError":       {"detail": {"invalid_request", "project_not_found", "internal_failure"}},
		"UpdateError":                     {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"ListError":                       {"detail": {"invalid_request", "internal_failure"}},
		"GetError":                        {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"ProjectLinkError":                {"detail": {"invalid_request", "project_not_found", "workflow_not_found", "internal_failure"}},
		"ProjectLinksListError":           {"detail": {"invalid_request", "project_not_found", "internal_failure"}},
		"SetDefaultProjectLinkError":      {"detail": {"invalid_request", "project_not_found", "workflow_not_found", "internal_failure"}},
		"UnlinkProjectError":              {"detail": {"invalid_request", "replacement_default_invalid", "internal_failure"}},
		"DeletePreviewError":              {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"DeleteError":                     {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"ValidateError":                   {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"GraphValidateDraftError":         {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"GraphDeriveWiringError":          {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"GraphSavePreviewError":           {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"GraphSaveError":                  {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"ProjectLabelCatalogError":        {"detail": {"invalid_request", "project_not_found", "internal_failure"}},
		"ProjectLabelCreateError":         {"detail": {"invalid_name", "name_conflict", "catalog_limit", "project_not_found", "invalid_mutation", "internal_failure"}},
		"ProjectLabelRenameError":         {"detail": {"invalid_name", "name_conflict", "label_not_found", "project_not_found", "invalid_mutation", "internal_failure"}},
		"ProjectLabelDeleteError":         {"detail": {"label_not_found", "invalid_mutation", "project_not_found", "internal_failure"}},
		"ProjectLabelReorderError":        {"detail": {"invalid_mutation", "project_not_found", "internal_failure"}},
		"CreateResult":                    {"outcome": {"success", "error"}},
		"CreateAndLinkProjectResult":      {"outcome": {"success", "error"}},
		"UpdateResult":                    {"outcome": {"success", "error"}},
		"ListResult":                      {"outcome": {"success", "error"}},
		"GetResult":                       {"outcome": {"success", "error"}},
		"LinkProjectResult":               {"outcome": {"success", "error"}},
		"ListProjectLinksResult":          {"outcome": {"success", "error"}},
		"SetDefaultProjectLinkResult":     {"outcome": {"success", "error"}},
		"UnlinkProjectResult":             {"outcome": {"success", "error"}},
		"DeletePreviewResult":             {"outcome": {"success", "error"}},
		"DeleteResult":                    {"outcome": {"success", "error"}},
		"ValidateResult":                  {"outcome": {"success", "error"}},
		"GraphValidateDraftResult":        {"outcome": {"success", "error"}},
		"GraphDeriveWiringResult":         {"outcome": {"success", "error"}},
		"GraphSavePreviewResult":          {"outcome": {"success", "error"}},
		"GraphSaveResult":                 {"outcome": {"success", "error"}},
		"ProjectLabelCatalogResult":       {"outcome": {"success", "error"}},
		"ProjectLabelCreateResult":        {"outcome": {"success", "error"}},
		"ProjectLabelRenameResult":        {"outcome": {"success", "error"}},
		"ProjectLabelDeleteResult":        {"outcome": {"success", "error"}},
		"ProjectLabelReorderResult":       {"outcome": {"success", "error"}},
		"SubscriptionStartError":          {"detail": {"invalid_request", "workflow_not_found", "internal_failure"}},
		"ProjectSubscriptionStartResult":  {"outcome": {"success", "error"}},
		"WorkflowSubscriptionStartResult": {"outcome": {"success", "error"}},
	}

	file := workflowpb.File_kent_api_workflow_definition_workflow_definition_proto
	messages := file.Messages()
	for index := 0; index < messages.Len(); index++ {
		message := messages.Get(index)
		oneofs := explicitWorkflowDefinitionOneofs(message)
		wanted, hasExpectedOneof := expected[message.Name()]
		if !hasExpectedOneof {
			if len(oneofs) != 0 {
				t.Errorf("%s has %d unexpected explicit oneofs", message.FullName(), len(oneofs))
			}
			continue
		}
		if len(oneofs) != len(wanted) {
			t.Errorf("%s explicit oneof count = %d, want %d", message.FullName(), len(oneofs), len(wanted))
		}
		for _, oneof := range oneofs {
			fields, ok := wanted[oneof.Name()]
			if !ok {
				t.Errorf("%s has unexpected oneof %s", message.FullName(), oneof.Name())
				continue
			}
			assertMessageOneofFields(t, message, oneof.Name(), fields...)
			delete(wanted, oneof.Name())
		}
		for name := range wanted {
			t.Errorf("%s missing oneof %s", message.FullName(), name)
		}
		delete(expected, message.Name())
	}
	for name := range expected {
		t.Errorf("missing workflow_definition oneof-bearing message %s", name)
	}
}

func explicitWorkflowDefinitionOneofs(message protoreflect.MessageDescriptor) []protoreflect.OneofDescriptor {
	oneofs := message.Oneofs()
	explicit := make([]protoreflect.OneofDescriptor, 0, oneofs.Len())
	for index := 0; index < oneofs.Len(); index++ {
		oneof := oneofs.Get(index)
		if !oneof.IsSynthetic() {
			explicit = append(explicit, oneof)
		}
	}
	return explicit
}
