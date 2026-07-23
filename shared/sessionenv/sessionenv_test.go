package sessionenv

import "testing"

func TestLookupSessionID(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
		ok   bool
	}{
		{name: "missing"},
		{name: "blank", env: map[string]string{SessionIDEnv: " \t\n"}},
		{name: "trims", env: map[string]string{SessionIDEnv: " session-1 \n"}, want: "session-1", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := LookupSessionID(func(key string) (string, bool) {
				value, exists := tt.env[key]
				return value, exists
			})
			if got != tt.want || ok != tt.ok {
				t.Fatalf("LookupSessionID = (%q, %t), want (%q, %t)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLookupSessionIDNilLookup(t *testing.T) {
	if got, ok := LookupSessionID(nil); got != "" || ok {
		t.Fatalf("LookupSessionID(nil) = (%q, %t), want empty false", got, ok)
	}
}

func TestLookupRunStepID(t *testing.T) {
	runID, stepID := LookupRunStepID(func(key string) (string, bool) {
		switch key {
		case RunIDEnv:
			return " run-1 ", true
		case StepIDEnv:
			return " step-1 ", true
		default:
			return "", false
		}
	})
	if runID != "run-1" || stepID != "step-1" {
		t.Fatalf("LookupRunStepID = (%q, %q), want run-1 step-1", runID, stepID)
	}
}
