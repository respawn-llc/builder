package migrationcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"core/shared/apicontract"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestWorkflowTaskAttentionSchemaOwnsExactRoutesAndTypedUnions(t *testing.T) {
	set := buildWorkflowTaskAttentionDescriptorSet(t)
	file := workflowTaskAttentionDescriptorFile(t, set)

	assertWorkflowTaskAttentionRoutes(t, set, map[string]workflowTaskAttentionRoute{
		"workflow.attention.list": {
			service: "AttentionReadService",
			method:  "List",
			input:   ".kent.api.workflow_task.AttentionListRequest",
			output:  ".kent.api.workflow_task.AttentionListResult",
		},
		"workflow.task.attention.list": {
			service: "AttentionReadService",
			method:  "ListTask",
			input:   ".kent.api.workflow_task.TaskAttentionListRequest",
			output:  ".kent.api.workflow_task.TaskAttentionListResult",
		},
		"attention.notification.subscribe": {
			service: "AttentionNotificationService",
			method:  "Subscribe",
			input:   ".google.protobuf.Empty",
			output:  ".kent.api.workflow_task.AttentionNotificationStartResult",
		},
		"attention.notification": {
			service: "AttentionNotificationService",
			method:  "Event",
			input:   ".kent.api.workflow_task.AttentionNotificationEvent",
			output:  ".google.protobuf.Empty",
		},
		"attention.notification.complete": {
			service: "AttentionNotificationService",
			method:  "Complete",
			input:   ".kent.api.shared.StreamCompletion",
			output:  ".google.protobuf.Empty",
		},
	})
	assertWorkflowTaskAttentionLiveRouteProvenance(t, set, []string{
		protocol.MethodWorkflowAttentionList,
		protocol.MethodWorkflowTaskAttentionList,
		protocol.MethodAttentionNotificationSubscribe,
		protocol.MethodAttentionNotificationEvent,
		protocol.MethodAttentionNotificationComplete,
	})

	assertWorkflowTaskAttentionMessageFields(t, file, "AttentionItem",
		"id", "kind", "project_id", "workflow_id", "task_id", "task_short_id", "task_title",
		"occurred_at", "question", "approval", "interrupted_current_node")
	assertWorkflowTaskAttentionOneof(t, file, "AttentionItem", "detail",
		"question", "approval", "interrupted_current_node")
	assertWorkflowTaskAttentionOneof(t, file, "AttentionNotification", "state",
		"question", "approval", "workflow_approval", "interrupted_current_node")
	assertWorkflowTaskAttentionOneof(t, file, "AttentionNotificationTarget", "target",
		"workflow_task", "session_prompt")
	assertWorkflowTaskAttentionOneof(t, file, "AttentionNotificationTaskFocus", "focus",
		"question", "approval", "interrupted_current_node")
	assertWorkflowTaskAttentionOneof(t, file, "InterruptedCurrentNodeDetails", "detail",
		"generic", "configured_execution_target_unavailable", "setup_recovery",
		"configured_execution_target_unavailable_with_setup_recovery")
	assertWorkflowTaskAttentionOneof(t, file, "AttentionNotificationEvent", "payload",
		"pending", "resolved", "snapshot_complete")

	assertWorkflowTaskAttentionFieldType(t, file, "AttentionNotification", "question", ".kent.api.attention.QuestionState")
	assertWorkflowTaskAttentionFieldType(t, file, "AttentionNotification", "approval", ".kent.api.attention.ApprovalState")
	assertWorkflowTaskAttentionFieldType(t, file, "AttentionNotification", "workflow_approval", ".kent.api.workflow_task.WorkflowApprovalState")
	assertWorkflowTaskAttentionFieldType(t, file, "AttentionNotificationTarget", "workflow_task", ".kent.api.workflow_task.WorkflowTaskAttentionTarget")
	assertWorkflowTaskAttentionFieldType(t, file, "InterruptedCurrentNodeState", "details", ".kent.api.workflow_task.InterruptedCurrentNodeDetails")
	assertWorkflowTaskAttentionFieldType(t, file, "InterruptedAttention", "details", ".kent.api.workflow_task.InterruptedCurrentNodeDetails")
	assertWorkflowTaskAttentionFieldType(t, file, "ConfiguredExecutionTargetUnavailableDetails", "mode", ".kent.api.workflow_definition.ExecutionTargetMode")
	assertWorkflowTaskAttentionFieldType(t, file, "SetupExecutionTarget", "mode", ".kent.api.workflow_definition.ExecutionTargetMode")

	for _, result := range []string{
		"AttentionListResult",
		"TaskAttentionListResult",
		"AttentionNotificationStartResult",
	} {
		assertWorkflowTaskAttentionOneof(t, file, result, "outcome", "success", "error")
	}
	assertWorkflowTaskAttentionOneof(t, file, "AttentionListError", "detail",
		"invalid_request", "internal_failure")
	assertWorkflowTaskAttentionOneof(t, file, "TaskAttentionListError", "detail",
		"invalid_request", "internal_failure")
	assertWorkflowTaskAttentionOneof(t, file, "AttentionNotificationStartError", "detail",
		"internal_failure")

	assertWorkflowTaskAttentionEnum(t, file, "AttentionNotificationKind",
		"ATTENTION_NOTIFICATION_KIND_UNSPECIFIED",
		"ATTENTION_NOTIFICATION_KIND_QUESTION",
		"ATTENTION_NOTIFICATION_KIND_APPROVAL",
		"ATTENTION_NOTIFICATION_KIND_WORKFLOW_APPROVAL",
		"ATTENTION_NOTIFICATION_KIND_INTERRUPTED_CURRENT_NODE")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionNotificationSource",
		"ATTENTION_NOTIFICATION_SOURCE_UNSPECIFIED",
		"ATTENTION_NOTIFICATION_SOURCE_LIVE",
		"ATTENTION_NOTIFICATION_SOURCE_SNAPSHOT")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionNotificationEventType",
		"ATTENTION_NOTIFICATION_EVENT_UNSPECIFIED",
		"ATTENTION_NOTIFICATION_EVENT_PENDING",
		"ATTENTION_NOTIFICATION_EVENT_RESOLVED",
		"ATTENTION_NOTIFICATION_EVENT_SNAPSHOT_COMPLETE")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionNotificationTargetKind",
		"ATTENTION_NOTIFICATION_TARGET_UNSPECIFIED",
		"ATTENTION_NOTIFICATION_TARGET_WORKFLOW_TASK",
		"ATTENTION_NOTIFICATION_TARGET_SESSION_PROMPT")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionNotificationFocusKind",
		"ATTENTION_NOTIFICATION_FOCUS_UNSPECIFIED",
		"ATTENTION_NOTIFICATION_FOCUS_QUESTION",
		"ATTENTION_NOTIFICATION_FOCUS_APPROVAL",
		"ATTENTION_NOTIFICATION_FOCUS_INTERRUPTED_CURRENT_NODE")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionQuestionKind",
		"ATTENTION_QUESTION_KIND_UNSPECIFIED",
		"ATTENTION_QUESTION_KIND_ORDINARY",
		"ATTENTION_QUESTION_KIND_APPROVAL")
	assertWorkflowTaskAttentionEnum(t, file, "ExecutionTargetUnavailableCause",
		"EXECUTION_TARGET_UNAVAILABLE_CAUSE_UNSPECIFIED",
		"EXECUTION_TARGET_UNAVAILABLE_CAUSE_INVALID_REVISION",
		"EXECUTION_TARGET_UNAVAILABLE_CAUSE_NON_COMMIT",
		"EXECUTION_TARGET_UNAVAILABLE_CAUSE_DEFAULT_BRANCH_MISSING",
		"EXECUTION_TARGET_UNAVAILABLE_CAUSE_DEFAULT_BRANCH_AMBIGUOUS",
		"EXECUTION_TARGET_UNAVAILABLE_CAUSE_GIT_FAILURE")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionSetupRecoveryCause",
		"ATTENTION_SETUP_RECOVERY_CAUSE_UNSPECIFIED",
		"ATTENTION_SETUP_RECOVERY_CAUSE_PROCESS_EXIT",
		"ATTENTION_SETUP_RECOVERY_CAUSE_TIMEOUT",
		"ATTENTION_SETUP_RECOVERY_CAUSE_TARGET_PREPARATION",
		"ATTENTION_SETUP_RECOVERY_CAUSE_OPERATIONAL")
	assertWorkflowTaskAttentionEnum(t, file, "AttentionSetupRequirement",
		"ATTENTION_SETUP_REQUIREMENT_UNSPECIFIED",
		"ATTENTION_SETUP_REQUIREMENT_REQUIRED",
		"ATTENTION_SETUP_REQUIREMENT_ALREADY_COMPLETED")

	imports := make(map[string]bool, len(file.Dependency))
	for _, dependency := range file.Dependency {
		imports[dependency] = true
	}
	if !imports["kent/api/attention/attention.proto"] {
		t.Error("attention.proto is not imported for the semantically exact Session-owned question and approval states")
	}
	if !imports["kent/api/workflow_definition/workflow_definition.proto"] {
		t.Error("attention.proto is not importing the authoritative Workflow execution-target mode enum")
	}
	for _, forbidden := range []string{
		"google/protobuf/any.proto",
		"google/protobuf/struct.proto",
	} {
		if imports[forbidden] {
			t.Errorf("attention.proto imports forbidden dynamic schema %s", forbidden)
		}
	}
	for _, message := range file.MessageType {
		for _, field := range message.Field {
			if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_BYTES ||
				field.GetTypeName() == ".google.protobuf.Struct" {
				t.Errorf("%s.%s uses forbidden dynamic payload type", message.GetName(), field.GetName())
			}
			if field.GetName() == "detail_json" {
				t.Errorf("%s retains untyped detail_json", message.GetName())
			}
		}
	}
}

