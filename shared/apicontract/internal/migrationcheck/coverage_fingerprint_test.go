package migrationcheck

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

type exceptionalFingerprintFixture struct {
	Value string
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
	if err == nil || !strings.Contains(err.Error(), "descriptor focused fixture changed") {
		t.Fatalf("descriptor mutation error = %v", err)
	}

	signoff.DescriptorFingerprint = fingerprintExceptionalDescriptor(liveMessage)
	signoff.LegacyFingerprint = "reviewed-legacy"
	err = checkWireExceptionFingerprint(legacyType, liveMessage, signoff)
	if err == nil || !strings.Contains(err.Error(), "legacy focused fixture changed") {
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
