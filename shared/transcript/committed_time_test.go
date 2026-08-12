package transcript

import "testing"

func TestValidateCommittedAtUnixMsAcceptsJavaScriptDateRange(t *testing.T) {
	tests := []int64{
		-8_640_000_000_000_000,
		-1,
		0,
		1,
		8_640_000_000_000_000,
	}
	for _, value := range tests {
		value := CommittedAtUnixMs(value)
		t.Run(string(rune(value)), func(t *testing.T) {
			if err := ValidateCommittedAtUnixMs(&value); err != nil {
				t.Fatalf("ValidateCommittedAtUnixMs(%d): %v", value, err)
			}
		})
	}
	if err := ValidateCommittedAtUnixMs(nil); err != nil {
		t.Fatalf("ValidateCommittedAtUnixMs(nil): %v", err)
	}
}

func TestValidateCommittedAtUnixMsRejectsOutsideJavaScriptDateRange(t *testing.T) {
	for _, value := range []int64{
		-8_640_000_000_000_001,
		8_640_000_000_000_001,
	} {
		value := CommittedAtUnixMs(value)
		if err := ValidateCommittedAtUnixMs(&value); err == nil {
			t.Fatalf("ValidateCommittedAtUnixMs(%d) succeeded", value)
		}
	}
}

func TestDecodeCommittedAtUnixMsFieldRejectsNullRegardlessOfJSONFieldCasing(t *testing.T) {
	for _, data := range []string{
		`{"committed_at_unix_ms":null}`,
		`{"COMMITTED_AT_UNIX_MS":null}`,
		`{"Committed_At_Unix_Ms":null}`,
		`{"committed_at_unix_ms":1,"COMMITTED_AT_UNIX_MS":null}`,
		`{"COMMITTED_AT_UNIX_MS":null,"committed_at_unix_ms":1}`,
	} {
		if _, _, err := DecodeCommittedAtUnixMsField([]byte(data), "committed_at_unix_ms"); err == nil {
			t.Fatalf("accepted malformed committed time field: %s", data)
		}
	}
	value, present, err := DecodeCommittedAtUnixMsField(
		[]byte(`{"COMMITTED_AT_UNIX_MS":0}`),
		"committed_at_unix_ms",
	)
	if err != nil || !present || value == nil || value.UnixMs() != 0 {
		t.Fatalf("decoded present zero = value=%v present=%t err=%v", value, present, err)
	}
}
