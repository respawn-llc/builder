package protoapi_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"core/shared/protoapi"
	fixturepb "core/shared/protoapi/gen/fixture"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protoapi/gen/testregistry"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestOperationNamesUseTheLockedStateMachine(t *testing.T) {
	fixtures := map[string]string{
		"APIStatus":                "api_status",
		"UUID":                     "uuid",
		"HTTP2Server":              "http2_server",
		"MaterializeWorkspaceChat": "materialize_workspace_chat",
		"CreateTarget":             "create_target",
	}
	for input, want := range fixtures {
		got, err := protoapi.PascalCaseToLowerSnake(input)
		if err != nil {
			t.Fatalf("PascalCaseToLowerSnake(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("PascalCaseToLowerSnake(%q) = %q, want %q", input, got, want)
		}
	}

	got, err := protoapi.ActiveOperationName(
		"workflow.task",
		"HTTP2Service",
		"MaterializeWorkspaceChat",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "workflow.task.http2_service.materialize_workspace_chat" {
		t.Fatalf("active operation name = %q", got)
	}
}

func TestOperationNamesRejectInvalidPackagesAndIdentifiers(t *testing.T) {
	for _, packageName := range []string{
		"",
		"Workflow",
		"workflow.Task",
		"workflow.2task",
		"workflow.-task",
		"workflow..task",
		"workflow.",
		"workfløw",
	} {
		if err := protoapi.ValidatePackageName(packageName); err == nil {
			t.Errorf("ValidatePackageName(%q) unexpectedly succeeded", packageName)
		}
	}

	for _, identifier := range []string{"", "workflow", "Work_flow", "Work-Flow", "Wørkflow"} {
		if _, err := protoapi.PascalCaseToLowerSnake(identifier); err == nil {
			t.Errorf("PascalCaseToLowerSnake(%q) unexpectedly succeeded", identifier)
		}
	}
}

func TestOperationIndexReadsTypedMethodOptions(t *testing.T) {
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) == 0 {
		t.Fatal("production operation index is empty")
	}
	for _, operation := range operations {
		if operation.Descriptor.ParentFile().Package().Parent() != "kent.api" &&
			operation.Descriptor.ParentFile().Package() != "kent.api" {
			t.Fatalf("production operation index contains %s", operation.Descriptor.FullName())
		}
	}
}

func TestOperationLookupUsesOnlyActiveNames(t *testing.T) {
	operation, exists, err := protoapi.OperationByName("kent.api.server.server_service.get_readiness")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || operation.ActiveName != "kent.api.server.server_service.get_readiness" {
		t.Fatalf("active lookup = %+v, %v", operation, exists)
	}

	if _, exists, err := protoapi.OperationByName("server.GetReadiness"); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("legacy wire name unexpectedly resolved through active lookup")
	}
	if _, exists, err := protoapi.OperationByName("fixture.naming_service.http2_server"); err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("test fixture operation unexpectedly resolved through production lookup")
	}
}

