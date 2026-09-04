package protoapi

import (
	"fmt"

	chatpb "core/shared/protoapi/gen/kent/api/chat"
	"google.golang.org/protobuf/proto"
)

type ChatTargetRequest interface {
	proto.Message
	GetTarget() *chatpb.ChatTarget
}

func ChatTargetFromRequest(request ChatTargetRequest) (*chatpb.ChatTarget, error) {
	if request == nil {
		return nil, fmt.Errorf("Chat target request is required")
	}
	target := request.GetTarget()
	if err := Validate(target); err != nil {
		return nil, fmt.Errorf("Chat target: %w", err)
	}
	return target, nil
}
