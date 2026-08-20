package serverapi

import (
	"testing"
)

func TestSessionPlanUsesTypedOptionalName(t *testing.T) {
	absent := SessionPlan{}
	if err := absent.Validate(); err != nil {
		t.Fatalf("validate absent session name: %v", err)
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
	if present.SessionName == nil || *present.SessionName != title {
		t.Fatalf("present session name = %v, want %q", present.SessionName, title)
	}
}