func assertWorkflowTaskAttentionLiveRouteProvenance(
	t *testing.T,
	set *descriptorpb.FileDescriptorSet,
	legacyNames []string,
) {
	t.Helper()
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}
	file, err := files.FindFileByPath("kent/api/workflow_task/attention.proto")
	if err != nil {
		t.Fatal(err)
	}
	descriptorCounts := make(map[string]int, len(legacyNames))
	for serviceIndex := 0; serviceIndex < file.Services().Len(); serviceIndex++ {
		service := file.Services().Get(serviceIndex)
		for methodIndex := 0; methodIndex < service.Methods().Len(); methodIndex++ {
			method := service.Methods().Get(methodIndex)
			options := method.Options().(*descriptorpb.MethodOptions)
			extension := proto.GetExtension(options, sharedpb.E_KentMethod)
			kentOptions, _ := extension.(*sharedpb.KentMethodOptions)
			if kentOptions != nil && kentOptions.LegacyWireName != nil {
				descriptorCounts[kentOptions.GetLegacyWireName()]++
			}
		}
	}
	for _, legacyName := range legacyNames {
		if _, exists := apicontract.RouteByMethod(legacyName); !exists {
			t.Fatalf("live attention route disappeared: %s", legacyName)
		}
		if got := descriptorCounts[legacyName]; got != 1 {
			t.Errorf("%s descriptor provenance count = %d, want 1", legacyName, got)
		}
	}
	if len(descriptorCounts) != len(legacyNames) {
		t.Errorf("attention descriptor provenance set = %v, want exactly %v", descriptorCounts, legacyNames)
	}
}

