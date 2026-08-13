package migrationcheck

import (
	"errors"
	"math"
	"math/big"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestDescriptorPolicyAcceptsApprovedSmallFixture(t *testing.T) {
	if err := checkDescriptorPolicy(validDescriptorPolicyFixture()); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorPolicyAcceptsAllRepositorySchemas(t *testing.T) {
	fixture, err := parseDescriptorPolicyFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDescriptorPolicy(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorPolicyRejectsInMemoryDescriptorViolationsIndependently(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*descriptorpb.FileDescriptorProto)
		want   descriptorPolicyIssueCode
	}{
		{
			name: "Any",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				addPolicyFixtureMessageField(file, "any", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".google.protobuf.Any")
				file.Dependency = append(file.Dependency, "google/protobuf/any.proto")
			},
			want: issueForbiddenOperationAny,
		},
		{
			name: "raw JSON",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				addPolicyFixtureMessageField(file, "raw_json", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".google.protobuf.Struct")
				file.Dependency = append(file.Dependency, "google/protobuf/struct.proto")
			},
			want: issueForbiddenOperationRawJSON,
		},
		{
			name: "unclassified operation bytes",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				addPolicyFixtureMessageField(file, "payload", descriptorpb.FieldDescriptorProto_TYPE_BYTES, "")
			},
			want: issueForbiddenOperationBytes,
		},
		{
			name: "generic map",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				file.MessageType[0].NestedType = append(file.MessageType[0].NestedType, &descriptorpb.DescriptorProto{
					Name: proto.String("AttributesEntry"),
					Options: &descriptorpb.MessageOptions{
						MapEntry: proto.Bool(true),
					},
					Field: []*descriptorpb.FieldDescriptorProto{
						{
							Name:   proto.String("key"),
							Number: proto.Int32(1),
							Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
							Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						},
						{
							Name:   proto.String("value"),
							Number: proto.Int32(2),
							Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
							Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						},
					},
				})
				field := addPolicyFixtureMessageField(file, "attributes", descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".kent.api.policy.Request.AttributesEntry")
				field.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			},
			want: issueForbiddenOperationMap,
		},
		{
			name: "generic application request ID",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				addPolicyFixtureMessageField(file, "request_id", descriptorpb.FieldDescriptorProto_TYPE_STRING, "")
			},
			want: issueGenericApplicationRequestID,
		},
		{
			name: "package v1",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				file.Package = proto.String("kent.api.v1.policy")
				file.Service[0].Method[0].InputType = proto.String(".kent.api.v1.policy.Request")
				file.Service[0].Method[0].OutputType = proto.String(".kent.api.v1.policy.Response")
				file.MessageType[0].Field[0].TypeName = proto.String(".kent.api.v1.policy.State")
			},
			want: issuePackageVersionSegment,
		},
		{
			name: "package v2",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				file.Package = proto.String("kent.api.v2.policy")
				file.Service[0].Method[0].InputType = proto.String(".kent.api.v2.policy.Request")
				file.Service[0].Method[0].OutputType = proto.String(".kent.api.v2.policy.Response")
				file.MessageType[0].Field[0].TypeName = proto.String(".kent.api.v2.policy.State")
			},
			want: issuePackageVersionSegment,
		},
		{
			name: "duplicate active operation name",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				file.Service[0].Method = append(file.Service[0].Method, policyFixtureMethod("HttpStatus", "legacy.http_status_alias"))
			},
			want: issueDuplicateActiveOperationName,
		},
		{
			name: "duplicate legacy operation option",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				file.Service[0].Method = append(file.Service[0].Method, policyFixtureMethod("Create", "legacy.http_status"))
			},
			want: issueDuplicateLegacyWireName,
		},
		{
			name: "missing defined-only enum constraint",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				file.MessageType[0].Field[0].Options = nil
			},
			want: issueEnumAllowsUnknownValues,
		},
		{
			name: "unsafe 64-bit field",
			mutate: func(file *descriptorpb.FileDescriptorProto) {
				addPolicyFixtureMessageField(file, "sequence", descriptorpb.FieldDescriptorProto_TYPE_UINT64, "")
			},
			want: issueUnsafeJavaScriptIntegerBounds,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := validInMemoryPolicyDescriptor()
			test.mutate(file)
			fixture, err := parseDescriptorPolicyFiles(policyFixtureFiles(t, file))
			if err != nil {
				t.Fatalf("parse descriptor fixture: %v", err)
			}
			assertOnlyDescriptorPolicyIssue(t, checkDescriptorPolicy(fixture), test.want)
		})
	}
}

