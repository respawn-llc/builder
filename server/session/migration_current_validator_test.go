package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentMigrationValidatorRejectsEveryInvalidProviderLinkCombination(t *testing.T) {
	tests := []struct {
		name       string
		outputKind ToolOutputKind
		item       string
	}{
		{
			name:       "other unknown link kind",
			outputKind: ToolOutputKindFunction,
			item:       `{"type":"other","raw":{"type":"message"},"link_kind":"future"}`,
		},
		{
			name:       "other linked call without link kind",
			outputKind: ToolOutputKindFunction,
			item:       `{"type":"other","raw":{"type":"message"},"linked_call_id":"call-1"}`,
		},
		{
			name:       "other attachment without linked call",
			outputKind: ToolOutputKindFunction,
			item:       `{"type":"other","raw":{"type":"message"},"link_kind":"tool_output_attachment"}`,
		},
		{
			name:       "other attachment linked to another call",
			outputKind: ToolOutputKindFunction,
			item:       `{"type":"other","raw":{"type":"message"},"linked_call_id":"call-2","link_kind":"tool_output_attachment"}`,
		},
		{
			name:       "function output has linked call",
			outputKind: ToolOutputKindFunction,
			item:       `{"type":"function_call_output","call_id":"call-1","raw":{"type":"function_call_output"},"linked_call_id":"call-1"}`,
		},
		{
			name:       "function output has link kind",
			outputKind: ToolOutputKindFunction,
			item:       `{"type":"function_call_output","call_id":"call-1","raw":{"type":"function_call_output"},"link_kind":"tool_output_attachment"}`,
		},
		{
			name:       "custom output has linked facts",
			outputKind: ToolOutputKindCustom,
			item:       `{"type":"custom_tool_call_output","call_id":"call-1","raw":{"type":"custom_tool_call_output"},"linked_call_id":"call-1","link_kind":"tool_output_attachment"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line := currentToolCompletionFixtureLine(test.outputKind, test.item)
			if _, err := decodeEventRecordV1(line); err == nil {
				t.Fatal("authoritative v1 codec accepted invalid provider facts")
			}

			path := filepath.Join(t.TempDir(), "events.jsonl")
			document := append(currentEventLogHeaderBytes(t), line...)
			document = append(document, '\n')
			if err := os.WriteFile(path, document, 0o600); err != nil {
				t.Fatalf("write current-v1 fixture: %v", err)
			}
			if _, err := validateCurrentEventLogComplete(
				path,
				newMigrationResourceLedger(),
			); err == nil {
				t.Fatal("bounded pre-install validator accepted invalid provider facts")
			}
		})
	}
}

func TestCurrentMigrationValidatorMatchesNullableToolCompletionFacts(t *testing.T) {
	valid := []byte(
		`{"seq":1,"kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"tool","output_kind":"function",` +
			`"is_error":false,"output":null,"summary":null,"condensed_text":null,` +
			`"provider_items":null}}`,
	)
	if _, err := decodeEventRecordV1(valid); err != nil {
		t.Fatalf("authoritative v1 codec rejected nullable optional facts: %v", err)
	}
	if err := validateCurrentFixtureLine(t, valid); err != nil {
		t.Fatalf("bounded validator rejected nullable optional facts: %v", err)
	}

	invalid := []byte(
		`{"seq":1,"kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"tool","output_kind":"function",` +
			`"is_error":null,"output":null}}`,
	)
	if _, err := decodeEventRecordV1(invalid); err == nil {
		t.Fatal("authoritative v1 codec accepted null required is_error")
	}
	if err := validateCurrentFixtureLine(t, invalid); err == nil {
		t.Fatal("bounded validator accepted null required is_error")
	}
}

func validateCurrentFixtureLine(t *testing.T, line []byte) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	document := append(currentEventLogHeaderBytes(t), line...)
	document = append(document, '\n')
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatalf("write current-v1 fixture: %v", err)
	}
	_, err := validateCurrentEventLogComplete(path, newMigrationResourceLedger())
	return err
}

func currentToolCompletionFixtureLine(
	outputKind ToolOutputKind,
	providerItem string,
) []byte {
	return []byte(
		`{"seq":1,"kind":"tool_completed","payload":{` +
			`"call_id":"call-1","name":"tool","output_kind":"` + string(outputKind) + `",` +
			`"is_error":false,"output":"done","provider_items":[` + providerItem + `]}}`,
	)
}