func TestOperationPolicyRejectsInvalidOptions(t *testing.T) {
	base := sharedpb.KentMethodOptions{
		Kind:                sharedpb.OperationKind_OPERATION_KIND_UNARY,
		AuthenticationStage: sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER,
		ScopePolicy:         sharedpb.ScopePolicy_SCOPE_POLICY_NONE,
		Direction:           sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER,
		UnaryConnection:     sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED,
	}
	tests := []struct {
		name   string
		mutate func(*sharedpb.KentMethodOptions)
		want   string
	}{
		{
			name: "unspecified kind",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Kind = sharedpb.OperationKind_OPERATION_KIND_UNSPECIFIED
			},
			want: "operation kind",
		},
		{
			name: "unknown kind",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Kind = sharedpb.OperationKind(99)
			},
			want: "operation kind",
		},
		{
			name: "unspecified auth",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.AuthenticationStage = sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_UNSPECIFIED
			},
			want: "authentication stage",
		},
		{
			name: "unknown auth",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.AuthenticationStage = sharedpb.AuthenticationStage(99)
			},
			want: "authentication stage",
		},
		{
			name: "unspecified scope",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.ScopePolicy = sharedpb.ScopePolicy_SCOPE_POLICY_UNSPECIFIED
			},
			want: "scope policy",
		},
		{
			name: "unknown scope",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.ScopePolicy = sharedpb.ScopePolicy(99)
			},
			want: "scope policy",
		},
		{
			name: "unspecified direction",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Direction = sharedpb.Direction_DIRECTION_UNSPECIFIED
			},
			want: "direction",
		},
		{
			name: "unknown direction",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Direction = sharedpb.Direction(99)
			},
			want: "direction",
		},
		{
			name: "unary connection unspecified",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.UnaryConnection = sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED
			},
			want: "unary connection",
		},
		{
			name: "unknown unary connection",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.UnaryConnection = sharedpb.UnaryConnection(99)
			},
			want: "unary connection",
		},
		{
			name: "non-unary connection specified",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Kind = sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION
			},
			want: "non-unary",
		},
		{
			name: "unary event",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Event = &sharedpb.OperationAssociation{
					Package: "fixture",
					Service: "NamingService",
					Method:  "WatchEvent",
				}
			},
			want: "unary operation",
		},
		{
			name: "subscription missing associations",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Kind = sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION
				options.UnaryConnection = sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED
			},
			want: "subscription",
		},
		{
			name: "progress missing event",
			mutate: func(options *sharedpb.KentMethodOptions) {
				options.Kind = sharedpb.OperationKind_OPERATION_KIND_PROGRESS
				options.UnaryConnection = sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED
			},
			want: "progress",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.mutate(&options)
			err := protoapi.ValidateKentMethodOptions(&options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want diagnostic containing %q", err, test.want)
			}
		})
	}
}

func TestOperationPolicyRejectsMissingOptions(t *testing.T) {
	method := fixturepb.File_fixture_method_policy_fixture_proto.
		Services().
		ByName(protoreflect.Name("NamingService")).
		Methods().
		ByName(protoreflect.Name("HTTP2Server"))
	if method == nil {
		t.Fatal("fixture method not found")
	}
	if _, err := protoapi.OperationFromDescriptor(method, nil); err == nil {
		t.Fatal("missing method options unexpectedly accepted")
	}
}

func TestIndexEnumeratesGeneratedAggregate(t *testing.T) {
	count := 0
	for descriptor := range protoapi.Files() {
		count++
		indexed, exists := protoapi.File(descriptor.Path())
		if !exists {
			t.Fatalf("enumerated descriptor %q is absent from the index", descriptor.Path())
		}
		if indexed != descriptor {
			t.Fatalf("index returned a different descriptor for %q", descriptor.Path())
		}
	}
	if count == 0 {
		t.Fatal("generated aggregate is empty")
	}
}

func TestDescriptorPathsReportTheCompleteSortedSchemaSet(t *testing.T) {
	got := protoapi.DescriptorPaths()
	if !slices.IsSorted(got) {
		t.Fatalf("descriptor paths are not sorted: %v", got)
	}

	want := repositorySchemaPaths(t, filepath.Join("kent", "api"))
	if !slices.Equal(got, want) {
		t.Fatalf("descriptor paths = %v, want complete schema set %v", got, want)
	}
}

func TestGeneratedFixtureRegistryReportsOnlyTheFixtureSchemaSet(t *testing.T) {
	got := make([]string, 0)
	for path := range testregistry.Paths {
		got = append(got, path)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("fixture descriptor paths are not sorted: %v", got)
	}

	want := repositorySchemaPaths(t, "fixture")
	if !slices.Equal(got, want) {
		t.Fatalf("fixture descriptor paths = %v, want fixture schema set %v", got, want)
	}
}

func repositorySchemaPaths(t *testing.T, relativeRoot string) []string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	protoRoot := filepath.Join(filepath.Dir(filename), "..", "..", "api", "proto")
	schemaRoot := filepath.Join(protoRoot, relativeRoot)
	paths := make([]string, 0)
	if err := filepath.WalkDir(schemaRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".proto" {
			return nil
		}
		relativePath, err := filepath.Rel(protoRoot, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relativePath))
		return nil
	}); err != nil {
		t.Fatalf("enumerate schema source tree: %v", err)
	}
	slices.Sort(paths)
	return paths
}

