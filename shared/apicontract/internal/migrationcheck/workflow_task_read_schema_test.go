package migrationcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"core/shared/apicontract"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	workflowtaskpb "core/shared/protoapi/gen/kent/api/workflow_task"
	"core/shared/protocol"
	"core/shared/serverapi"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestWorkflowTaskReadSchemaOwnsExactLiveRoutesAndReadContract(t *testing.T) {
	descriptorSet := buildWorkflowTaskReadDescriptorSet(t)
	file := descriptorFile(t, descriptorSet, "kent/api/workflow_task/read.proto")

	expectedRoutes := map[string]descriptorRouteExpectation{
		protocol.MethodWorkflowTaskList: {
			service:    "TaskReadService",
			method:     "List",
			input:      ".kent.api.workflow_task.ListRequest",
			output:     ".kent.api.workflow_task.ListResult",
			connection: "UNARY_CONNECTION_MULTIPLEXED",
			auth:       apicontract.AuthPreServerAuth,
			scope:      apicontract.ScopeProjectView,
			request:    reflect.TypeOf(serverapi.WorkflowTaskListRequest{}),
			response:   reflect.TypeOf(serverapi.WorkflowTaskListResponse{}),
		},
		protocol.MethodWorkflowTaskSearch: {
			service:    "TaskReadService",
			method:     "Search",
			input:      ".kent.api.workflow_task.SearchRequest",
			output:     ".kent.api.workflow_task.SearchResult",
			connection: "UNARY_CONNECTION_DEDICATED",
			auth:       apicontract.AuthServer,
			scope:      apicontract.ScopeNone,
			request:    reflect.TypeOf(serverapi.TaskSearchRequest{}),
			response:   reflect.TypeOf(serverapi.TaskSearchResponse{}),
		},
		protocol.MethodWorkflowTaskGet: {
			service:    "TaskReadService",
			method:     "Get",
			input:      ".kent.api.workflow_task.GetRequest",
			output:     ".kent.api.workflow_task.GetResult",
			connection: "UNARY_CONNECTION_MULTIPLEXED",
			auth:       apicontract.AuthPreServerAuth,
			scope:      apicontract.ScopeProjectView,
			request:    reflect.TypeOf(serverapi.WorkflowTaskGetRequest{}),
			response:   reflect.TypeOf(serverapi.WorkflowTaskGetResponse{}),
		},
		protocol.MethodWorkflowBoardGet: {
			service:    "BoardReadService",
			method:     "Get",
			input:      ".kent.api.workflow_task.BoardGetRequest",
			output:     ".kent.api.workflow_task.BoardGetResult",
			connection: "UNARY_CONNECTION_MULTIPLEXED",
			auth:       apicontract.AuthPreServerAuth,
			scope:      apicontract.ScopeProjectView,
			request:    reflect.TypeOf(serverapi.WorkflowBoardRequest{}),
			response:   reflect.TypeOf(serverapi.WorkflowBoardResponse{}),
		},
		protocol.MethodWorkflowBoardNodeCardsList: {
			service:    "BoardReadService",
			method:     "ListNodeCards",
			input:      ".kent.api.workflow_task.BoardNodeCardsListRequest",
			output:     ".kent.api.workflow_task.BoardNodeCardsListResult",
			connection: "UNARY_CONNECTION_MULTIPLEXED",
			auth:       apicontract.AuthPreServerAuth,
			scope:      apicontract.ScopeProjectView,
			request:    reflect.TypeOf(serverapi.WorkflowBoardNodeCardsListRequest{}),
			response:   reflect.TypeOf(serverapi.WorkflowBoardNodeCardsListResponse{}),
		},
		protocol.MethodWorkflowTaskLabelsGet: {
			service:    "TaskLabelReadService",
			method:     "Get",
			input:      ".kent.api.workflow_task.LabelsGetRequest",
			output:     ".kent.api.workflow_task.LabelsGetResult",
			connection: "UNARY_CONNECTION_MULTIPLEXED",
			auth:       apicontract.AuthPreServerAuth,
			scope:      apicontract.ScopeProjectView,
			request:    reflect.TypeOf(serverapi.WorkflowTaskLabelsGetRequest{}),
			response:   reflect.TypeOf(serverapi.WorkflowTaskLabelsGetResponse{}),
		},
	}
	assertDescriptorRoutes(t, file, expectedRoutes)

	assertDescriptorMessageFields(t, file, "ListRequest",
		"project_id", "workflow_id", "column_keys", "status_kinds", "attention_kinds",
		"label_filter", "dependency_filter", "sort", "offset", "limit")
	assertDescriptorMessageFields(t, file, "ListSuccess",
		"scope", "matching_workflow_cardinality", "next_offset", "generated_at", "tasks")
	assertDescriptorMessageFields(t, file, "ListItem",
		"task_id", "short_id", "workflow_id", "workflow_name", "title", "created_at",
		"updated_at", "column_keys", "status", "label_ids")
	assertDescriptorMessageFields(t, file, "SearchRequest",
		"mode", "query", "context", "case_sensitive", "include_comments", "project_ids",
		"status_kinds", "page_size", "offset")
	assertDescriptorMessageFields(t, file, "SearchHit",
		"ordinal", "source", "literal", "fts5")
	assertDescriptorOneofFields(t, file, "SearchHit", "match", "literal", "fts5")
	assertDescriptorMessageFields(t, file, "Board",
		"project_id", "project", "selected_workflow", "workflows", "groups", "columns", "generated_at")
	assertDescriptorMessageFields(t, file, "BoardTaskCard",
		"task_id", "short_id", "title", "preview", "workflow_id", "active_node_ids",
		"source_workspace", "status", "actions", "label_ids", "dependency_progress", "updated_at")
	assertDescriptorMessageFields(t, file, "TaskDetail",
		"summary", "project", "workflow", "body", "source_url", "source_workspace",
		"execution_target", "worktree_path", "current_nodes", "live_sessions", "current_scripts",
		"retained_session_count", "status", "actions", "label_ids", "attention_count", "dependencies")
	assertDescriptorMessageFields(t, file, "LabelsGetSuccess", "assignment")
	assertDescriptorFieldType(t, file, "ListItem", "column_keys", ".kent.api.workflow_task.ColumnKeys")
	assertDescriptorOneofFields(t, file, "LabelFilter", "filter", "none", "named", "unlabeled")

	assertDescriptorEnumValues(t, file, "TaskStatusKind",
		"TASK_STATUS_KIND_UNSPECIFIED",
		"TASK_STATUS_KIND_DONE",
		"TASK_STATUS_KIND_WAITING_QUESTION",
		"TASK_STATUS_KIND_WAITING_APPROVAL",
		"TASK_STATUS_KIND_INTERRUPTED",
		"TASK_STATUS_KIND_RUNNING",
		"TASK_STATUS_KIND_QUEUED",
		"TASK_STATUS_KIND_BACKLOG",
		"TASK_STATUS_KIND_ACTIVE")
	assertDescriptorEnumValues(t, file, "TaskNativeState",
		"TASK_NATIVE_STATE_UNSPECIFIED",
		"TASK_NATIVE_STATE_TERMINAL",
		"TASK_NATIVE_STATE_WAITING_ASK",
		"TASK_NATIVE_STATE_WAITING_APPROVAL",
		"TASK_NATIVE_STATE_INTERRUPTED",
		"TASK_NATIVE_STATE_RUNNING",
		"TASK_NATIVE_STATE_QUEUED",
		"TASK_NATIVE_STATE_ACTIVE")
	assertDescriptorEnumValues(t, file, "TaskAttentionKind",
		"TASK_ATTENTION_KIND_UNSPECIFIED",
		"TASK_ATTENTION_KIND_QUESTION",
		"TASK_ATTENTION_KIND_APPROVAL",
		"TASK_ATTENTION_KIND_INTERRUPTED")
	assertDescriptorEnumValues(t, file, "ListSortField",
		"LIST_SORT_FIELD_UNSPECIFIED",
		"LIST_SORT_FIELD_CREATED",
		"LIST_SORT_FIELD_UPDATED",
		"LIST_SORT_FIELD_STATUS",
		"LIST_SORT_FIELD_COLUMN",
		"LIST_SORT_FIELD_TITLE",
		"LIST_SORT_FIELD_LABELS",
		"LIST_SORT_FIELD_SHORT_ID")
	assertDescriptorEnumValues(t, file, "SearchMode",
		"SEARCH_MODE_UNSPECIFIED",
		"SEARCH_MODE_LITERAL",
		"SEARCH_MODE_FTS5")
	assertDescriptorEnumValues(t, file, "SearchSourceKind",
		"SEARCH_SOURCE_KIND_UNSPECIFIED",
		"SEARCH_SOURCE_KIND_SHORT_ID",
		"SEARCH_SOURCE_KIND_TITLE",
		"SEARCH_SOURCE_KIND_BODY",
		"SEARCH_SOURCE_KIND_COMMENT")
	assertDescriptorEnumValues(t, file, "ListSortDirection",
		"LIST_SORT_DIRECTION_UNSPECIFIED",
		"LIST_SORT_DIRECTION_ASC",
		"LIST_SORT_DIRECTION_DESC")
	assertDescriptorEnumValues(t, file, "MatchingWorkflowCardinality",
		"MATCHING_WORKFLOW_CARDINALITY_UNSPECIFIED",
		"MATCHING_WORKFLOW_CARDINALITY_NONE",
		"MATCHING_WORKFLOW_CARDINALITY_ONE",
		"MATCHING_WORKFLOW_CARDINALITY_MULTIPLE")
	assertDescriptorEnumValues(t, file, "ListScopeErrorReason",
		"LIST_SCOPE_ERROR_REASON_UNSPECIFIED",
		"LIST_SCOPE_ERROR_REASON_NO_LINKED_WORKFLOWS",
		"LIST_SCOPE_ERROR_REASON_WORKFLOW_NOT_LINKED",
		"LIST_SCOPE_ERROR_REASON_WORKFLOW_REQUIRED_FOR_COLUMNS")
	assertDescriptorEnumValues(t, file, "NamedLabelFilterMode",
		"NAMED_LABEL_FILTER_MODE_UNSPECIFIED",
		"NAMED_LABEL_FILTER_MODE_ANY",
		"NAMED_LABEL_FILTER_MODE_ALL")
	assertDescriptorEnumValues(t, file, "ExecutionTargetProvenance",
		"EXECUTION_TARGET_PROVENANCE_UNSPECIFIED",
		"EXECUTION_TARGET_PROVENANCE_RESOLVED",
		"EXECUTION_TARGET_PROVENANCE_LEGACY_OBSERVED")
	assertDescriptorEnumValues(t, file, "DependencyDirection",
		"DEPENDENCY_DIRECTION_UNSPECIFIED",
		"DEPENDENCY_DIRECTION_BLOCKED_BY",
		"DEPENDENCY_DIRECTION_BLOCKS")
	assertDescriptorEnumValues(t, file, "DependencySatisfaction",
		"DEPENDENCY_SATISFACTION_UNSPECIFIED",
		"DEPENDENCY_SATISFACTION_SATISFIED",
		"DEPENDENCY_SATISFACTION_UNSATISFIED")
	assertDescriptorOneofFields(t, file, "DependencyAddAvailability", "availability", "available", "limit_reached")

	for _, result := range []string{
		"ListResult",
		"SearchResult",
		"GetResult",
		"BoardGetResult",
		"BoardNodeCardsListResult",
		"LabelsGetResult",
	} {
		assertDescriptorOneofFields(t, file, result, "outcome", "success", "error")
	}
	assertDescriptorOneofFields(t, file, "ListError", "detail",
		"invalid_request", "scope_error", "invalid_filter", "label_not_found",
		"wrong_project", "project_not_found", "internal_failure")
	assertDescriptorOneofFields(t, file, "SearchError", "detail",
		"invalid_request", "normalized_too_short", "internal_failure")
	assertDescriptorOneofFields(t, file, "GetError", "detail",
		"invalid_request", "task_not_found", "internal_failure")
	assertDescriptorOneofFields(t, file, "BoardGetError", "detail",
		"invalid_request", "invalid_filter", "label_not_found", "wrong_project",
		"project_not_found", "internal_failure")
	assertDescriptorOneofFields(t, file, "BoardNodeCardsListError", "detail",
		"invalid_request", "invalid_filter", "label_not_found", "wrong_project",
		"project_not_found", "internal_failure")
	assertDescriptorOneofFields(t, file, "LabelsGetError", "detail",
		"invalid_request", "task_not_found", "internal_failure")

	assertDescriptorFieldType(t, file, "TaskSourceWorkspace", "availability", ".kent.api.project.ProjectAvailability")
	assertDescriptorMessageFields(t, file, "TaskSourceWorkspace",
		"workspace_id", "display_name", "root_path", "availability", "is_primary", "updated_at")
	assertDescriptorMessageFields(t, descriptorFile(t, descriptorSet, "kent/api/project/project.proto"), "ProjectWorkspaceSummary",
		"workspace_id", "display_name", "root_path", "availability", "is_primary", "session_count", "updated_at")
	assertDescriptorFieldType(t, file, "ExecutionTarget", "mode", ".kent.api.workflow_definition.ExecutionTargetMode")
	assertDescriptorFieldType(t, file, "CurrentScript", "current_node", ".kent.api.workflow_task.AttentionCurrentNode")
	assertDescriptorFieldType(t, file, "BoardTaskCard", "source_workspace", ".kent.api.workflow_task.TaskSourceWorkspace")
	assertDescriptorFieldType(t, file, "TaskDetail", "source_workspace", ".kent.api.workflow_task.TaskSourceWorkspace")
	assertDescriptorFieldType(t, file, "WorkflowPickerItem", "validation_errors", ".kent.api.workflow_definition.WorkflowValidationError")
	assertDescriptorFieldType(t, file, "BoardNodeSummary", "output_fields", ".kent.api.workflow_definition.OutputField")
	assertDescriptorFieldType(t, file, "LabelsGetSuccess", "assignment", ".kent.api.workflow_task.AssignedLabelIds")

	imports := map[string]bool{}
	for _, dependency := range file.Dependency {
		imports[dependency] = true
	}
	for _, required := range []string{
		"kent/api/project/project.proto",
		"kent/api/workflow_definition/workflow_definition.proto",
		"kent/api/shared/foundation.proto",
	} {
		if !imports[required] {
			t.Errorf("read.proto does not import reusable schema %s", required)
		}
	}

	for _, forbidden := range []string{"google/protobuf/any.proto", "google/protobuf/struct.proto"} {
		if imports[forbidden] {
			t.Errorf("read.proto imports forbidden dynamic schema %s", forbidden)
		}
	}
	for _, message := range file.MessageType {
		for _, field := range message.Field {
			if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_BYTES ||
				field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && field.GetTypeName() == ".google.protobuf.Struct" {
				t.Errorf("%s.%s uses forbidden dynamic payload type", message.GetName(), field.GetName())
			}
		}
	}
}

