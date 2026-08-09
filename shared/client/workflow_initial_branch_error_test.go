package client

import (
	"errors"
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestProtocolErrorDecodesWorkflowTaskInitialBranchError(t *testing.T) {
	ref := "refs/remotes/upstream/feature/MBL-742"
	remote := "upstream"
	source := &serverapi.WorkflowTaskInitialBranchError{
		Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonRemoteTrackingCollision,
		BranchName: "feature/MBL-742",
		Ref:        &ref,
		Remote:     &remote,
	}

	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeWorkflowTaskInitialBranch,
		Message: source.Error(),
		Data:    source.RPCErrorData(),
	})

	var decoded *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskInitialBranchError", err, err)
	}
	if !reflect.DeepEqual(decoded, source) {
		t.Fatalf("decoded error = %+v, want %+v", decoded, source)
	}
}
