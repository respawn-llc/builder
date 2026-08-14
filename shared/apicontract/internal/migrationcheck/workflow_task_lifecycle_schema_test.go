package migrationcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"core/shared/apicontract"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/serverapi"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestWorkflowTaskLifecycleSchemaOwnsExactRoutesAndPolicies(t *testing.T) {
	set := buildWorkflowTaskLifecycleDescriptorSet(t)
	file := descriptorFile(t, set, "kent/api/workflow_task/lifecycle.proto")
	expected := map[string]descriptorRouteExpectation{
		protocol.MethodWorkflowTaskCreate:           lifecycleRoute("TaskLifecycleService", "Create", "CreateRequest", "CreateResult", reflect.TypeOf(serverapi.WorkflowTaskCreateRequest{}), reflect.TypeOf(serverapi.WorkflowTaskCreateResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskUpdate:           lifecycleRoute("TaskLifecycleService", "Update", "UpdateRequest", "UpdateResult", reflect.TypeOf(serverapi.WorkflowTaskUpdateRequest{}), reflect.TypeOf(serverapi.WorkflowTaskUpdateResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskStart:            lifecycleRoute("TaskLifecycleService", "Start", "StartRequest", "StartResult", reflect.TypeOf(serverapi.WorkflowTaskStartRequest{}), reflect.TypeOf(serverapi.WorkflowTaskStartResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskInterrupt:        lifecycleRoute("TaskLifecycleService", "Interrupt", "InterruptRequest", "InterruptResult", reflect.TypeOf(serverapi.WorkflowTaskInterruptRequest{}), reflect.TypeOf(serverapi.WorkflowTaskInterruptResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskResume:           lifecycleRoute("TaskLifecycleService", "Resume", "ResumeRequest", "ResumeResult", reflect.TypeOf(serverapi.WorkflowTaskResumeRequest{}), reflect.TypeOf(serverapi.WorkflowTaskResumeResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskApprove:          lifecycleRoute("TaskLifecycleService", "Approve", "ApproveRequest", "ApproveResult", reflect.TypeOf(serverapi.WorkflowTaskApproveRequest{}), reflect.TypeOf(serverapi.WorkflowTaskApproveResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskMovePreview:      lifecycleRoute("TaskLifecycleService", "PreviewMove", "MovePreviewRequest", "MovePreviewResult", reflect.TypeOf(serverapi.WorkflowTaskMovePreviewRequest{}), reflect.TypeOf(serverapi.WorkflowTaskMovePreviewResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskMove:             lifecycleRoute("TaskLifecycleService", "Move", "MoveRequest", "MoveResult", reflect.TypeOf(serverapi.WorkflowTaskMoveRequest{}), reflect.TypeOf(serverapi.WorkflowTaskMoveResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskComplete:         lifecycleRoute("TaskLifecycleService", "Complete", "CompleteRequest", "CompleteResult", reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{}), reflect.TypeOf(serverapi.WorkflowTaskCompleteResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskDelete:           lifecycleRoute("TaskLifecycleService", "Delete", "DeleteRequest", "DeleteResult", reflect.TypeOf(serverapi.WorkflowTaskDeleteRequest{}), reflect.TypeOf(struct{}{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskDependencyAdd:    lifecycleRoute("TaskDependencyService", "Add", "DependencyAddRequest", "DependencyAddResult", reflect.TypeOf(serverapi.WorkflowTaskDependencyAddRequest{}), reflect.TypeOf(serverapi.WorkflowTaskDependencyAddResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskDependencyRemove: lifecycleRoute("TaskDependencyService", "Remove", "DependencyRemoveRequest", "DependencyRemoveResult", reflect.TypeOf(serverapi.WorkflowTaskDependencyRemoveRequest{}), reflect.TypeOf(serverapi.WorkflowTaskDependencyRemoveResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskDependencyList:   lifecycleRoute("TaskDependencyService", "List", "DependencyListRequest", "DependencyListResult", reflect.TypeOf(serverapi.WorkflowTaskDependencyListRequest{}), reflect.TypeOf(serverapi.WorkflowTaskDependencyListResponse{}), apicontract.AuthPreServerAuth),
		protocol.MethodWorkflowTaskCommentAdd:       lifecycleRoute("TaskCommentService", "Add", "CommentAddRequest", "CommentAddResult", reflect.TypeOf(serverapi.WorkflowTaskCommentAddRequest{}), reflect.TypeOf(serverapi.WorkflowTaskCommentAddResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskCommentList:      lifecycleRoute("TaskCommentService", "List", "TaskOffsetPageRequest", "CommentListResult", reflect.TypeOf(serverapi.WorkflowTaskOffsetPageRequest{}), reflect.TypeOf(serverapi.WorkflowTaskCommentListResponse{}), apicontract.AuthPreServerAuth),
		protocol.MethodWorkflowTaskCommentReplace:   lifecycleRoute("TaskCommentService", "Replace", "CommentReplaceRequest", "CommentReplaceResult", reflect.TypeOf(serverapi.WorkflowTaskCommentReplaceRequest{}), reflect.TypeOf(struct{}{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskCommentDelete:    lifecycleRoute("TaskCommentService", "Delete", "CommentDeleteRequest", "CommentDeleteResult", reflect.TypeOf(serverapi.WorkflowTaskCommentDeleteRequest{}), reflect.TypeOf(struct{}{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskActivityList:     lifecycleRoute("TaskActivityService", "List", "TaskOffsetPageRequest", "ActivityListResult", reflect.TypeOf(serverapi.WorkflowTaskOffsetPageRequest{}), reflect.TypeOf(serverapi.WorkflowTaskActivityListResponse{}), apicontract.AuthPreServerAuth),
		protocol.MethodWorkflowTaskSessionList:      lifecycleRoute("TaskSessionService", "List", "TaskOffsetPageRequest", "SessionListResult", reflect.TypeOf(serverapi.WorkflowTaskOffsetPageRequest{}), reflect.TypeOf(serverapi.WorkflowTaskSessionListResponse{}), apicontract.AuthPreServerAuth),
		protocol.MethodWorkflowTaskLabelsUpdate:     lifecycleRoute("TaskLabelService", "Update", "LabelsUpdateRequest", "LabelsUpdateResult", reflect.TypeOf(serverapi.WorkflowTaskLabelsUpdateRequest{}), reflect.TypeOf(serverapi.WorkflowTaskLabelsUpdateResponse{}), apicontract.AuthServer),
		protocol.MethodWorkflowTaskObserve:          lifecycleRoute("TaskObservationService", "Observe", "ObserveRequest", "ObserveResult", reflect.TypeOf(serverapi.WorkflowTaskObservationRequest{}), reflect.TypeOf(serverapi.WorkflowTaskObservationResponse{}), apicontract.AuthServer),
	}
	assertDescriptorRoutes(t, file, expected)
	for _, service := range file.Service {
		for _, method := range service.Method {
			options, ok := proto.GetExtension(method.GetOptions(), sharedpb.E_KentMethod).(*sharedpb.KentMethodOptions)
			if !ok || options == nil {
				t.Fatalf("%s.%s has no Kent method metadata", service.GetName(), method.GetName())
			}
			route := expected[options.GetLegacyWireName()]
			if options.GetKind() != sharedpb.OperationKind_OPERATION_KIND_UNARY ||
				options.GetAuthenticationStage() != generatedAuthenticationStage(route.auth) ||
				options.GetScopePolicy() != generatedScopePolicy(route.scope) ||
				options.GetDirection() != sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER ||
				options.GetUnaryConnection() != sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED {
				t.Errorf("%s metadata = %+v, want unary %s/%s client-to-server multiplexed",
					options.GetLegacyWireName(), options, route.auth, route.scope)
			}
		}
	}
}

func TestWorkflowTaskLifecycleSchemaHasExactFieldsEnumsAndNestedDomainOutcomes(t *testing.T) {
	set := buildWorkflowTaskLifecycleDescriptorSet(t)
	file := descriptorFile(t, set, "kent/api/workflow_task/lifecycle.proto")

	for message, fields := range map[string][]string{
		"CreateRequest":             {"project_id", "workflow_id", "title", "body", "source_url", "source_workspace_id", "label_ids", "dependency_intent"},
		"UpdateRequest":             {"task_id", "title", "body", "source_workspace_id"},
		"ExecutionTargetSelection":  {"mode", "custom_ref"},
		"StartRequest":              {"task_id", "invoking_session_id", "setup_operation_id", "execution_target", "branch_name", "proceed_despite_dependencies"},
		"InterruptRequest":          {"task_id", "invoking_session_id", "session_id", "reason"},
		"ResumeRequest":             {"task_id", "invoking_session_id", "setup_operation_id", "execution_target", "branch_name"},
		"ApproveRequest":            {"approval_id", "invoking_session_id"},
		"MovePreviewRequest":        {"task_id", "target_node_id"},
		"MoveRequest":               {"task_id", "invoking_session_id", "target_node_id", "transition_key", "values", "commentary", "execution_target", "branch_name", "proceed_despite_dependencies"},
		"CompleteRequest":           {"session_id", "task_id", "transition_id", "output_values", "commentary", "actor_kind", "agent_session_id", "force"},
		"DependencyMutationSuccess": {"outcome", "blocker_task_id", "blocker_short_id", "blocked_task_id", "blocked_short_id"},
		"DependencyListSuccess":     {"task_id", "short_id", "directions"},
		"Comment":                   {"id", "task_id", "body", "author", "author_id", "created_at", "updated_at"},
		"ActivityItem":              {"activity_id", "task_id", "occurred_at", "updated_at", "comment", "session_started"},
		"SessionItem":               {"session_id", "session_name", "node_name", "agent_role", "status"},
		"SessionListSuccess":        {"task_id", "items", "next_offset"},
		"ObserveRequest":            {"task_id", "project_id", "mode"},
		"ObserveSuccess":            {"task_id", "task_short_id", "outcomes"},
	} {
		assertDescriptorMessageFields(t, file, message, fields...)
	}

	for message, fixture := range map[string]struct {
		oneof  string
		fields []string
	}{
		"StartSuccess":       {"outcome", []string{"applied", "selection_required", "dependency_confirmation_required"}},
		"ResumeSuccess":      {"outcome", []string{"applied", "selection_required", "no_op"}},
		"ApproveSuccess":     {"outcome", []string{"applied", "selection_required"}},
		"MovePreviewSuccess": {"outcome", []string{"no_op", "direct", "transition", "blocked"}},
		"MoveSuccess":        {"outcome", []string{"no_op", "applied", "selection_required", "dependency_confirmation_required"}},
		"ActivityItem":       {"activity", []string{"comment", "session_started"}},
		"ObserveOutcome":     {"outcome", []string{"done", "question", "execution_error", "interrupted"}},
	} {
		assertDescriptorOneofFields(t, file, message, fixture.oneof, fixture.fields...)
	}

	assertDescriptorEnumValues(t, file, "DependencyRole",
		"DEPENDENCY_ROLE_UNSPECIFIED", "DEPENDENCY_ROLE_BLOCKER", "DEPENDENCY_ROLE_BLOCKED")
	assertDescriptorEnumValues(t, file, "DependencyMutationOutcome",
		"DEPENDENCY_MUTATION_OUTCOME_UNSPECIFIED", "DEPENDENCY_MUTATION_OUTCOME_ADDED",
		"DEPENDENCY_MUTATION_OUTCOME_ALREADY_PRESENT", "DEPENDENCY_MUTATION_OUTCOME_REMOVED",
		"DEPENDENCY_MUTATION_OUTCOME_ALREADY_ABSENT")
	assertDescriptorEnumValues(t, file, "MovePreviewBlocker",
		"MOVE_PREVIEW_BLOCKER_UNSPECIFIED", "MOVE_PREVIEW_BLOCKER_INVALID_WORKFLOW",
		"MOVE_PREVIEW_BLOCKER_NO_SOURCE_POSITION", "MOVE_PREVIEW_BLOCKER_UNSUPPORTED_DESTINATION",
		"MOVE_PREVIEW_BLOCKER_WAITING_QUESTION", "MOVE_PREVIEW_BLOCKER_LIFECYCLE_CONFLICT",
		"MOVE_PREVIEW_BLOCKER_CONTEXT_SESSION_UNAVAILABLE", "MOVE_PREVIEW_BLOCKER_NO_USABLE_TRANSITION",
		"MOVE_PREVIEW_BLOCKER_PARALLEL_BRANCH_REQUIRES_FAN_OUT")
	assertDescriptorEnumValues(t, file, "CompleteActorKind",
		"COMPLETE_ACTOR_KIND_UNSPECIFIED", "COMPLETE_ACTOR_KIND_AGENT", "COMPLETE_ACTOR_KIND_USER")
	assertDescriptorEnumValues(t, file, "CommentAuthorKind",
		"COMMENT_AUTHOR_KIND_UNSPECIFIED", "COMMENT_AUTHOR_KIND_USER", "COMMENT_AUTHOR_KIND_AGENT")
	assertDescriptorEnumValues(t, file, "SessionStatus",
		"SESSION_STATUS_UNSPECIFIED", "SESSION_STATUS_RUNNING", "SESSION_STATUS_QUESTION", "SESSION_STATUS_IDLE")
	assertDescriptorEnumValues(t, file, "ObservationMode",
		"OBSERVATION_MODE_UNSPECIFIED", "OBSERVATION_MODE_WAIT", "OBSERVATION_MODE_WATCH")
	assertDescriptorEnumValues(t, file, "CreateSelectionReason",
		"CREATE_SELECTION_REASON_UNSPECIFIED", "CREATE_SELECTION_REASON_NO_LINKED_WORKFLOWS",
		"CREATE_SELECTION_REASON_WORKFLOW_NOT_LINKED", "CREATE_SELECTION_REASON_AMBIGUOUS_WITHOUT_DEFAULT")
	assertDescriptorEnumValues(t, file, "CreateConflictReason",
		"CREATE_CONFLICT_REASON_UNSPECIFIED", "CREATE_CONFLICT_REASON_SERIALIZATION")
	assertDescriptorEnumValues(t, file, "StartConflictReason",
		"START_CONFLICT_REASON_UNSPECIFIED", "START_CONFLICT_REASON_ALREADY_STARTED")
	assertDescriptorEnumValues(t, file, "DependencyErrorReason",
		"DEPENDENCY_ERROR_REASON_UNSPECIFIED", "DEPENDENCY_ERROR_REASON_MISSING_TASK",
		"DEPENDENCY_ERROR_REASON_SELF", "DEPENDENCY_ERROR_REASON_PROJECT_MISMATCH",
		"DEPENDENCY_ERROR_REASON_RECIPROCAL", "DEPENDENCY_ERROR_REASON_BLOCKER_LIMIT",
		"DEPENDENCY_ERROR_REASON_BLOCKED_LIMIT")
	assertDescriptorEnumValues(t, file, "LabelErrorReason",
		"LABEL_ERROR_REASON_UNSPECIFIED", "LABEL_ERROR_REASON_INVALID_NAME",
		"LABEL_ERROR_REASON_NAME_CONFLICT", "LABEL_ERROR_REASON_CATALOG_LIMIT",
		"LABEL_ERROR_REASON_PROJECT_NOT_FOUND", "LABEL_ERROR_REASON_LABEL_NOT_FOUND",
		"LABEL_ERROR_REASON_TASK_NOT_FOUND", "LABEL_ERROR_REASON_WRONG_PROJECT",
		"LABEL_ERROR_REASON_INVALID_FILTER", "LABEL_ERROR_REASON_INVALID_MUTATION")
	assertDescriptorEnumValues(t, file, "ExecutionTargetResolutionCode",
		"EXECUTION_TARGET_RESOLUTION_CODE_UNSPECIFIED", "EXECUTION_TARGET_RESOLUTION_CODE_INVALID_REVISION",
		"EXECUTION_TARGET_RESOLUTION_CODE_NON_COMMIT", "EXECUTION_TARGET_RESOLUTION_CODE_GIT_FAILURE")
	assertDescriptorEnumValues(t, file, "LockedExecutionTargetCause",
		"LOCKED_EXECUTION_TARGET_CAUSE_UNSPECIFIED", "LOCKED_EXECUTION_TARGET_CAUSE_DETACHED_HEAD",
		"LOCKED_EXECUTION_TARGET_CAUSE_INVALID_ROOT", "LOCKED_EXECUTION_TARGET_CAUSE_ROOT_INACCESSIBLE",
		"LOCKED_EXECUTION_TARGET_CAUSE_MISSING_BRANCH", "LOCKED_EXECUTION_TARGET_CAUSE_CONFLICT",
		"LOCKED_EXECUTION_TARGET_CAUSE_GIT_FAILURE")
	assertDescriptorEnumValues(t, file, "InitialBranchErrorReason",
		"INITIAL_BRANCH_ERROR_REASON_UNSPECIFIED", "INITIAL_BRANCH_ERROR_REASON_INVALID_NAME",
		"INITIAL_BRANCH_ERROR_REASON_LOCAL_COLLISION", "INITIAL_BRANCH_ERROR_REASON_REMOTE_TRACKING_COLLISION",
		"INITIAL_BRANCH_ERROR_REASON_NO_MANAGED_TARGET", "INITIAL_BRANCH_ERROR_REASON_OPERATION_CANNOT_CREATE_WORKTREE",
		"INITIAL_BRANCH_ERROR_REASON_POST_CREATION_MISMATCH")

	assertDescriptorFieldType(t, file, "CreateSuccess", "task", ".kent.api.workflow_task.TaskSummary")
	assertDescriptorFieldType(t, file, "LabelsUpdateSuccess", "assignment", ".kent.api.workflow_task.AssignedLabelIds")
	assertDescriptorOneofFields(t, file, "SelectionRequired", "reason", "policy_requires_selection", "configured_target_unavailable")
	assertDescriptorFieldType(t, file, "SelectionRequired", "configured_target_unavailable", ".kent.api.workflow_task.ConfiguredExecutionTargetUnavailableDetails")
	assertDescriptorFieldType(t, file, "MoveNoOp", "retained_previous_worktree", ".kent.api.worktree.RetainedPreviousWorktree")
	assertDescriptorFieldType(t, file, "ObserveQuestion", "question", ".kent.api.prompt.ObservationQuestion")
	assertDescriptorFieldType(t, file, "ObserveFailure", "failure", ".kent.api.prompt.LiveWatchFailure")
}

func TestWorkflowTaskLifecycleErrorsPreserveTypedLegacyFailuresAndInternalFailure(t *testing.T) {
	file := descriptorFile(t, buildWorkflowTaskLifecycleDescriptorSet(t), "kent/api/workflow_task/lifecycle.proto")
	expected := map[string][]string{
		"CreateError":           {"invalid_request", "create_selection", "create_conflict", "dependency", "label", "internal_failure"},
		"UpdateError":           {"invalid_request", "task_not_found", "internal_failure"},
		"StartError":            {"invalid_request", "task_not_found", "self_target", "start_conflict", "execution_target_resolution", "locked_execution_target", "initial_branch", "internal_failure"},
		"InterruptError":        {"invalid_request", "task_not_found", "self_target", "internal_failure"},
		"ResumeError":           {"invalid_request", "task_not_found", "self_target", "execution_target_resolution", "locked_execution_target", "initial_branch", "internal_failure"},
		"ApproveError":          {"invalid_request", "task_not_found", "self_target", "execution_target_resolution", "locked_execution_target", "internal_failure"},
		"MovePreviewError":      {"invalid_request", "task_not_found", "internal_failure"},
		"MoveError":             {"invalid_request", "task_not_found", "self_target", "execution_target_resolution", "locked_execution_target", "initial_branch", "internal_failure"},
		"CompleteError":         {"invalid_request", "task_not_found", "completion_target_not_found", "completion_selector_ambiguous", "internal_failure"},
		"DeleteError":           {"invalid_request", "task_not_found", "worktree_blocked", "internal_failure"},
		"DependencyAddError":    {"invalid_request", "dependency", "internal_failure"},
		"DependencyRemoveError": {"invalid_request", "dependency", "internal_failure"},
		"DependencyListError":   {"invalid_request", "dependency", "internal_failure"},
		"CommentAddError":       {"invalid_request", "task_not_found", "internal_failure"},
		"CommentListError":      {"invalid_request", "task_not_found", "internal_failure"},
		"CommentReplaceError":   {"invalid_request", "task_not_found", "internal_failure"},
		"CommentDeleteError":    {"invalid_request", "task_not_found", "internal_failure"},
		"ActivityListError":     {"invalid_request", "task_not_found", "internal_failure"},
		"SessionListError":      {"invalid_request", "task_not_found", "internal_failure"},
		"LabelsUpdateError":     {"invalid_request", "task_not_found", "label", "internal_failure"},
		"ObserveError":          {"invalid_request", "task_not_found", "internal_failure"},
	}
	for message, details := range expected {
		descriptor := descriptorMessage(t, file, message)
		if descriptor.Field[0].GetName() != "code" {
			t.Errorf("%s first field is not code", message)
		}
		assertDescriptorOneofFields(t, file, message, "detail", details...)
	}

	for _, result := range []string{
		"CreateResult", "UpdateResult", "StartResult", "InterruptResult", "ResumeResult",
		"ApproveResult", "MovePreviewResult", "MoveResult", "CompleteResult", "DeleteResult",
		"DependencyAddResult", "DependencyRemoveResult", "DependencyListResult",
		"CommentAddResult", "CommentListResult", "CommentReplaceResult", "CommentDeleteResult",
		"ActivityListResult", "SessionListResult", "LabelsUpdateResult", "ObserveResult",
	} {
		assertDescriptorOneofFields(t, file, result, "outcome", "success", "error")
	}
}

func TestWorkflowTaskLifecycleGeneratedValidationPreservesMutationPredicates(t *testing.T) {
	files, err := protodesc.NewFiles(buildWorkflowTaskLifecycleDescriptorSet(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, fixture := range []struct {
		message string
		fields  map[string]string
	}{
		{message: "CreateRequest", fields: map[string]string{"project_id": " ", "title": "task"}},
		{message: "CreateRequest", fields: map[string]string{"project_id": "project", "title": " "}},
		{message: "UpdateRequest", fields: map[string]string{"task_id": "task", "title": " "}},
	} {
		request := dynamicMessage(t, files, protoreflect.FullName("kent.api.workflow_task."+fixture.message))
		for field, value := range fixture.fields {
			setStringField(t, request, protoreflect.Name(field), value)
		}
		assertDynamicInvalid(t, request)
	}

	complete := dynamicMessage(t, files, "kent.api.workflow_task.CompleteRequest")
	setEnumField(t, complete, "actor_kind", 2)
	setStringField(t, complete, "task_id", "task-1")
	assertDynamicInvalid(t, complete)
	complete.Set(fds(t, complete, "force"), protoreflect.ValueOfBool(true))
	assertDynamicValid(t, complete)
	setStringField(t, complete, "session_id", "session-1")
	assertDynamicInvalid(t, complete)

	agentComplete := dynamicMessage(t, files, "kent.api.workflow_task.CompleteRequest")
	setEnumField(t, agentComplete, "actor_kind", 1)
	setStringField(t, agentComplete, "agent_session_id", "agent-session-1")
	setStringField(t, agentComplete, "task_id", "task-1")
	assertDynamicValid(t, agentComplete)

	selection := dynamicMessage(t, files, "kent.api.workflow_task.ExecutionTargetSelection")
	setEnumField(t, selection, "mode", 4)
	assertDynamicInvalid(t, selection)
	setStringField(t, selection, "custom_ref", "feature/task")
	assertDynamicValid(t, selection)

	for _, messageName := range []string{"StartRequest", "ResumeRequest"} {
		request := dynamicMessage(t, files, protoreflect.FullName("kent.api.workflow_task."+messageName))
		setStringField(t, request, "task_id", "task-1")
		setStringField(t, request, "setup_operation_id", "123e4567-e89b-12d3-a456-426614174000")
		assertDynamicInvalid(t, request)
	}

	labels := dynamicMessage(t, files, "kent.api.workflow_task.LabelsUpdateRequest")
	setStringField(t, labels, "task_id", "task-1")
	labelID := "123e4567-e89b-42d3-a456-426614174000"
	labels.Mutable(fds(t, labels, "add_label_ids")).List().Append(protoreflect.ValueOfString(labelID))
	labels.Mutable(fds(t, labels, "remove_label_ids")).List().Append(protoreflect.ValueOfString(labelID))
	assertDynamicInvalid(t, labels)

	start := dynamicMessage(t, files, "kent.api.workflow_task.StartSuccess")
	assertDynamicInvalid(t, start)
	applied := dynamicMessage(t, files, "kent.api.workflow_task.StartApplied")
	setMessageField(t, start, "applied", applied)
	assertDynamicInvalid(t, start)
	node := dynamicMessage(t, files, "kent.api.workflow_task.AttentionCurrentNode")
	setStringField(t, node, "node_id", "node-1")
	appendMessageField(t, applied, "current_nodes", node)
	assertDynamicValid(t, start)

	move := dynamicMessage(t, files, "kent.api.workflow_task.MoveRequest")
	setStringField(t, move, "task_id", "task-1")
	setStringField(t, move, "target_node_id", "node-2")
	values := dynamicMessage(t, files, "kent.api.workflow_task.NodeOutputValues")
	setStringField(t, values, "node_key", "node")
	output := dynamicMessage(t, files, "kent.api.workflow_task.NamedValue")
	setStringField(t, output, "name", "result")
	setStringField(t, output, "value", " ")
	appendMessageField(t, values, "outputs", output)
	appendMessageField(t, move, "values", values)
	assertDynamicInvalid(t, move)
	setStringField(t, output, "value", "result")
	appendMessageField(t, values, "outputs", output)
	assertDynamicInvalid(t, move)

	duplicateNodes := dynamicMessage(t, files, "kent.api.workflow_task.MoveRequest")
	setStringField(t, duplicateNodes, "task_id", "task-1")
	setStringField(t, duplicateNodes, "target_node_id", "node-2")
	firstValues := dynamicMessage(t, files, "kent.api.workflow_task.NodeOutputValues")
	setStringField(t, firstValues, "node_key", "node")
	appendMessageField(t, duplicateNodes, "values", firstValues)
	appendMessageField(t, duplicateNodes, "values", firstValues)
	assertDynamicInvalid(t, duplicateNodes)

	oversized := dynamicMessage(t, files, "kent.api.workflow_task.NamedValue")
	setStringField(t, oversized, "name", "result")
	setStringField(t, oversized, "value", strings.Repeat("x", 65537))
	assertDynamicInvalid(t, oversized)

	dependency := dynamicMessage(t, files, "kent.api.workflow_task.DependencyErrorDetails")
	setEnumField(t, dependency, "reason", 1)
	setStringField(t, dependency, "blocker_task_id", "task-1")
	setStringField(t, dependency, "blocked_task_id", "task-2")
	assertDynamicInvalid(t, dependency)
	setStringField(t, dependency, "missing_task_id", "task-2")
	assertDynamicValid(t, dependency)

	label := dynamicMessage(t, files, "kent.api.workflow_task.LabelErrorDetails")
	setEnumField(t, label, "reason", 1)
	setStringField(t, label, "project_id", "project-1")
	setStringField(t, label, "field", "title")
	assertDynamicInvalid(t, label)
	setStringField(t, label, "field", "name")
	assertDynamicValid(t, label)

	branch := dynamicMessage(t, files, "kent.api.workflow_task.InitialBranchDetails")
	setEnumField(t, branch, "reason", 2)
	setStringField(t, branch, "branch_name", "feature/task")
	assertDynamicInvalid(t, branch)
	setStringField(t, branch, "ref", "refs/heads/feature/task")
	assertDynamicValid(t, branch)
}

func TestWorkflowTaskLifecycleUsesExistingDomainSchemasWithoutDynamicPayloads(t *testing.T) {
	file := descriptorFile(t, buildWorkflowTaskLifecycleDescriptorSet(t), "kent/api/workflow_task/lifecycle.proto")
	imports := make(map[string]bool, len(file.Dependency))
	for _, dependency := range file.Dependency {
		imports[dependency] = true
	}
	for _, required := range []string{
		"kent/api/workflow_task/read.proto",
		"kent/api/workflow_task/attention.proto",
		"kent/api/workflow_definition/workflow_definition.proto",
		"kent/api/worktree/worktree.proto",
		"kent/api/prompt/prompt.proto",
		"kent/api/shared/foundation.proto",
		"kent/api/shared/validation.proto",
	} {
		if !imports[required] {
			t.Errorf("lifecycle.proto does not reuse %s", required)
		}
	}
	for _, forbidden := range []string{"google/protobuf/any.proto", "google/protobuf/struct.proto"} {
		if imports[forbidden] {
			t.Errorf("lifecycle.proto imports forbidden dynamic schema %s", forbidden)
		}
	}
	for _, message := range file.MessageType {
		for _, field := range message.Field {
			if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && field.GetTypeName() != "" {
				for _, nested := range message.NestedType {
					if nested.GetOptions().GetMapEntry() && field.GetTypeName() == ".kent.api.workflow_task."+message.GetName()+"."+nested.GetName() {
						t.Errorf("%s.%s uses a forbidden map field", message.GetName(), field.GetName())
					}
				}
			}
			if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_BYTES ||
				field.GetTypeName() == ".google.protobuf.Struct" {
				t.Errorf("%s.%s uses forbidden dynamic payload type", message.GetName(), field.GetName())
			}
			if field.GetName() == "detail_json" || field.GetName() == "raw_json" {
				t.Errorf("%s retains untyped field %s", message.GetName(), field.GetName())
			}
		}
	}
}

func lifecycleRoute(
	service string,
	method string,
	input string,
	output string,
	request reflect.Type,
	response reflect.Type,
	auth apicontract.AuthPolicy,
) descriptorRouteExpectation {
	return descriptorRouteExpectation{
		service: service, method: method,
		input: ".kent.api.workflow_task." + input, output: ".kent.api.workflow_task." + output,
		connection: "UNARY_CONNECTION_MULTIPLEXED",
		auth:       auth, scope: apicontract.ScopeProjectView, request: request, response: response,
	}
}

func buildWorkflowTaskLifecycleDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	root := repositoryRoot(t)
	output := filepath.Join(t.TempDir(), "workflow-task-lifecycle.binpb")
	command := exec.Command(
		"go", "tool", "buf", "build", root,
		"--config", filepath.Join(root, "buf.yaml"),
		"--as-file-descriptor-set",
		"--output", output,
	)
	command.Dir = filepath.Join(root, "tools", "protobuf")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Workflow Task lifecycle schema: %v\n%s", err, combined)
	}
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(encoded, &set); err != nil {
		t.Fatal(err)
	}
	return &set
}