func TestWorkflowTaskReadGeneratedValidationPreservesReadPredicates(t *testing.T) {
	set := buildWorkflowTaskReadDescriptorSet(t)
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}

	noneFilter := dynamicMessage(t, files, "kent.api.workflow_task.LabelFilter")
	setMessageField(t, noneFilter, "none", dynamicMessage(t, files, "google.protobuf.Empty"))
	assertDynamicValid(t, noneFilter)
	assertDynamicInvalid(t, dynamicMessage(t, files, "kent.api.workflow_task.LabelFilter"))

	list := dynamicMessage(t, files, "kent.api.workflow_task.ListRequest")
	setStringField(t, list, "project_id", "project-1")
	setMessageField(t, list, "label_filter", noneFilter)
	appendEnumField(t, list, "status_kinds", 1)
	appendEnumField(t, list, "status_kinds", 1)
	appendEnumField(t, list, "attention_kinds", 1)
	appendEnumField(t, list, "attention_kinds", 1)
	assertDynamicValid(t, list)
	sort := dynamicMessage(t, files, "kent.api.workflow_task.ListSort")
	setEnumField(t, sort, "field", 1)
	setEnumField(t, sort, "direction", 1)
	appendMessageField(t, list, "sort", sort)
	appendMessageField(t, list, "sort", sort)
	assertDynamicInvalid(t, list)

	scope := dynamicMessage(t, files, "kent.api.workflow_task.ListScope")
	setStringField(t, scope, "project_id", "project-1")
	setStringField(t, scope, "workflow_id", validWorkflowTaskReadUUID)
	listSuccess := dynamicMessage(t, files, "kent.api.workflow_task.ListSuccess")
	setMessageField(t, listSuccess, "scope", scope)
	setEnumField(t, listSuccess, "matching_workflow_cardinality", 3)
	setMessageField(t, listSuccess, "generated_at", validTimestamp(t, files))
	assertDynamicInvalid(t, listSuccess)

	get := dynamicMessage(t, files, "kent.api.workflow_task.GetRequest")
	setStringField(t, get, "task_id", "task-1")
	setStringField(t, get, "project_id", "project-1")
	assertDynamicValid(t, get)
	setStringField(t, get, "project_id", " project-1")
	assertDynamicInvalid(t, get)
	assertDynamicInvalid(t, dynamicMessage(t, files, "kent.api.workflow_task.GetRequest"))

	status := validTaskStatus(t, files)
	assertDynamicValid(t, status)
	setEnumField(t, status, "native_state", 7)
	assertDynamicInvalid(t, status)

	literalHit := validSearchHit(t, files, true, 1, 1)
	literalSuccess := validSearchSuccess(t, files, literalHit, 1)
	assertDynamicValid(t, literalSuccess)

	ftsShortIDHit := validSearchHit(t, files, false, 1, 1)
	ftsSuccess := validSearchSuccess(t, files, ftsShortIDHit, 2)
	assertDynamicInvalid(t, ftsSuccess)

	ftsBodyHit := validSearchHit(t, files, false, 3, 1)
	ftsSuccess = validSearchSuccess(t, files, ftsBodyHit, 2)
	assertDynamicValid(t, ftsSuccess)
	group := ftsSuccess.Get(fds(t, ftsSuccess, "groups")).List().Get(0).Message()
	second := validSearchHit(t, files, false, 3, 1)
	appendMessageField(t, group, "hits", second)
	setInt32Field(t, group, "total_hit_count", 2)
	assertDynamicInvalid(t, ftsSuccess)

	descending := validSearchSuccess(t, files, validSearchHit(t, files, false, 3, 2), 2)
	descendingGroup := descending.Get(fds(t, descending, "groups")).List().Get(0).Message()
	appendMessageField(t, descendingGroup, "hits", validSearchHit(t, files, false, 3, 1))
	setInt32Field(t, descendingGroup, "total_hit_count", 2)
	assertSearchHitOrdinalsStrictlyAscending(t, descending)

	duplicateGroups := validSearchSuccess(t, files, validSearchHit(t, files, true, 1, 1), 1)
	firstGroup := duplicateGroups.Get(fds(t, duplicateGroups, "groups")).List().Get(0).Message()
	appendReflectedMessageField(t, duplicateGroups, "groups", firstGroup)
	assertDynamicInvalid(t, duplicateGroups)

	dependencies := validTaskDependencies(t, files)
	assertDynamicValid(t, dependencies)
	setInt32Field(t, dependencies, "blocker_count", 1)
	assertDynamicInvalid(t, dependencies)
}

