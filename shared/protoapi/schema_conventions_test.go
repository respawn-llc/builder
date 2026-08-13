package protoapi_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	fixturepb "core/shared/protoapi/gen/fixture"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const javaScriptSafeIntegerMaximum int64 = 1<<53 - 1

type protovalidateParityCase struct {
	Name              string  `json:"name"`
	OptionalLabel     *string `json:"optional_label"`
	OptionalPresence  *string `json:"optional_presence"`
	EmptyPresent      bool    `json:"empty_present"`
	OccurredAtSeconds int64   `json:"occurred_at_seconds"`
	ElapsedSeconds    int64   `json:"elapsed_seconds"`
	State             int32   `json:"state"`
	ProviderID        string  `json:"provider_id"`
	UUIDv4            string  `json:"uuid_v4"`
	SignedSafe        int64   `json:"signed_safe"`
	UnsignedSafe      uint64  `json:"unsigned_safe"`
	Valid             bool    `json:"valid"`
}

func TestProtovalidateParityCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/protovalidate-parity.json")
	if err != nil {
		t.Fatalf("read parity corpus: %v", err)
	}
	var cases []protovalidateParityCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("decode parity corpus: %v", err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			var empty *emptypb.Empty
			if test.EmptyPresent {
				empty = &emptypb.Empty{}
			}
			message := &fixturepb.SchemaConventionsFixture{
				OptionalLabel:    test.OptionalLabel,
				OptionalPresence: test.OptionalPresence,
				Empty:            empty,
				OccurredAt:       &timestamppb.Timestamp{Seconds: test.OccurredAtSeconds},
				Elapsed:          &durationpb.Duration{Seconds: test.ElapsedSeconds},
				State:            fixturepb.ConventionState(test.State),
				ProviderId:       test.ProviderID,
				UuidV4:           test.UUIDv4,
				SignedSafe:       test.SignedSafe,
				UnsignedSafe:     test.UnsignedSafe,
			}
			err := protovalidate.Validate(message)
			if got := err == nil; got != test.Valid {
				t.Fatalf("valid = %t, want %t (error: %v)", got, test.Valid, err)
			}
		})
	}
}

func TestSchemaConventionsAcceptValidValuesAndOptionalAbsence(t *testing.T) {
	message := validSchemaConventionsFixture()
	message.OptionalLabel = nil

	if err := protovalidate.Validate(message); err != nil {
		t.Fatalf("validate conventions fixture: %v", err)
	}
}

func TestSchemaConventionsPreserveAbsentAndPresentEmptyValuesDistinctly(t *testing.T) {
	tests := []struct {
		name    string
		value   *string
		present bool
	}{
		{name: "absent", value: nil, present: false},
		{name: "present empty", value: new(string), present: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := validSchemaConventionsFixture()
			message.OptionalPresence = test.value
			encoded, err := proto.Marshal(message)
			if err != nil {
				t.Fatalf("marshal presence fixture: %v", err)
			}
			decoded := new(fixturepb.SchemaConventionsFixture)
			if err := proto.Unmarshal(encoded, decoded); err != nil {
				t.Fatalf("unmarshal presence fixture: %v", err)
			}
			if got := decoded.OptionalPresence != nil; got != test.present {
				t.Fatalf("presence after round trip = %t, want %t", got, test.present)
			}
			if test.present && *decoded.OptionalPresence != "" {
				t.Fatalf("present value after round trip = %q, want empty", *decoded.OptionalPresence)
			}
			if err := protovalidate.Validate(decoded); err != nil {
				t.Fatalf("validate presence fixture: %v", err)
			}
		})
	}
}

func TestSchemaConventionsRejectPresentEmptyOptionalValue(t *testing.T) {
	message := validSchemaConventionsFixture()
	message.OptionalLabel = new(string)

	if err := protovalidate.Validate(message); err == nil {
		t.Fatal("present empty optional value unexpectedly validated")
	}
}