func TestDescriptorPolicyAllowsInMemoryDescriptorTypedEnvelopeBytes(t *testing.T) {
	file := protodesc.ToFileDescriptorProto(sharedpb.File_kent_api_shared_foundation_proto)
	fixture, err := parseDescriptorPolicyFiles(policyFixtureFiles(t, file))
	if err != nil {
		t.Fatalf("parse descriptor fixture: %v", err)
	}
	if err := checkDescriptorPolicy(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorPolicyRejectsInMemoryUntypedEnvelopeBytes(t *testing.T) {
	file := protodesc.ToFileDescriptorProto(sharedpb.File_kent_api_shared_foundation_proto)
	transportFailure := descriptorMessageByName(t, file, "TransportFailure")
	transportFailure.Field = append(transportFailure.Field, &descriptorpb.FieldDescriptorProto{
		Name:   proto.String("debug_payload"),
		Number: proto.Int32(3),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:   descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum(),
	})
	fixture, err := parseDescriptorPolicyFiles(policyFixtureFiles(t, file))
	if err != nil {
		t.Fatalf("parse descriptor fixture: %v", err)
	}
	assertOnlyDescriptorPolicyIssue(t, checkDescriptorPolicy(fixture), issueForbiddenEnvelopeBytes)
}

func TestDescriptorPolicyRejectsPackageVersionSegmentIndependently(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Name = "workflow.v1.task"

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issuePackageVersionSegment,
	)
}

func TestDescriptorPolicyRejectsUnknownEnumAcceptanceIndependently(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages[0].Fields[0].EnumDefinedOnly = false

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issueEnumAllowsUnknownValues,
	)
}

func TestDescriptorPolicyRejectsUnsafeSignedJavaScriptIntegerBoundIndependently(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages[0].Fields[1].IntegerBounds.Maximum = bigInteger(
		"9007199254740992",
	)

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issueUnsafeJavaScriptIntegerBounds,
	)
}

func TestDescriptorPolicyRejectsUnsafeUnsignedJavaScriptIntegerBoundIndependently(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages[0].Fields[2].IntegerBounds.Maximum = bigInteger(
		"9007199254740992",
	)

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issueUnsafeJavaScriptIntegerBounds,
	)
}

func TestDescriptorPolicyRejectsUnsignedBoundBeyondMaxInt64Independently(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages[0].Fields[2].IntegerBounds.Maximum = new(big.Int).SetUint64(
		math.MaxUint64,
	)

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issueUnsafeJavaScriptIntegerBounds,
	)
}

func TestDescriptorPolicyRejectsForbiddenOperationMessageDynamicFieldsIndependently(t *testing.T) {
	tests := []struct {
		name  string
		field descriptorPolicyField
		want  descriptorPolicyIssueCode
	}{
		{
			name:  "Any",
			field: descriptorPolicyField{Name: "detail", Kind: descriptorPolicyAny},
			want:  issueForbiddenOperationAny,
		},
		{
			name:  "raw JSON",
			field: descriptorPolicyField{Name: "detail", Kind: descriptorPolicyRawJSON},
			want:  issueForbiddenOperationRawJSON,
		},
		{
			name:  "opaque bytes",
			field: descriptorPolicyField{Name: "payload", Kind: descriptorPolicyBytes},
			want:  issueForbiddenOperationBytes,
		},
		{
			name: "generic map",
			field: descriptorPolicyField{
				Name:     "attributes",
				Kind:     descriptorPolicyMap,
				MapValue: descriptorPolicyString,
			},
			want: issueForbiddenOperationMap,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validDescriptorPolicyFixture()
			fixture.Packages[0].Messages[0].Fields = append(
				fixture.Packages[0].Messages[0].Fields,
				test.field,
			)

			assertOnlyDescriptorPolicyIssue(
				t,
				checkDescriptorPolicy(fixture),
				test.want,
			)
		})
	}
}