func TestWorkflowTaskReadGeneratedValidationRequiresLabelUUIDV4(t *testing.T) {
	versionOneUUID := "123e4567-e89b-12d3-a456-426614174000"
	for name, message := range map[string]proto.Message{
		"named filter": &workflowtaskpb.NamedLabelFilter{
			Mode:     workflowtaskpb.NamedLabelFilterMode_NAMED_LABEL_FILTER_MODE_ANY,
			LabelIds: []string{versionOneUUID},
		},
		"assigned labels": &workflowtaskpb.AssignedLabelIds{
			TaskId:   "task-1",
			LabelIds: []string{versionOneUUID},
		},
		"missing label detail": &workflowtaskpb.LabelNotFoundDetails{
			ProjectId: "project",
			LabelId:   versionOneUUID,
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err == nil {
			t.Fatalf("%s accepted a non-v4 label ID", name)
		}
	}
}

const validWorkflowTaskReadUUID = "123e4567-e89b-42d3-a456-426614174000"

type descriptorRouteExpectation struct {
	service    string
	method     string
	input      string
	output     string
	connection string
	auth       apicontract.AuthPolicy
	scope      apicontract.ScopePolicy
	request    reflect.Type
	response   reflect.Type
}

func dynamicMessage(t *testing.T, files *protoregistry.Files, name protoreflect.FullName) *dynamicpb.Message {
	t.Helper()
	descriptor, err := files.FindDescriptorByName(name)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := descriptor.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s is %T, want message descriptor", name, descriptor)
	}
	return dynamicpb.NewMessage(message)
}