type workflowTaskAttentionRoute struct {
	service string
	method  string
	input   string
	output  string
}

func buildWorkflowTaskAttentionDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	root := workflowTaskAttentionRepositoryRoot(t)
	isolatedRoot := t.TempDir()
	if err := os.CopyFS(filepath.Join(isolatedRoot, "api/proto"), os.DirFS(filepath.Join(root, "api/proto"))); err != nil {
		t.Fatalf("copy Protobuf module: %v", err)
	}
	if err := os.Remove(filepath.Join(isolatedRoot, "api/proto/kent/api/workflow_task/lifecycle.proto")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("exclude in-progress Workflow Task lifecycle schema: %v", err)
	}
	for _, name := range []string{"buf.yaml", "buf.lock"} {
		encoded, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(isolatedRoot, name), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(isolatedRoot, "workflow-task-attention.binpb")
	command := exec.Command(
		"go", "tool", "buf", "build", isolatedRoot,
		"--config", filepath.Join(isolatedRoot, "buf.yaml"),
		"--as-file-descriptor-set",
		"--output", output,
	)
	command.Dir = filepath.Join(root, "tools", "protobuf")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Workflow Task Attention schema: %v\n%s", err, combined)
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

func workflowTaskAttentionRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../.."))
}

func workflowTaskAttentionDescriptorFile(t *testing.T, set *descriptorpb.FileDescriptorSet) *descriptorpb.FileDescriptorProto {
	t.Helper()
	const path = "kent/api/workflow_task/attention.proto"
	for _, file := range set.File {
		if file.GetName() == path {
			return file
		}
	}
	t.Fatalf("descriptor file %s not found", path)
	return nil
}

