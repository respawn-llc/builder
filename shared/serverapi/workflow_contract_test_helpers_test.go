package serverapi

import (
	"encoding/json"
	"errors"
	"testing"
)

type workflowRequestValidator interface {
	Validate() error
}

type workflowValidRequestCase struct {
	name    string
	request workflowRequestValidator
}

type workflowFieldErrorCase struct {
	name    string
	request workflowRequestValidator
	field   string
	code    string
}

func testValidWorkflowRequests(t *testing.T, cases []workflowValidRequestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.request.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func testWorkflowFieldErrors(t *testing.T, cases []workflowFieldErrorCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.request.Validate()
			if !hasWorkflowRequestError(err, tc.field, tc.code) {
				t.Fatalf("Validate error = %v, want field %q code %q", err, tc.field, tc.code)
			}
		})
	}
}

func hasWorkflowRequestError(err error, field string, code string) bool {
	var requestErr WorkflowRequestValidationError
	return errors.As(err, &requestErr) && requestErr.Field == field && requestErr.Code == code
}

func marshalWorkflowJSON[T any](t *testing.T, value any) ([]byte, T) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	var decoded T
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal %T: %v", value, err)
	}
	return data, decoded
}

func stringPointerForTest(value string) *string {
	return &value
}
