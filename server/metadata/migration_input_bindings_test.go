package metadata

import (
	"database/sql/driver"
	"strings"
	"testing"
)

func TestDecodeMigrationInputBindingsRequiresArray(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantCount int
		wantError bool
	}{
		{name: "empty array", raw: `[]`},
		{
			name:      "binding array",
			raw:       `[{"name":"summary","source":"transition_output","field":"summary"}]`,
			wantCount: 1,
		},
		{name: "malformed", raw: `[`, wantError: true},
		{name: "scalar", raw: `7`, wantError: true},
		{name: "empty object", raw: `{}`, wantError: true},
		{name: "non-empty object", raw: `{"name":"summary"}`, wantError: true},
		{name: "null", raw: `null`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings, err := decodeMigrationInputBindings(test.raw)
			if test.wantError {
				if err == nil {
					t.Fatalf("decodeMigrationInputBindings accepted %s", test.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeMigrationInputBindings: %v", err)
			}
			if len(bindings) != test.wantCount {
				t.Fatalf("binding count = %d, want %d", len(bindings), test.wantCount)
			}
		})
	}
}

func TestMigrationCurrentInputValuesRejectsInvalidBindingValues(t *testing.T) {
	_, err := migrationCurrentInputValues(nil, []driver.Value{
		"task-1",
		"node-1",
		"",
		`[{"name":"summary","source":"unsupported","field":"summary"}]`,
		`{}`,
		"",
		"TASK-1",
		"Task",
		"Body",
		"",
	})
	if err == nil {
		t.Fatal("migrationCurrentInputValues accepted an unsupported binding source")
	}
	if !strings.Contains(err.Error(), "unsupported binding source") {
		t.Fatalf("migrationCurrentInputValues error = %v", err)
	}
}