func assertWorkflowTaskAttentionRoutes(
	t *testing.T,
	set *descriptorpb.FileDescriptorSet,
	expected map[string]workflowTaskAttentionRoute,
) {
	t.Helper()
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatalf("link attention descriptors: %v", err)
	}
	file, err := files.FindFileByPath("kent/api/workflow_task/attention.proto")
	if err != nil {
		t.Fatal(err)
	}
	methods := make(map[protoreflect.FullName]protoreflect.MethodDescriptor)
	for serviceIndex := 0; serviceIndex < file.Services().Len(); serviceIndex++ {
		service := file.Services().Get(serviceIndex)
		for methodIndex := 0; methodIndex < service.Methods().Len(); methodIndex++ {
			method := service.Methods().Get(methodIndex)
			methods[method.FullName()] = method
		}
	}
	found := make(map[string]struct{}, len(expected))
	for legacy, route := range expected {
		service := file.Services().ByName(protoreflect.Name(route.service))
		if service == nil {
			t.Fatalf("service %s not found", route.service)
		}
		method := service.Methods().ByName(protoreflect.Name(route.method))
		if method == nil {
			t.Fatalf("%s.%s not found", route.service, route.method)
		}
		if string(method.Input().FullName()) != strings.TrimPrefix(route.input, ".") ||
			string(method.Output().FullName()) != strings.TrimPrefix(route.output, ".") {
			t.Errorf("%s signature = %s -> %s, want %s -> %s",
				legacy, method.Input().FullName(), method.Output().FullName(), route.input, route.output)
		}
		options, ok := method.Options().(*descriptorpb.MethodOptions)
		if !ok {
			t.Fatalf("%s options type = %T", method.FullName(), method.Options())
		}
		extension := proto.GetExtension(options, sharedpb.E_KentMethod)
		kentOptions, ok := extension.(*sharedpb.KentMethodOptions)
		if !ok || kentOptions == nil {
			t.Fatalf("%s has no Kent method options", method.FullName())
		}
		if kentOptions.GetLegacyWireName() != legacy {
			t.Errorf("%s provenance = %q, want %q", method.FullName(), kentOptions.GetLegacyWireName(), legacy)
		}
		if _, duplicate := found[legacy]; duplicate {
			t.Errorf("duplicate attention route provenance %s", legacy)
		}
		found[legacy] = struct{}{}
		live, exists := apicontract.RouteByMethod(legacy)
		if !exists {
			t.Fatalf("live route disappeared: %s", legacy)
		}
		if kentOptions.GetKind() != workflowTaskAttentionOperationKind(live.Kind) {
			t.Errorf("%s kind = %s, want %s", legacy, kentOptions.GetKind(), live.Kind)
		}
		if kentOptions.GetAuthenticationStage() != workflowTaskAttentionAuthenticationStage(live.Auth) {
			t.Errorf("%s authentication = %s, want %s", legacy, kentOptions.GetAuthenticationStage(), live.Auth)
		}
		if kentOptions.GetScopePolicy() != workflowTaskAttentionScopePolicy(live.Scope) {
			t.Errorf("%s scope = %s, want %s", legacy, kentOptions.GetScopePolicy(), live.Scope)
		}
		wantDirection := sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER
		if live.Kind == apicontract.KindNotification {
			wantDirection = sharedpb.Direction_DIRECTION_SERVER_TO_CLIENT
		}
		if kentOptions.GetDirection() != wantDirection {
			t.Errorf("%s direction = %s, want %s", legacy, kentOptions.GetDirection(), wantDirection)
		}
		if live.Kind == apicontract.KindUnary {
			wantConnection := workflowTaskAttentionUnaryConnection(live.Connection)
			if kentOptions.GetUnaryConnection() != wantConnection {
				t.Errorf("%s connection = %s, want %s", legacy, kentOptions.GetUnaryConnection(), wantConnection)
			}
		} else if kentOptions.GetUnaryConnection() != sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED {
			t.Errorf("%s non-unary connection = %s, want unspecified", legacy, kentOptions.GetUnaryConnection())
		}
		assertWorkflowTaskAttentionAssociation(t, methods, legacy, "event", kentOptions.GetEvent(), live.EventMethod)
		assertWorkflowTaskAttentionAssociation(t, methods, legacy, "completion", kentOptions.GetCompletion(), live.CompleteMethod)
	}
	for serviceIndex := 0; serviceIndex < file.Services().Len(); serviceIndex++ {
		service := file.Services().Get(serviceIndex)
		for methodIndex := 0; methodIndex < service.Methods().Len(); methodIndex++ {
			method := service.Methods().Get(methodIndex)
			options := method.Options().(*descriptorpb.MethodOptions)
			extension := proto.GetExtension(options, sharedpb.E_KentMethod)
			kentOptions, _ := extension.(*sharedpb.KentMethodOptions)
			if kentOptions == nil || kentOptions.LegacyWireName == nil {
				t.Errorf("%s has no route provenance", method.FullName())
				continue
			}
			if _, exists := expected[kentOptions.GetLegacyWireName()]; !exists {
				t.Errorf("%s has unexpected route provenance %q", method.FullName(), kentOptions.GetLegacyWireName())
			}
		}
	}
}

