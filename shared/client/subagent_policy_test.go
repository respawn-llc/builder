package client

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestProtocolErrorDecodesSubagentLaunchDenial(t *testing.T) {
	target := "worker"
	data, err := json.Marshal(serverapi.SubagentLaunchDeniedError{
		Kind:           serverapi.SubagentLaunchDenialNotCallable,
		Target:         &target,
		AvailableRoles: []string{"reviewer"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	err = protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeSubagentLaunchDenied,
		Message: "denied",
		Data:    data,
	})
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("decoded error = %T %v, want SubagentLaunchDeniedError", err, err)
	}
	if denied.Kind != serverapi.SubagentLaunchDenialNotCallable || denied.Target == nil || *denied.Target != "worker" {
		t.Fatalf("decoded denial = %+v", denied)
	}
}

func TestProtocolErrorDecodesSubagentLaunchPolicyError(t *testing.T) {
	source := protocol.NewMaxDepthExceededSubagentLaunchPolicyError(3, 2)
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeSubagentLaunchPolicy,
		Message: "launch rejected",
		Data:    source.RPCErrorData(),
	})
	var policyErr *protocol.SubagentLaunchPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("decoded error = %T %v, want SubagentLaunchPolicyError", err, err)
	}
	if policyErr.Kind != protocol.SubagentLaunchPolicyMaxDepthExceeded ||
		policyErr.AttemptedDepth == nil || *policyErr.AttemptedDepth != 3 ||
		policyErr.MaxDepth == nil || *policyErr.MaxDepth != 2 {
		t.Fatalf("decoded policy error = %+v", policyErr)
	}
}

func TestProtocolErrorUsesGenericMessageForMalformedSubagentLaunchPolicyData(t *testing.T) {
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeSubagentLaunchPolicy,
		Message: "launch rejected",
		Data:    json.RawMessage(`{"kind":"max_depth_exceeded"}`),
	})
	var policyErr *protocol.SubagentLaunchPolicyError
	if errors.As(err, &policyErr) {
		t.Fatalf("decoded malformed data as typed error %+v", policyErr)
	}
	if err == nil || err.Error() != "launch rejected" {
		t.Fatalf("decoded malformed error = %v, want generic message", err)
	}
}
