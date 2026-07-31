package runtimeids

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

const testWorkflowID = "11111111-1111-4111-8111-111111111111"

func TestWorkflowIDCreationAndCanonicalRoundTrips(t *testing.T) {
	generated := NewWorkflowID()
	if generated.IsZero() {
		t.Fatal("NewWorkflowID returned zero")
	}
	if _, err := ParseWorkflowID(generated.String()); err != nil {
		t.Fatalf("NewWorkflowID returned an unparseable value: %v", err)
	}

	id, err := ParseWorkflowID(testWorkflowID)
	if err != nil {
		t.Fatalf("ParseWorkflowID: %v", err)
	}
	if got := id.String(); got != testWorkflowID {
		t.Fatalf("WorkflowID.String() = %q, want %q", got, testWorkflowID)
	}

	jsonBytes, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if got, want := string(jsonBytes), `"`+testWorkflowID+`"`; got != want {
		t.Fatalf("json.Marshal = %s, want %s", got, want)
	}
	var decodedJSON WorkflowID
	if err := json.Unmarshal(jsonBytes, &decodedJSON); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decodedJSON != id {
		t.Fatalf("JSON round trip = %q, want %q", decodedJSON.String(), id.String())
	}

	text, err := id.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if got := string(text); got != testWorkflowID {
		t.Fatalf("MarshalText = %q, want %q", got, testWorkflowID)
	}
	var decodedText WorkflowID
	if err := decodedText.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if decodedText != id {
		t.Fatalf("text round trip = %q, want %q", decodedText.String(), id.String())
	}

	value, err := id.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	rawValue, ok := value.([]byte)
	if !ok {
		t.Fatalf("Value() type = %T, want []byte", value)
	}
	expectedValue := uuid.MustParse(testWorkflowID)
	if !bytes.Equal(rawValue, expectedValue[:]) {
		t.Fatalf("Value() = %x, want %x", rawValue, expectedValue[:])
	}
	if len(rawValue) != 16 {
		t.Fatalf("Value() length = %d, want 16", len(rawValue))
	}
	var decodedSQL WorkflowID
	if err := decodedSQL.Scan(rawValue); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if decodedSQL != id {
		t.Fatalf("SQL round trip = %q, want %q", decodedSQL.String(), id.String())
	}
}

func TestWorkflowIDRejectsNonCanonicalOrInvalidValues(t *testing.T) {
	for _, raw := range []string{
		"workflow-" + testWorkflowID,
		" " + testWorkflowID,
		testWorkflowID + " ",
		"11111111-1111-4111-8111-11111111111A",
		"11111111-1111-1111-8111-111111111111",
		"11111111-1111-4111-0111-111111111111",
		"00000000-0000-0000-0000-000000000000",
		"",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseWorkflowID(raw); err == nil {
				t.Fatalf("ParseWorkflowID(%q) succeeded", raw)
			}
		})
	}
}

func TestWorkflowIDRejectsInvalidJSONTextAndSQLValues(t *testing.T) {
	for _, input := range []string{
		`null`,
		`"workflow-` + testWorkflowID + `"`,
		`"` + testWorkflowID + ` "`,
	} {
		t.Run("json/"+input, func(t *testing.T) {
			var id WorkflowID
			if err := json.Unmarshal([]byte(input), &id); err == nil {
				t.Fatalf("json.Unmarshal(%s) succeeded", input)
			}
		})
	}

	id, err := ParseWorkflowID(testWorkflowID)
	if err != nil {
		t.Fatalf("ParseWorkflowID: %v", err)
	}
	for _, input := range []string{
		"workflow-" + testWorkflowID,
		"",
	} {
		t.Run("text/"+input, func(t *testing.T) {
			var decoded WorkflowID
			if err := decoded.UnmarshalText([]byte(input)); err == nil {
				t.Fatalf("UnmarshalText(%q) succeeded", input)
			}
		})
	}

	for name, input := range map[string]driver.Value{
		"nil":             nil,
		"text":            testWorkflowID,
		"canonical bytes": []byte(testWorkflowID),
		"short bytes":     []byte{1, 2, 3},
		"long bytes":      make([]byte, 17),
	} {
		t.Run("sql/"+name, func(t *testing.T) {
			var decoded WorkflowID
			if err := decoded.Scan(input); err == nil {
				t.Fatalf("Scan(%T) succeeded", input)
			}
		})
	}

	var zero WorkflowID
	if _, err := zero.MarshalJSON(); err == nil {
		t.Fatal("zero WorkflowID marshaled as JSON")
	}
	if _, err := zero.MarshalText(); err == nil {
		t.Fatal("zero WorkflowID marshaled as text")
	}
	if _, err := zero.Value(); err == nil {
		t.Fatal("zero WorkflowID returned a SQL value")
	}
	validBytes := uuid.MustParse(testWorkflowID)
	if err := id.Scan(validBytes[:]); err != nil {
		t.Fatalf("Scan valid bytes: %v", err)
	}
}
