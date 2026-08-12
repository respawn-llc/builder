package main

import (
	"bytes"
	"testing"
)

func TestTaskCompleteJSONContractRejectsStructuralViolations(t *testing.T) {
	contract, err := prepareTaskCompleteJSONContract()
	if err != nil {
		t.Fatalf("prepare Task Complete JSON contract: %v", err)
	}
	for _, raw := range []string{
		`{`,
		`{} {}`,
		`null`,
		`[]`,
		`"done"`,
		`{"transition":1}`,
		`{"commentary":{}}`,
		`{"output_values":[]}`,
		`{"output_values":"value"}`,
		`{"session_id":"session-1"}`,
		`{"task_id":null}`,
		`{"actor_kind":"agent"}`,
		`{"agent_session_id":"session-1"}`,
		`{"force":false}`,
	} {
		if _, err := contract.Parse(raw); err == nil {
			t.Fatalf("invalid Task Complete JSON accepted: %s", raw)
		}
	}
}

func TestTaskCompleteJSONContractAcceptsNullableReservedFields(t *testing.T) {
	contract, err := prepareTaskCompleteJSONContract()
	if err != nil {
		t.Fatalf("prepare Task Complete JSON contract: %v", err)
	}
	fields, err := contract.Parse(
		`{"transition":null,"transition_id":null,"commentary":null,"output_values":null}`,
	)
	if err != nil {
		t.Fatalf("parse nullable Task Complete JSON: %v", err)
	}
	if fields.TransitionID != "" || fields.Commentary != "" || len(fields.OutputValues) != 0 {
		t.Fatalf("nullable Task Complete fields = %+v, want empty values", fields)
	}
}

func TestTaskCompleteJSONContractStringifiesDynamicParameters(t *testing.T) {
	contract, err := prepareTaskCompleteJSONContract()
	if err != nil {
		t.Fatalf("prepare Task Complete JSON contract: %v", err)
	}
	fields, err := contract.Parse(`{
		"transition":"done",
		"commentary":"evidence",
		"output_values":{
			"nested":{"ok":true},
			"items":["a",2],
			"nil":null,
			"text":"exact"
		},
		"count":3,
		"flag":false
	}`)
	if err != nil {
		t.Fatalf("parse dynamic Task Complete JSON: %v", err)
	}
	if fields.TransitionID != "done" || fields.Commentary != "evidence" {
		t.Fatalf("reserved Task Complete fields = %+v", fields)
	}
	for key, want := range map[string]string{
		"nested": `{"ok":true}`,
		"items":  `["a",2]`,
		"nil":    "null",
		"text":   "exact",
		"count":  "3",
		"flag":   "false",
	} {
		if got := fields.OutputValues[key]; got != want {
			t.Fatalf("output %s = %q, want %q; all outputs = %+v", key, got, want, fields.OutputValues)
		}
	}
}

func TestTaskCompleteJSONContractRetainsTransitionAndOutputMergeRules(t *testing.T) {
	contract, err := prepareTaskCompleteJSONContract()
	if err != nil {
		t.Fatalf("prepare Task Complete JSON contract: %v", err)
	}
	if _, err := contract.Parse(`{"transition":"done","transition_id":"blocked"}`); err == nil {
		t.Fatal("Task Complete JSON accepted disagreeing transition fields")
	}
	fields, err := contract.Parse(`{"output_values":{"value":"nested","other":1},"value":"top"}`)
	if err != nil {
		t.Fatalf("parse merged Task Complete JSON: %v", err)
	}
	if fields.OutputValues["value"] != "top" || fields.OutputValues["other"] != "1" {
		t.Fatalf("merged Task Complete output values = %+v", fields.OutputValues)
	}
	if _, err := contract.Parse(`{"output_values":{"   ":"value"}}`); err == nil {
		t.Fatal("Task Complete JSON accepted a blank output_values field name")
	}
}

func TestTaskCompleteJSONContractUsesDependencyDuplicateFieldSemantics(t *testing.T) {
	contract, err := prepareTaskCompleteJSONContract()
	if err != nil {
		t.Fatalf("prepare Task Complete JSON contract: %v", err)
	}
	fields, err := contract.Parse(
		`{"transition":"first","transition":"second","output_values":{"value":1,"value":2}}`,
	)
	if err != nil {
		t.Fatalf("parse duplicate Task Complete JSON fields: %v", err)
	}
	if fields.TransitionID != "second" || fields.OutputValues["value"] != "2" {
		t.Fatalf("duplicate-field result = %+v, want dependency last-value semantics", fields)
	}
}

func TestTaskCompleteRejectsInvalidJSONBeforeRemoteComposition(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskCompleteSubcommand(
		[]string{"--json", `{"transition":`},
		&stdout,
		&stderr,
	)
	if exitCode != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf(
			"invalid Task Complete JSON exit=%d stdout=%q stderr=%q",
			exitCode,
			stdout.String(),
			stderr.String(),
		)
	}
}