func assertDynamicValid(t *testing.T, message proto.Message) {
	t.Helper()
	if err := protovalidate.Validate(message); err != nil {
		t.Fatalf("%s should be valid: %v", message.ProtoReflect().Descriptor().FullName(), err)
	}
}

func assertDynamicInvalid(t *testing.T, message proto.Message) {
	t.Helper()
	if err := protovalidate.Validate(message); err == nil {
		t.Fatalf("%s should be invalid", message.ProtoReflect().Descriptor().FullName())
	}
}

func fds(t *testing.T, message protoreflect.Message, name protoreflect.Name) protoreflect.FieldDescriptor {
	t.Helper()
	field := message.Descriptor().Fields().ByName(name)
	if field == nil {
		t.Fatalf("%s.%s not found", message.Descriptor().FullName(), name)
	}
	return field
}

func setStringField(t *testing.T, message protoreflect.Message, name protoreflect.Name, value string) {
	t.Helper()
	message.Set(fds(t, message, name), protoreflect.ValueOfString(value))
}

func setInt32Field(t *testing.T, message protoreflect.Message, name protoreflect.Name, value int32) {
	t.Helper()
	message.Set(fds(t, message, name), protoreflect.ValueOfInt32(value))
}

func setEnumField(t *testing.T, message protoreflect.Message, name protoreflect.Name, value protoreflect.EnumNumber) {
	t.Helper()
	message.Set(fds(t, message, name), protoreflect.ValueOfEnum(value))
}