func TestDescriptorPolicyDoesNotTreatOperationBytesAsDescriptorTypedEnvelopePayload(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages[0].Fields = append(
		fixture.Packages[0].Messages[0].Fields,
		descriptorPolicyField{
			Name:                   "payload",
			Kind:                   descriptorPolicyBytes,
			DescriptorTypedPayload: true,
		},
	)

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issueForbiddenOperationBytes,
	)
}

func TestDescriptorPolicyRejectsGenericApplicationRequestIDsIndependently(t *testing.T) {
	for _, fieldName := range []string{"request_id", "client_request_id"} {
		t.Run(fieldName, func(t *testing.T) {
			fixture := validDescriptorPolicyFixture()
			fixture.Packages[0].Messages[0].Fields = append(
				fixture.Packages[0].Messages[0].Fields,
				descriptorPolicyField{Name: fieldName, Kind: descriptorPolicyString},
			)

			assertOnlyDescriptorPolicyIssue(
				t,
				checkDescriptorPolicy(fixture),
				issueGenericApplicationRequestID,
			)
		})
	}
}

func TestDescriptorPolicyAllowsRetainedDomainAndEnvelopeCorrelationIDs(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages[0].Fields = append(
		fixture.Packages[0].Messages[0].Fields,
		descriptorPolicyField{Name: "queue_item_id", Kind: descriptorPolicyString},
		descriptorPolicyField{Name: "setup_operation_id", Kind: descriptorPolicyString},
		descriptorPolicyField{Name: "correlation", Kind: descriptorPolicyString},
	)

	if err := checkDescriptorPolicy(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorPolicyAllowsOnlyDescriptorTypedEnvelopeBytes(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages = append(
		fixture.Packages[0].Messages,
		descriptorPolicyMessage{
			Path: "transport.Envelope",
			Role: descriptorPolicyEnvelope,
			Fields: []descriptorPolicyField{{
				Name:                   "payload",
				Kind:                   descriptorPolicyBytes,
				DescriptorTypedPayload: true,
			}},
		},
	)

	if err := checkDescriptorPolicy(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorPolicyRejectsUntypedEnvelopeBytesIndependently(t *testing.T) {
	fixture := validDescriptorPolicyFixture()
	fixture.Packages[0].Messages = append(
		fixture.Packages[0].Messages,
		descriptorPolicyMessage{
			Path:   "transport.Envelope",
			Role:   descriptorPolicyEnvelope,
			Fields: []descriptorPolicyField{{Name: "payload", Kind: descriptorPolicyBytes}},
		},
	)

	assertOnlyDescriptorPolicyIssue(
		t,
		checkDescriptorPolicy(fixture),
		issueForbiddenEnvelopeBytes,
	)
}

func validDescriptorPolicyFixture() descriptorPolicySet {
	return descriptorPolicySet{
		Packages: []descriptorPolicyPackage{{
			Name: "workflow.task",
			Messages: []descriptorPolicyMessage{{
				Path: "workflow.task.SearchRequest",
				Role: descriptorPolicyOperation,
				Fields: []descriptorPolicyField{
					{
						Name:            "sort",
						Kind:            descriptorPolicyEnum,
						EnumDefinedOnly: true,
					},
					{
						Name: "version",
						Kind: descriptorPolicyInt64,
						IntegerBounds: descriptorPolicyIntegerBounds{
							Minimum: bigInteger("-9007199254740991"),
							Maximum: bigInteger("9007199254740991"),
						},
					},
					{
						Name: "sequence",
						Kind: descriptorPolicyUint64,
						IntegerBounds: descriptorPolicyIntegerBounds{
							Minimum: bigInteger("0"),
							Maximum: bigInteger("9007199254740991"),
						},
					},
				},
			}},
		}},
	}
}

func bigInteger(value string) *big.Int {
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid test integer " + value)
	}
	return parsed
}

func assertOnlyDescriptorPolicyIssue(
	t *testing.T,
	err error,
	want descriptorPolicyIssueCode,
) {
	t.Helper()
	var policyError *descriptorPolicyError
	if !errors.As(err, &policyError) {
		t.Fatalf("error = %v, want descriptorPolicyError", err)
	}
	if len(policyError.Issues) != 1 {
		t.Fatalf("descriptor policy issues = %+v, want only %s", policyError.Issues, want)
	}
	if policyError.Issues[0].Code != want {
		t.Fatalf("descriptor policy issue = %+v, want %s", policyError.Issues[0], want)
	}
}

func validInMemoryPolicyDescriptor() *descriptorpb.FileDescriptorProto {
	enumRules := &validate.FieldRules{
		Type: &validate.FieldRules_Enum{
			Enum: &validate.EnumRules{DefinedOnly: proto.Bool(true)},
		},
	}
	enumOptions := &descriptorpb.FieldOptions{}
	proto.SetExtension(enumOptions, validate.E_Field, enumRules)
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("kent/api/policy/fixture.proto"),
		Package:    proto.String("kent.api.policy"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"buf/validate/validate.proto", "kent/api/shared/foundation.proto"},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("State"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("STATE_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("STATE_READY"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Request"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:     proto.String("state"),
					Number:   proto.Int32(1),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
					TypeName: proto.String(".kent.api.policy.State"),
					Options:  enumOptions,
				}},
			},
			{Name: proto.String("Response")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("PolicyService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				policyFixtureMethod("HTTPStatus", "legacy.http_status"),
			},
		}},
	}
}

