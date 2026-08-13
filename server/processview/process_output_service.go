package processview

import (
	"context"
	"errors"
	"fmt"

	shelltool "core/server/tools/shell"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type ProcessOutputSubscriber interface {
	SubscribeOutput(ctx context.Context, processID string, offsetBytes int64) (shelltool.OutputSubscription, error)
}

type ProcessOutputService struct {
	subscriber ProcessOutputSubscriber
}

func NewProcessOutputService(subscriber ProcessOutputSubscriber) *ProcessOutputService {
	return &ProcessOutputService{subscriber: subscriber}
}

func (s *ProcessOutputService) SubscribeProcessOutput(ctx context.Context, req serverapi.ProcessOutputSubscribeRequest) (serverapi.ProcessOutputSubscription, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.ProcessOutputSubscribeRequest]) (serverapi.ProcessOutputSubscription, error) {
		request := validated.Value()
		return s.subscribeProcessOutputOwner(ctx, request.ProcessID, request.OffsetBytes)
	})
}

func (s *ProcessOutputService) SubscribeProcessOutputValidated(ctx context.Context, req servicecontract.Validated[serverapi.ProcessOutputSubscribeRequest], authorization servicecontract.AuthorizedProcessInActiveProject) (serverapi.ProcessOutputSubscription, error) {
	request := req.Value()
	return s.subscribeProcessOutputOwner(ctx, authorization.ProcessID, request.OffsetBytes)
}

func (s *ProcessOutputService) subscribeProcessOutputOwner(ctx context.Context, processID string, offsetBytes int64) (serverapi.ProcessOutputSubscription, error) {
	if s == nil || s.subscriber == nil {
		return nil, errors.New("process output subscriber is required")
	}
	sub, err := s.subscriber.SubscribeOutput(ctx, processID, offsetBytes)
	if err != nil {
		return nil, fmt.Errorf("process output stream for %q failed: %w", processID, serverapi.NormalizeStreamError(err))
	}
	return &processOutputSubscription{inner: sub}, nil
}

type processOutputSubscription struct {
	inner shelltool.OutputSubscription
}

func (s *processOutputSubscription) Next(ctx context.Context) (clientui.ProcessOutputChunk, error) {
	chunk, err := s.inner.Next(ctx)
	if err != nil {
		return clientui.ProcessOutputChunk{}, serverapi.NormalizeStreamError(err)
	}
	return clientui.ProcessOutputChunk{
		ProcessID:       chunk.ProcessID,
		OffsetBytes:     chunk.OffsetBytes,
		NextOffsetBytes: chunk.NextOffsetBytes,
		Text:            chunk.Text,
	}, nil
}

func (s *processOutputSubscription) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

var _ servicecontract.ProcessOutputService = (*ProcessOutputService)(nil)