func setMessageField(t *testing.T, message protoreflect.Message, name protoreflect.Name, value protoreflect.ProtoMessage) {
	t.Helper()
	message.Set(fds(t, message, name), protoreflect.ValueOfMessage(value.ProtoReflect()))
}

func appendEnumField(t *testing.T, message protoreflect.Message, name protoreflect.Name, value protoreflect.EnumNumber) {
	t.Helper()
	message.Mutable(fds(t, message, name)).List().Append(protoreflect.ValueOfEnum(value))
}

func appendMessageField(t *testing.T, message protoreflect.Message, name protoreflect.Name, value protoreflect.ProtoMessage) {
	t.Helper()
	message.Mutable(fds(t, message, name)).List().Append(protoreflect.ValueOfMessage(value.ProtoReflect()))
}

func appendReflectedMessageField(t *testing.T, message protoreflect.Message, name protoreflect.Name, value protoreflect.Message) {
	t.Helper()
	message.Mutable(fds(t, message, name)).List().Append(protoreflect.ValueOfMessage(value))
}

func validTimestamp(t *testing.T, files *protoregistry.Files) *dynamicpb.Message {
	t.Helper()
	timestamp := dynamicMessage(t, files, "google.protobuf.Timestamp")
	timestamp.Set(fds(t, timestamp, "seconds"), protoreflect.ValueOfInt64(1))
	return timestamp
}