func policyFixtureMethod(name string, legacyWireName string) *descriptorpb.MethodDescriptorProto {
	options := &descriptorpb.MethodOptions{}
	proto.SetExtension(options, sharedpb.E_KentMethod, &sharedpb.KentMethodOptions{
		Kind:                sharedpb.OperationKind_OPERATION_KIND_UNARY,
		AuthenticationStage: sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER,
		ScopePolicy:         sharedpb.ScopePolicy_SCOPE_POLICY_NONE,
		Direction:           sharedpb.Direction_DIRECTION_CLIENT_TO_SERVER,
		UnaryConnection:     sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED,
		LegacyWireName:      proto.String(legacyWireName),
	})
	return &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(name),
		InputType:  proto.String(".kent.api.policy.Request"),
		OutputType: proto.String(".kent.api.policy.Response"),
		Options:    options,
	}
}

func addPolicyFixtureMessageField(
	file *descriptorpb.FileDescriptorProto,
	name string,
	fieldType descriptorpb.FieldDescriptorProto_Type,
	typeName string,
) *descriptorpb.FieldDescriptorProto {
	field := &descriptorpb.FieldDescriptorProto{
		Name:   proto.String(name),
		Number: proto.Int32(int32(len(file.MessageType[0].Field) + 1)),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:   fieldType.Enum(),
	}
	if typeName != "" {
		field.TypeName = proto.String(typeName)
	}
	file.MessageType[0].Field = append(file.MessageType[0].Field, field)
	return field
}

func descriptorMessageByName(
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
	t.Fatalf("descriptor message %s not found", name)
	return nil
}

func policyFixtureFiles(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
) []protoreflect.FileDescriptor {
	t.Helper()
	descriptor, err := protodesc.NewFile(file, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build descriptor fixture: %v", err)
	}
	return []protoreflect.FileDescriptor{descriptor}
}
