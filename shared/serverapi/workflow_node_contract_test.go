package serverapi

import (
	"errors"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowNodeUpdateRejectsNoncanonicalJoinProviderIdentity(t *testing.T) {
	request := WorkflowNodeUpdateRequest{
		WorkflowID:     runtimeids.NewWorkflowID(),
		NodeID:         runtimeids.NewGraphEntityID(),
		Key:            "join",
		Kind:           string(WorkflowNodeKindJoin),
		DisplayName:    "Join",
		CompletionMode: "",
		JoinInputProviders: []WorkflowJoinInputProvider{{
			InputName:      "result",
			ProviderEdgeID: "edge-prefixed",
		}},
	}

	err := request.Validate()
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != WorkflowRequestErrorInvalidValue ||
		validationErr.Field != "join_input_provider.provider_edge_id" {
		t.Fatalf("Validate error = %#v, want canonical provider identity rejection", err)
	}
}
