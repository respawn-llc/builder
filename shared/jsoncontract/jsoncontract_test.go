package jsoncontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	invjsonschema "github.com/invopop/jsonschema"
	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

type profileChild struct {
	Label string `json:"label"`
}

type internalProfileFixture struct {
	Required string        `json:"required"`
	Optional *string       `json:"optional,omitempty" jsonschema:"nullable"`
	Child    *profileChild `json:"child,omitempty"`
}

type functionProfileFixture struct {
	Required string        `json:"required"`
	Optional *string       `json:"optional,omitempty" jsonschema:"nullable"`
	Child    *profileChild `json:"child,omitempty"`
}

type structuredProfileFixture struct {
	Required string  `json:"required"`
	Optional *string `json:"optional" jsonschema:"nullable"`
}

func TestPreparedInternalContractUsesDraft2020AndReferences(t *testing.T) {
	preparer := NewPreparer(false)
	contract, err := preparer.Internal("internal fixture", internalProfileFixture{})
	if err != nil {
		t.Fatalf("prepare internal contract: %v", err)
	}

	document := decodeDocument(t, contract.JSON())
	if got := document["$schema"]; got != invjsonschema.Version {
		t.Fatalf("$schema = %v, want %q", got, invjsonschema.Version)
	}
	if _, ok := document["$defs"]; !ok {
		t.Fatal("internal contract omitted dependency-generated references")
	}
	if err := contract.Validate([]byte(`{"required":"ok","child":{"label":"nested"}}`)); err != nil {
		t.Fatalf("validate referenced document: %v", err)
	}
}

func TestPreparedFunctionContractIsClosedReferenceFreeAndNonStrict(t *testing.T) {
	preparer := NewPreparer(false)
	contract, err := preparer.Function("function fixture", functionProfileFixture{})
	if err != nil {
		t.Fatalf("prepare function contract: %v", err)
	}

	document := decodeDocument(t, contract.JSON())
	if _, ok := document["$defs"]; ok {
		t.Fatal("function contract contains $defs")
	}
	if _, ok := document["$ref"]; ok {
		t.Fatal("function contract contains a root $ref")
	}
	if got := document["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %v, want false", got)
	}
	required := stringSet(t, document["required"])
	if !required["required"] {
		t.Fatal("required field is not required")
	}
	if required["optional"] {
		t.Fatal("omitempty field unexpectedly required")
	}
	if contract.Strict() {
		t.Fatal("ordinary function contract is strict")
	}
	if err := contract.Validate([]byte(`{"required":"ok","unknown":true}`)); err == nil {
		t.Fatal("closed function contract accepted an unknown field")
	}
}

func TestPreparedStructuredContractIsClosedReferenceFreeFullyRequiredAndStrict(t *testing.T) {
	preparer := NewPreparer(false)
	contract, err := preparer.Structured("structured fixture", structuredProfileFixture{})
	if err != nil {
		t.Fatalf("prepare structured contract: %v", err)
	}

	document := decodeDocument(t, contract.JSON())
	if _, ok := document["$defs"]; ok {
		t.Fatal("structured contract contains $defs")
	}
	if got := document["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %v, want false", got)
	}
	required := stringSet(t, document["required"])
	if !required["required"] || !required["optional"] {
		t.Fatalf("required fields = %v, want required and optional", required)
	}
	optional := document["properties"].(map[string]any)["optional"].(map[string]any)
	if _, ok := optional["oneOf"]; !ok {
		t.Fatalf("nullable field schema = %#v, want supported nullable union", optional)
	}
	if !contract.Strict() {
		t.Fatal("structured output contract is not strict")
	}
	if err := contract.Validate([]byte(`{"required":"ok","optional":null}`)); err != nil {
		t.Fatalf("validate nullable structured field: %v", err)
	}
	if err := contract.Validate([]byte(`{"required":"ok"}`)); err == nil {
		t.Fatal("structured contract accepted an absent fully-required field")
	}
}

func TestValidationReturnsParsedReadOnlyValuesAndDependencyErrors(t *testing.T) {
	preparer := NewPreparer(false)
	contract, err := preparer.Function("value fixture", functionProfileFixture{})
	if err != nil {
		t.Fatalf("prepare function contract: %v", err)
	}

	value, err := contract.ValidateValue([]byte("{\n  \"required\": \"ok\",\n  \"optional\": null\n}"))
	if err != nil {
		t.Fatalf("validate value: %v", err)
	}
	required, ok := value.Field("required")
	if !ok {
		t.Fatal("validated object omitted required field")
	}
	if got, ok := required.String(); !ok || got != "ok" {
		t.Fatalf("required value = %q, %v", got, ok)
	}
	compact, err := value.CompactJSON()
	if err != nil {
		t.Fatalf("compact value: %v", err)
	}
	if !bytes.Equal(compact, []byte(`{"optional":null,"required":"ok"}`)) {
		t.Fatalf("compact value = %s", compact)
	}

	_, err = contract.ValidateValue([]byte(`{"required":7}`))
	if err == nil {
		t.Fatal("wrong-typed value passed validation")
	}
	var validationErr *validator.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("validation error type = %T, want dependency ValidationError", err)
	}
	if len(validationErr.Causes) == 0 || len(validationErr.Causes[0].InstanceLocation) == 0 {
		t.Fatalf("validation error omitted dependency detail: %v", validationErr)
	}
}

