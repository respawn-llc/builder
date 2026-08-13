package protoapi_test

import (
	"bytes"
	"testing"

	"core/shared/protoapi"
	fixturepb "core/shared/protoapi/gen/fixture"
	"google.golang.org/protobuf/proto"
)

func TestGeneratedMessageCodecPreservesUnknownFields(t *testing.T) {
	encoded, err := proto.Marshal(validSchemaConventionsFixture())
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	encoded = append(encoded, 0xa0, 0x06, 0x07)

	decoded := new(fixturepb.SchemaConventionsFixture)
	if err := protoapi.DecodeGeneratedMessage(encoded, decoded); err != nil {
		t.Fatalf("decode generated message: %v", err)
	}
	reencoded, err := protoapi.EncodeGeneratedMessage(decoded)
	if err != nil {
		t.Fatalf("encode generated message: %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatalf("re-encoded bytes = %v, want %v", reencoded, encoded)
	}
}

func TestGeneratedMessageCodecRejectsUnknownEnumValues(t *testing.T) {
	message := validSchemaConventionsFixture()
	message.State = fixturepb.ConventionState(99)
	encoded, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	if err := protoapi.DecodeGeneratedMessage(encoded, new(fixturepb.SchemaConventionsFixture)); err == nil {
		t.Fatal("unknown enum value unexpectedly decoded")
	}
}

func TestGeneratedMessageCodecExecutesProtovalidateConstraints(t *testing.T) {
	message := validSchemaConventionsFixture()
	message.ProviderId = ""
	encoded, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	if err := protoapi.DecodeGeneratedMessage(encoded, new(fixturepb.SchemaConventionsFixture)); err == nil {
		t.Fatal("constraint violation unexpectedly decoded")
	}
	if _, err := protoapi.EncodeGeneratedMessage(message); err == nil {
		t.Fatal("constraint violation unexpectedly encoded")
	}
	if err := protoapi.ValidateGeneratedMessage(message); err == nil {
		t.Fatal("constraint violation unexpectedly validated")
	}
}