func validTaskStatus(t *testing.T, files *protoregistry.Files) *dynamicpb.Message {
	t.Helper()
	status := dynamicMessage(t, files, "kent.api.workflow_task.TaskStatus")
	setEnumField(t, status, "kind", 1)
	setEnumField(t, status, "native_state", 1)
	return status
}

func validSearchHit(
	t *testing.T,
	files *protoregistry.Files,
	literal bool,
	sourceKind protoreflect.EnumNumber,
	ordinal int32,
) *dynamicpb.Message {
	t.Helper()
	source := dynamicMessage(t, files, "kent.api.workflow_task.SearchSource")
	setEnumField(t, source, "kind", sourceKind)
	hit := dynamicMessage(t, files, "kent.api.workflow_task.SearchHit")
	setInt32Field(t, hit, "ordinal", ordinal)
	setMessageField(t, hit, "source", source)
	if literal {
		detail := dynamicMessage(t, files, "kent.api.workflow_task.SearchLiteralHit")
		setStringField(t, detail, "match", "task")
		setMessageField(t, hit, "literal", detail)
	} else {
		detail := dynamicMessage(t, files, "kent.api.workflow_task.SearchFts5Hit")
		setStringField(t, detail, "snippet", "task")
		setMessageField(t, hit, "fts5", detail)
	}
	return hit
}