func assertWorkflowTaskAttentionAssociation(
	t *testing.T,
	methods map[protoreflect.FullName]protoreflect.MethodDescriptor,
	legacyName string,
	label string,
	association *sharedpb.OperationAssociation,
	wantLegacyName string,
) {
	t.Helper()
	if wantLegacyName == "" {
		if association != nil {
			t.Errorf("%s unexpected %s association = %+v", legacyName, label, association)
		}
		return
	}
	if association == nil {
		t.Errorf("%s missing %s association for %s", legacyName, label, wantLegacyName)
		return
	}
	fullName := protoreflect.FullName(association.GetPackage() + "." + association.GetService() + "." + association.GetMethod())
	method := methods[fullName]
	if method == nil {
		t.Errorf("%s %s association target %s does not exist", legacyName, label, fullName)
		return
	}
	options := method.Options().(*descriptorpb.MethodOptions)
	extension := proto.GetExtension(options, sharedpb.E_KentMethod)
	kentOptions, _ := extension.(*sharedpb.KentMethodOptions)
	if kentOptions == nil || kentOptions.GetLegacyWireName() != wantLegacyName {
		t.Errorf("%s %s association provenance = %q, want %q",
			legacyName, label, kentOptions.GetLegacyWireName(), wantLegacyName)
	}
}

