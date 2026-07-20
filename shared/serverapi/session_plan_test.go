package serverapi

import (
	"encoding/json"
	"testing"
)

func TestSessionPlanUsesTypedOptionalNameOnWire(t *testing.T) {
	absent := SessionPlan{}
	if err := absent.Validate(); err != nil {
		t.Fatalf("validate absent session name: %v", err)
	}
	encoded, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("marshal absent session name: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode absent session name fields: %v", err)
	}
	rawName, fieldPresent := fields["session_name"]
	if !fieldPresent {
		t.Fatal("absent session name omitted its required wire field")
	}
	var decodedName *string
	if err := json.Unmarshal(rawName, &decodedName); err != nil {
		t.Fatalf("decode absent session name value: %v", err)
	}
	if decodedName != nil {
		t.Fatalf("absent session name = %v, want null", decodedName)
	}

	for name, value := range map[string]string{"empty": "", "blank": " \t "} {
		value := value
		t.Run(name, func(t *testing.T) {
			if err := (SessionPlan{SessionName: &value}).Validate(); err == nil {
				t.Fatal("session plan accepted a present empty or blank name")
			}
		})
	}

	title := "Incident triage"
	present := SessionPlan{SessionName: &title}
	if err := present.Validate(); err != nil {
		t.Fatalf("validate present session name: %v", err)
	}
	encoded, err = json.Marshal(present)
	if err != nil {
		t.Fatalf("marshal present session name: %v", err)
	}
	var decoded SessionPlan
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal present session name: %v", err)
	}
	if decoded.SessionName == nil || *decoded.SessionName != title {
		t.Fatalf("decoded session name = %v, want %q", decoded.SessionName, title)
	}
}