func validSearchSuccess(
	t *testing.T,
	files *protoregistry.Files,
	hit *dynamicpb.Message,
	mode protoreflect.EnumNumber,
) *dynamicpb.Message {
	t.Helper()
	group := dynamicMessage(t, files, "kent.api.workflow_task.SearchGroup")
	setStringField(t, group, "project_id", "project-1")
	setStringField(t, group, "project_key", "KENT")
	setStringField(t, group, "task_id", "task-1")
	setStringField(t, group, "short_id", "KENT-1")
	setStringField(t, group, "workflow_id", validWorkflowTaskReadUUID)
	setStringField(t, group, "title", "Task")
	setMessageField(t, group, "status", validTaskStatus(t, files))
	setInt32Field(t, group, "total_hit_count", 1)
	appendMessageField(t, group, "hits", hit)
	success := dynamicMessage(t, files, "kent.api.workflow_task.SearchSuccess")
	setEnumField(t, success, "mode", mode)
	appendMessageField(t, success, "groups", group)
	return success
}

func validTaskDependencies(t *testing.T, files *protoregistry.Files) *dynamicpb.Message {
	t.Helper()
	available := dynamicMessage(t, files, "kent.api.workflow_task.DependencyAvailable")
	setInt32Field(t, available, "remaining_capacity", 1)
	availability := dynamicMessage(t, files, "kent.api.workflow_task.DependencyAddAvailability")
	setMessageField(t, availability, "available", available)
	direction := func(kind protoreflect.EnumNumber, blockedBy bool) *dynamicpb.Message {
		projection := dynamicMessage(t, files, "kent.api.workflow_task.DependencyDirectionProjection")
		setEnumField(t, projection, "direction", kind)
		setInt32Field(t, projection, "total_count", 0)
		if blockedBy {
			setInt32Field(t, projection, "unsatisfied_count", 0)
		}
		setMessageField(t, projection, "add_availability", availability)
		return projection
	}
	dependencies := dynamicMessage(t, files, "kent.api.workflow_task.TaskDependencies")
	appendMessageField(t, dependencies, "directions", direction(1, true))
	appendMessageField(t, dependencies, "directions", direction(2, false))
	return dependencies
}

func assertSearchHitOrdinalsStrictlyAscending(t *testing.T, success protoreflect.Message) {
	t.Helper()
	groups := success.Get(fds(t, success, "groups")).List()
	for groupIndex := 0; groupIndex < groups.Len(); groupIndex++ {
		hits := groups.Get(groupIndex).Message().Get(
			fds(t, groups.Get(groupIndex).Message(), "hits"),
		).List()
		var previous int64
		for hitIndex := 0; hitIndex < hits.Len(); hitIndex++ {
			hit := hits.Get(hitIndex).Message()
			ordinal := hit.Get(fds(t, hit, "ordinal")).Int()
			if hitIndex > 0 && previous >= ordinal {
				return
			}
			previous = ordinal
		}
	}
	t.Fatal("descending Search hit ordinals were accepted by the migration fixture")
}

func buildWorkflowTaskReadDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	root := repositoryRoot(t)
	output := filepath.Join(t.TempDir(), "workflow-task-read.binpb")
	command := exec.Command(
		"go", "tool", "buf", "build", root,
		"--config", filepath.Join(root, "buf.yaml"),
		"--as-file-descriptor-set",
		"--output", output,
	)
	command.Dir = filepath.Join(root, "tools", "protobuf")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Workflow Task read schema: %v\n%s", err, combined)
	}
	bytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(bytes, &set); err != nil {
		t.Fatal(err)
	}
	return &set
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../.."))
}

func descriptorFile(t *testing.T, set *descriptorpb.FileDescriptorSet, name string) *descriptorpb.FileDescriptorProto {
	t.Helper()
	for _, file := range set.File {
		if file.GetName() == name {
			return file
		}
	}
	t.Fatalf("descriptor file %s not found", name)
	return nil
}