func TestGeneratedDescriptorsHaveUniqueNamesAndResolvedReferences(t *testing.T) {
	files := new(protoregistry.Files)
	sourcePaths := make(map[string]struct{})
	for file := range protoapi.Files() {
		if err := files.RegisterFile(file); err != nil {
			t.Fatalf("register generated descriptor %q: %v", file.Path(), err)
		}
		sourcePaths[file.Path()] = struct{}{}
	}

	for file := range protoapi.Files() {
		checkDescriptorReferences(t, files, sourcePaths, file.Messages())
		checkEnumReferences(t, files, sourcePaths, file.Enums())
		checkExtensionReferences(t, files, sourcePaths, file.Extensions())
		services := file.Services()
		for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
			methods := services.Get(serviceIndex).Methods()
			for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
				method := methods.Get(methodIndex)
				requireResolvedDescriptor(t, files, sourcePaths, method.Input())
				requireResolvedDescriptor(t, files, sourcePaths, method.Output())
			}
		}
	}
}

func checkDescriptorReferences(
	t *testing.T,
	files *protoregistry.Files,
	sourcePaths map[string]struct{},
	messages protoreflect.MessageDescriptors,
) {
	t.Helper()
	for messageIndex := 0; messageIndex < messages.Len(); messageIndex++ {
		message := messages.Get(messageIndex)
		fields := message.Fields()
		for fieldIndex := 0; fieldIndex < fields.Len(); fieldIndex++ {
			field := fields.Get(fieldIndex)
			if field.Message() != nil {
				requireResolvedDescriptor(t, files, sourcePaths, field.Message())
			}
			if field.Enum() != nil {
				requireResolvedDescriptor(t, files, sourcePaths, field.Enum())
			}
		}
		checkDescriptorReferences(t, files, sourcePaths, message.Messages())
		checkEnumReferences(t, files, sourcePaths, message.Enums())
		checkExtensionReferences(t, files, sourcePaths, message.Extensions())
	}
}

func checkEnumReferences(
	t *testing.T,
	files *protoregistry.Files,
	sourcePaths map[string]struct{},
	enums protoreflect.EnumDescriptors,
) {
	t.Helper()
	for enumIndex := 0; enumIndex < enums.Len(); enumIndex++ {
		requireResolvedDescriptor(t, files, sourcePaths, enums.Get(enumIndex))
	}
}

func checkExtensionReferences(
	t *testing.T,
	files *protoregistry.Files,
	sourcePaths map[string]struct{},
	extensions protoreflect.ExtensionDescriptors,
) {
	t.Helper()
	for extensionIndex := 0; extensionIndex < extensions.Len(); extensionIndex++ {
		extension := extensions.Get(extensionIndex)
		requireResolvedDescriptor(t, files, sourcePaths, extension)
		if extension.ContainingMessage() != nil {
			requireResolvedDescriptor(t, files, sourcePaths, extension.ContainingMessage())
		}
		if extension.Message() != nil {
			requireResolvedDescriptor(t, files, sourcePaths, extension.Message())
		}
		if extension.Enum() != nil {
			requireResolvedDescriptor(t, files, sourcePaths, extension.Enum())
		}
	}
}

func requireResolvedDescriptor(
	t *testing.T,
	files *protoregistry.Files,
	sourcePaths map[string]struct{},
	descriptor protoreflect.Descriptor,
) {
	t.Helper()
	if descriptor == nil {
		t.Fatal("generated descriptor reference is nil")
	}
	if _, sourceOwned := sourcePaths[descriptor.ParentFile().Path()]; !sourceOwned {
		if _, err := protoregistry.GlobalFiles.FindDescriptorByName(descriptor.FullName()); err != nil {
			t.Fatalf(
				"%s references unresolved imported descriptor %s from %q: %v",
				descriptor.ParentFile().Path(),
				descriptor.FullName(),
				descriptor.ParentFile().Path(),
				err,
			)
		}
		return
	}
	resolved, err := files.FindDescriptorByName(descriptor.FullName())
	if err != nil {
		t.Fatalf("generated descriptor %s is unresolved: %v", descriptor.FullName(), err)
	}
	if resolved != descriptor {
		t.Fatalf("generated descriptor %s resolves to a different declaration", descriptor.FullName())
	}
}
