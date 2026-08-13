package migrationcheck

import (
	"reflect"
	"strings"
	"testing"

	"core/shared/protoapi"
)

type exceptionalFingerprintFixture struct {
	Value string
}

func TestExceptionalWireFingerprintComparesLiveShapeToImmutableSignoff(t *testing.T) {
	legacyType := reflect.TypeFor[exceptionalFingerprintFixture]()
	operation, exists, err := protoapi.OperationByName("kent.api.server.server_service.get_readiness")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("server readiness operation not found")
	}
	liveMessage := operation.Descriptor.Output()
	signoff := WireException{
		LegacyType:            legacyType,
		Message:               liveMessage.FullName(),
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
