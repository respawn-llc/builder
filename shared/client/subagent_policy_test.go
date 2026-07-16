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
