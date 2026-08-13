package migrationcheck

import (
	"errors"
	"go/token"
	"go/types"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

type exceptionalFingerprintFixture struct {
	Value string
}

func TestAssociatedClosedEnumCoverageRejectsDescriptorMemberDrift(t *testing.T) {
	enum := syntheticEnumDescriptor(t, "DELIVERY_MODE_IMMEDIATE", "DELIVERY_MODE_SCHEDULED")
	scalarObject := types.NewTypeName(
		token.NoPos,
		types.NewPackage("fixture", "fixture"),
		"DeliveryMode",
		nil,
	)
	scalar := NamedScalar{
		Identity: Identity{PackagePath: "fixture", TypeName: "DeliveryMode", Kind: IdentityType},
		Type:     scalarObject,
	}
	classification := DeclarationClassification{Scalars: []ScalarClassification{{
		Identity: scalar.Identity,
		Kind:     ScalarClosedStringEnum,
		EnumMembers: []EnumMemberClassification{
			{GoConstant: "DeliveryModeImmediate", DescriptorName: "DELIVERY_MODE_IMMEDIATE"},
			{GoConstant: "DeliveryModeScheduled", DescriptorName: "DELIVERY_MODE_SCHEDULED"},
		},
	}}}

	associations := map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor{
		scalarObject: {enum.FullName(): enum},
	}
	var issues []CoverageIssue
	checkAssociatedClosedEnumCoverage(classification, []NamedScalar{scalar}, associations, &issues)
	if len(issues) != 0 {
		t.Fatalf("matching enum coverage issues = %+v", issues)
	}

	classification.Scalars[0].EnumMembers = append(
		classification.Scalars[0].EnumMembers,
		EnumMemberClassification{
			GoConstant:     "DeliveryModeDeferred",
			DescriptorName: "DELIVERY_MODE_DEFERRED",
		},
	)
	checkAssociatedClosedEnumCoverage(classification, []NamedScalar{scalar}, associations, &issues)
	if len(issues) != 1 ||
		issues[0].Code != IssueCoverageDeclaration {
		t.Fatalf("descriptor enum drift issues = %+v", issues)
	}
}

func syntheticEnumDescriptor(t *testing.T, members ...string) protoreflect.EnumDescriptor {
	t.Helper()
	values := []*descriptorpb.EnumValueDescriptorProto{{
		Name:   proto.String("DELIVERY_MODE_UNSPECIFIED"),
		Number: proto.Int32(0),
	}}
	for index, member := range members {
		values = append(values, &descriptorpb.EnumValueDescriptorProto{
			Name:   proto.String(member),
			Number: proto.Int32(int32(index + 1)),
		})
	}
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("fixture/closed_enum.proto"),
		Package: proto.String("fixture"),
		Syntax:  proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:  proto.String("DeliveryMode"),
			Value: values,
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return file.Enums().ByName("DeliveryMode")
}

func TestExceptionalWireFingerprintComparesLiveShapeToImmutableSignoff(t *testing.T) {
	legacyType := reflect.TypeFor[exceptionalFingerprintFixture]()
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("fixture/exceptional.proto"),
		Package: proto.String("fixture.exceptional"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Exceptional"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:   proto.String("value"),
				Number: proto.Int32(1),
				Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	liveMessage := file.Messages().ByName(protoreflect.Name("Exceptional"))
	signoff := WireException{
		LegacyType:            legacyType,
		Message:               liveMessage.FullName(),
		Classification:        WireExceptionFieldReshape,
		LegacyFingerprint:     fingerprintExceptionalLegacyType(legacyType),
		DescriptorFingerprint: fingerprintExceptionalDescriptor(liveMessage),
	}
	if err := checkWireExceptionFingerprint(legacyType, liveMessage, signoff); err != nil {
		t.Fatalf("reviewed signoff rejected: %v", err)
	}

	signoff.DescriptorFingerprint = "reviewed-descriptor"
	err = checkWireExceptionFingerprint(legacyType, liveMessage, signoff)
	var fingerprintError *wireExceptionFingerprintError
	if !errors.As(err, &fingerprintError) ||
		fingerprintError.Code != wireExceptionFingerprintDescriptorChanged {
		t.Fatalf("descriptor mutation error = %v", err)
	}

	signoff.DescriptorFingerprint = fingerprintExceptionalDescriptor(liveMessage)
	signoff.LegacyFingerprint = "reviewed-legacy"
	err = checkWireExceptionFingerprint(legacyType, liveMessage, signoff)
	fingerprintError = nil
	if !errors.As(err, &fingerprintError) ||
		fingerprintError.Code != wireExceptionFingerprintLegacyChanged {
		t.Fatalf("legacy mutation error = %v", err)
	}
}

func TestWireExceptionClassificationRejectsMissingAndUnknownValues(t *testing.T) {
	for _, classification := range []WireExceptionClassification{
		WireExceptionClassificationUnspecified,
		WireExceptionClassification(99),
	} {
		if classification.valid() {
			t.Fatalf("classification %d accepted", classification)
		}
	}
	for _, classification := range []WireExceptionClassification{
		WireExceptionCustomWire,
		WireExceptionOneofReshape,
		WireExceptionFieldReshape,
		WireExceptionCollectionReshape,
		WireExceptionEmptyAcknowledment,
	} {
		if !classification.valid() {
			t.Fatalf("classification %d rejected", classification)
		}
	}
}
