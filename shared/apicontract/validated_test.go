package apicontract

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type dualValidatorRequest struct {
	calls *[]string
	err   error
}

func (r dualValidatorRequest) ValidateRPC() error {
	*r.calls = append(*r.calls, "rpc")
	return r.err
}

func (r dualValidatorRequest) Validate() error {
	*r.calls = append(*r.calls, "plain")
	return nil
}

type structuredValidationError struct {
	data json.RawMessage
}

func (e structuredValidationError) Error() string                 { return "structured validation failed" }
func (e structuredValidationError) RPCErrorCode() int             { return 1234 }
func (e structuredValidationError) RPCErrorData() json.RawMessage { return e.data }

func TestWithValidatedUsesOnlyValidateRPCWhenBothValidatorsExist(t *testing.T) {
	calls := make([]string, 0, 2)
	wantErr := structuredValidationError{data: json.RawMessage(`{"field":"title"}`)}
	consumed := false

	_, err := WithValidated(
		dualValidatorRequest{calls: &calls, err: wantErr},
		SemanticValidationRequired,
		func(Validated[dualValidatorRequest]) (struct{}, error) {
			consumed = true
			return struct{}{}, nil
		},
	)

	var gotErr structuredValidationError
	if !errors.As(err, &gotErr) || !reflect.DeepEqual(gotErr, wantErr) {
		t.Fatalf("WithValidated error = %#v, want %#v", err, wantErr)
	}
	if consumed {
		t.Fatal("consumer ran after validation failed")
	}
	if !reflect.DeepEqual(calls, []string{"rpc"}) {
		t.Fatalf("validator calls = %v, want [rpc]", calls)
	}
	var structured interface {
		RPCErrorCode() int
		RPCErrorData() json.RawMessage
	}
	if !errors.As(err, &structured) || structured.RPCErrorCode() != 1234 ||
		!reflect.DeepEqual(structured.RPCErrorData(), wantErr.data) {
		t.Fatalf("structured error = %#v, want preserved code and data", err)
	}
}

func TestWithValidatedRequiresExplicitPolicyWithoutSemanticValidator(t *testing.T) {
	consumed := false
	if _, err := WithValidated(42, SemanticValidationRequired, func(Validated[int]) (struct{}, error) {
		consumed = true
		return struct{}{}, nil
	}); err == nil {
		t.Fatal("WithValidated without validator succeeded")
	}
	if consumed {
		t.Fatal("consumer ran without a semantic validator")
	}

	got, err := WithValidated(42, NoSemanticValidation, func(value Validated[int]) (int, error) {
		return value.Value(), nil
	})
	if err != nil {
		t.Fatalf("WithValidated explicit no-semantic policy: %v", err)
	}
	if got != 42 {
		t.Fatalf("consumer value = %d, want 42", got)
	}
}