func workflowTaskAttentionOperationKind(kind apicontract.Kind) sharedpb.OperationKind {
	switch kind {
	case apicontract.KindUnary:
		return sharedpb.OperationKind_OPERATION_KIND_UNARY
	case apicontract.KindSubscription:
		return sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION
	case apicontract.KindProgress:
		return sharedpb.OperationKind_OPERATION_KIND_PROGRESS
	case apicontract.KindNotification:
		return sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION
	default:
		panic("unsupported attention operation kind: " + kind)
	}
}

func workflowTaskAttentionAuthenticationStage(auth apicontract.AuthPolicy) sharedpb.AuthenticationStage {
	switch auth {
	case apicontract.AuthNone:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_NONE
	case apicontract.AuthPreServerAuth:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_PRE_SERVER
	case apicontract.AuthServer:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER
	default:
		panic("unsupported attention authentication stage: " + auth)
	}
}

func workflowTaskAttentionScopePolicy(scope apicontract.ScopePolicy) sharedpb.ScopePolicy {
	switch scope {
	case apicontract.ScopeNone:
		return sharedpb.ScopePolicy_SCOPE_POLICY_NONE
	case apicontract.ScopeProjectView:
		return sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_VIEW
	case apicontract.ScopeNotification:
		return sharedpb.ScopePolicy_SCOPE_POLICY_NOTIFICATION
	default:
		panic("unsupported attention scope policy: " + scope)
	}
}

func workflowTaskAttentionUnaryConnection(connection apicontract.ConnectionStrategy) sharedpb.UnaryConnection {
	switch connection {
	case apicontract.ConnectionControl, apicontract.ConnectionUnscoped:
		return sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED
	case apicontract.ConnectionDedicated:
		return sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED
	default:
		panic("unsupported attention unary connection: " + connection)
	}
}

func workflowTaskAttentionMessage(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
	name string,
) *descriptorpb.DescriptorProto {
	t.Helper()
	for _, message := range file.MessageType {
		if message.GetName() == name {
			return message
		}
	}
	t.Fatalf("message %s not found", name)
	return nil
}

func assertWorkflowTaskAttentionMessageFields(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
	messageName string,
	names ...string,
) {
	t.Helper()
	message := workflowTaskAttentionMessage(t, file, messageName)
	if len(message.Field) != len(names) {
		t.Fatalf("%s fields = %d, want %d", messageName, len(message.Field), len(names))
	}
	for index, name := range names {
		if got := message.Field[index].GetName(); got != name {
			t.Errorf("%s field %d = %s, want %s", messageName, index, got, name)
		}
	}
}

func assertWorkflowTaskAttentionOneof(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
	messageName string,
	oneofName string,
	names ...string,
) {
	t.Helper()
	message := workflowTaskAttentionMessage(t, file, messageName)
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

func assertWorkflowTaskAttentionFieldType(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
	messageName string,
	fieldName string,
	typeName string,
) {
	t.Helper()
	message := workflowTaskAttentionMessage(t, file, messageName)
	for _, field := range message.Field {
		if field.GetName() != fieldName {
			continue
		}
		if field.GetTypeName() != typeName {
			t.Errorf("%s.%s type = %s, want %s", messageName, fieldName, field.GetTypeName(), typeName)
		}
		return
	}
	t.Fatalf("%s.%s not found", messageName, fieldName)
}

func assertWorkflowTaskAttentionEnum(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
	name string,
	values ...string,
) {
	t.Helper()
	for _, enum := range file.EnumType {
		if enum.GetName() != name {
			continue
		}
		var got []string
		for _, value := range enum.Value {
			got = append(got, value.GetName())
		}
		if strings.Join(got, ",") != strings.Join(values, ",") {
			t.Errorf("%s values = %v, want %v", name, got, values)
		}
		return
	}
	t.Fatalf("enum %s not found", name)
}