func TestValidatedValuesExposeReadOnlyObjectFields(t *testing.T) {
	preparer := NewPreparer(false)
	contract, err := preparer.Internal(
		"dynamic object fixture",
		map[string]any{},
		func(schema *invjsonschema.Schema) error {
			schema.Type = "object"
			schema.AdditionalProperties = invjsonschema.TrueSchema
			return nil
		},
	)
	if err != nil {
		t.Fatalf("prepare dynamic object contract: %v", err)
	}
	value, err := contract.ValidateValue([]byte(`{"z":false,"a":{"nested":true}}`))
	if err != nil {
		t.Fatalf("validate dynamic object: %v", err)
	}
	fields, err := value.ObjectFields()
	if err != nil {
		t.Fatalf("ObjectFields: %v", err)
	}
	if len(fields) != 2 || fields[0].Name != "a" || fields[1].Name != "z" {
		t.Fatalf("object fields = %+v, want sorted a,z", fields)
	}
	compact, err := fields[0].Value.CompactJSON()
	if err != nil {
		t.Fatalf("compact nested field: %v", err)
	}
	if string(compact) != `{"nested":true}` {
		t.Fatalf("nested field = %s", compact)
	}
}

func TestPreparedSchemaJSONIsImmutableToCallers(t *testing.T) {
	preparer := NewPreparer(false)
	contract, err := preparer.Function("immutable fixture", functionProfileFixture{})
	if err != nil {
		t.Fatalf("prepare function contract: %v", err)
	}

	first := contract.JSON()
	first[0] = '['
	second := contract.JSON()
	if second[0] != '{' {
		t.Fatalf("mutating returned schema bytes changed prepared contract: %s", second)
	}
	if err := contract.Validate([]byte(`{"required":"ok"}`)); err != nil {
		t.Fatalf("mutating returned bytes changed validator: %v", err)
	}
}

func TestPreparedProviderValuesReportPreparationState(t *testing.T) {
	var emptyFunction Function
	var emptyStructured Structured
	if emptyFunction.Prepared() || emptyStructured.Prepared() {
		t.Fatal("zero provider contract reports prepared")
	}
	preparer := NewPreparer(false)
	function, err := preparer.Function("function fixture", functionProfileFixture{})
	if err != nil {
		t.Fatalf("prepare function contract: %v", err)
	}
	structured, err := preparer.Structured("structured fixture", structuredProfileFixture{})
	if err != nil {
		t.Fatalf("prepare structured contract: %v", err)
	}
	if !function.Prepared() || !structured.Prepared() {
		t.Fatal("prepared provider contract reports unprepared")
	}
}

func TestPreparationFailureReturnsOwnerRichErrorInProduction(t *testing.T) {
	preparer := NewPreparer(false)
	_, err := preparer.Internal(
		"workflow prior values",
		internalProfileFixture{},
		Customize(func(schema *invjsonschema.Schema) error {
			schema.Type = "not-a-json-schema-type"
			return nil
		}),
	)
	if err == nil {
		t.Fatal("invalid customized schema unexpectedly prepared")
	}
	if !strings.Contains(err.Error(), "workflow prior values") {
		t.Fatalf("preparation error omits owner: %v", err)
	}
}

func TestPreparationFailurePanicsInDebugMode(t *testing.T) {
	preparer := NewPreparer(true)
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("debug preparation failure did not panic")
		}
		if !strings.Contains(recovered.(error).Error(), "reviewer suggestions") {
			t.Fatalf("debug panic omits owner: %v", recovered)
		}
	}()
	_, _ = preparer.Structured(
		"reviewer suggestions",
		structuredProfileFixture{},
		Customize(func(schema *invjsonschema.Schema) error {
			schema.Type = "not-a-json-schema-type"
			return nil
		}),
	)
}

func decodeDocument(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode schema document: %v", err)
	}
	return document
}

func stringSet(t *testing.T, raw any) map[string]bool {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("string list = %#v", raw)
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("string list member = %#v", value)
		}
		result[text] = true
	}
	return result
}