func assertDescriptorRoutes(t *testing.T, file *descriptorpb.FileDescriptorProto, expected map[string]descriptorRouteExpectation) {
	t.Helper()
	found := map[string]bool{}
	for _, service := range file.Service {
		for _, method := range service.Method {
			if !proto.HasExtension(method.GetOptions(), sharedpb.E_KentMethod) {
				t.Errorf("%s.%s has no Kent method options", service.GetName(), method.GetName())
				continue
			}
			options, ok := proto.GetExtension(method.GetOptions(), sharedpb.E_KentMethod).(*sharedpb.KentMethodOptions)
			if !ok || options.LegacyWireName == nil {
				t.Errorf("%s.%s has invalid Kent method options", service.GetName(), method.GetName())
				continue
			}
			legacy := options.GetLegacyWireName()
			route, exists := expected[legacy]
			if !exists {
				t.Errorf("unexpected legacy route %s", legacy)
				continue
			}
			if found[legacy] {
				t.Errorf("duplicate legacy route %s", legacy)
				continue
			}
			if service.GetName() != route.service ||
				method.GetName() != route.method ||
				method.GetInputType() != route.input ||
				method.GetOutputType() != route.output {
				t.Errorf("%s descriptor = %s.%s(%s) returns (%s), want %s.%s(%s) returns (%s)",
					legacy,
					service.GetName(), method.GetName(), method.GetInputType(), method.GetOutputType(),
					route.service, route.method, route.input, route.output)
			}
			if options.GetUnaryConnection().String() != route.connection {
				t.Errorf("%s unary connection = %s, want %s", legacy, options.GetUnaryConnection(), route.connection)
			}
			found[legacy] = true
		}
	}
	for legacy := range expected {
		if !found[legacy] {
			t.Errorf("missing exact descriptor route for %s", legacy)
		}
	}
	if len(found) != len(expected) {
		t.Fatalf("found routes = %d, want %d", len(found), len(expected))
	}
}

func descriptorMessage(t *testing.T, file *descriptorpb.FileDescriptorProto, name string) *descriptorpb.DescriptorProto {
	t.Helper()
	for _, message := range file.MessageType {
		if message.GetName() == name {
			return message
		}
	}
	t.Fatalf("message %s not found", name)
	return nil
}

func assertDescriptorMessageFields(t *testing.T, file *descriptorpb.FileDescriptorProto, messageName string, names ...string) {
	t.Helper()
	message := descriptorMessage(t, file, messageName)
	if len(message.Field) != len(names) {
		t.Fatalf("%s fields = %d, want %d", messageName, len(message.Field), len(names))
	}
	for index, name := range names {
		if message.Field[index].GetName() != name {
			t.Errorf("%s field %d = %s, want %s", messageName, index, message.Field[index].GetName(), name)
		}
	}
}

func assertDescriptorOneofFields(t *testing.T, file *descriptorpb.FileDescriptorProto, messageName string, oneofName string, names ...string) {
	t.Helper()
	message := descriptorMessage(t, file, messageName)
	oneofIndex := -1
	for index, oneof := range message.OneofDecl {
		if oneof.GetName() == oneofName {
			oneofIndex = index
			break
		}
	}
	if oneofIndex < 0 {
		t.Fatalf("%s oneof %s not found", messageName, oneofName)
	}
	var got []string
	for _, field := range message.Field {
		if field.OneofIndex != nil && int(field.GetOneofIndex()) == oneofIndex {
			got = append(got, field.GetName())
		}
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Errorf("%s.%s fields = %v, want %v", messageName, oneofName, got, names)
	}
}

func assertDescriptorEnumValues(t *testing.T, file *descriptorpb.FileDescriptorProto, enumName string, names ...string) {
	t.Helper()
	for _, enum := range file.EnumType {
		if enum.GetName() != enumName {
			continue
		}
		var got []string
		for _, value := range enum.Value {
			got = append(got, value.GetName())
		}
		if strings.Join(got, ",") != strings.Join(names, ",") {
			t.Errorf("%s values = %v, want %v", enumName, got, names)
		}
		return
	}
	t.Fatalf("enum %s not found", enumName)
}

func assertDescriptorFieldType(t *testing.T, file *descriptorpb.FileDescriptorProto, messageName string, fieldName string, typeName string) {
	t.Helper()
	message := descriptorMessage(t, file, messageName)
	for _, field := range message.Field {
		if field.GetName() == fieldName {
			if field.GetTypeName() != typeName {
				t.Errorf("%s.%s type = %s, want %s", messageName, fieldName, field.GetTypeName(), typeName)
			}
			return
		}
	}
	t.Fatalf("%s.%s not found", messageName, fieldName)
}