func TestSchemaConventionsRequireStandardEmptyTimestampAndDurationMessages(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fixturepb.SchemaConventionsFixture)
	}{
		{name: "Empty", mutate: func(message *fixturepb.SchemaConventionsFixture) { message.Empty = nil }},
		{name: "Timestamp", mutate: func(message *fixturepb.SchemaConventionsFixture) { message.OccurredAt = nil }},
		{name: "Duration", mutate: func(message *fixturepb.SchemaConventionsFixture) { message.Elapsed = nil }},
		{
			name: "invalid Timestamp",
			mutate: func(message *fixturepb.SchemaConventionsFixture) {
				message.OccurredAt = &timestamppb.Timestamp{Seconds: 253402300800}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := validSchemaConventionsFixture()
			test.mutate(message)
			if err := protovalidate.Validate(message); err == nil {
				t.Fatal("invalid well-known-type convention unexpectedly validated")
			}
		})
	}
}

func TestSchemaConventionsAllowStructurallyValidNegativeDuration(t *testing.T) {
	message := validSchemaConventionsFixture()
	message.Elapsed = durationpb.New(-time.Nanosecond)

	if err := protovalidate.Validate(message); err != nil {
		t.Fatalf("negative duration has no reusable convention-level prohibition: %v", err)
	}
}

func TestSchemaConventionsRejectUndefinedEnumValues(t *testing.T) {
	for _, state := range []fixturepb.ConventionState{
		fixturepb.ConventionState_CONVENTION_STATE_UNSPECIFIED,
		fixturepb.ConventionState(99),
	} {
		message := validSchemaConventionsFixture()
		message.State = state
		if err := protovalidate.Validate(message); err == nil {
			t.Fatalf("state %d unexpectedly validated", state)
		}
	}
}

func TestSchemaConventionsValidateProviderStringsAndCanonicalUUIDv4Text(t *testing.T) {
	for _, providerID := range []string{"", " ", " openai", "openai "} {
		message := validSchemaConventionsFixture()
		message.ProviderId = providerID
		if err := protovalidate.Validate(message); err == nil {
			t.Fatalf("provider ID %q unexpectedly validated", providerID)
		}
	}

	for _, uuid := range []string{
		"",
		"550e8400-e29b-11d4-a716-446655440000",
		"550E8400-E29B-41D4-A716-446655440000",
		"550e8400-e29b-41d4-7716-446655440000",
		"{550e8400-e29b-41d4-a716-446655440000}",
	} {
		message := validSchemaConventionsFixture()
		message.UuidV4 = uuid
		if err := protovalidate.Validate(message); err == nil {
			t.Fatalf("UUID %q unexpectedly validated", uuid)
		}
	}
}

func TestSchemaConventionsBound64BitValuesToJavaScriptSafeIntegers(t *testing.T) {
	validBounds := [][2]int64{
		{-javaScriptSafeIntegerMaximum, 0},
		{javaScriptSafeIntegerMaximum, javaScriptSafeIntegerMaximum},
	}
	for _, bounds := range validBounds {
		message := validSchemaConventionsFixture()
		message.SignedSafe = bounds[0]
		message.UnsignedSafe = uint64(bounds[1])
		if err := protovalidate.Validate(message); err != nil {
			t.Fatalf("safe bounds %+v failed validation: %v", bounds, err)
		}
	}

	message := validSchemaConventionsFixture()
	message.SignedSafe = javaScriptSafeIntegerMaximum + 1
	if err := protovalidate.Validate(message); err == nil {
		t.Fatal("unsafe signed integer unexpectedly validated")
	}

	message = validSchemaConventionsFixture()
	message.SignedSafe = -javaScriptSafeIntegerMaximum - 1
	if err := protovalidate.Validate(message); err == nil {
		t.Fatal("unsafe negative signed integer unexpectedly validated")
	}

	message = validSchemaConventionsFixture()
	message.UnsignedSafe = uint64(javaScriptSafeIntegerMaximum) + 1
	if err := protovalidate.Validate(message); err == nil {
		t.Fatal("unsafe unsigned integer unexpectedly validated")
	}
}

func validSchemaConventionsFixture() *fixturepb.SchemaConventionsFixture {
	label := "present"
	return &fixturepb.SchemaConventionsFixture{
		OptionalLabel: &label,
		Empty:         &emptypb.Empty{},
		OccurredAt:    timestamppb.New(time.Unix(1_700_000_000, 0)),
		Elapsed:       durationpb.New(2 * time.Second),
		State:         fixturepb.ConventionState_CONVENTION_STATE_READY,
		ProviderId:    "openai",
		UuidV4:        "550e8400-e29b-41d4-a716-446655440000",
		SignedSafe:    javaScriptSafeIntegerMaximum,
		UnsignedSafe:  uint64(javaScriptSafeIntegerMaximum),
	}
}
