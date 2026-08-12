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
		value := value
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
		value := value
		if err := ValidateCommittedAtUnixMs(&value); err == nil {
			t.Fatalf("ValidateCommittedAtUnixMs(%d) succeeded", value)
		}
	}
}
