package runtime

import (
	"context"
	"errors"
	"fmt"

	"core/server/llm"
)

type cacheObservationDispatchError struct {
	cause error
}

func (e *cacheObservationDispatchError) Error() string {
	return fmt.Sprintf("record model response cache observation: %v", e.cause)
}

func (e *cacheObservationDispatchError) Unwrap() error {
	return e.cause
}

type observedModelClient struct {
	generate             func(context.Context, cacheObservedRequest, llm.StreamCallbacks, func()) (llm.Response, error)
	compact              func(context.Context, cacheObservedRequest, func()) (llm.CompactionResponse, error)
	providerCapabilities func(context.Context) (llm.ProviderCapabilities, error)
}

func newObservedModelClient(client llm.Client) *observedModelClient {
	if client == nil {
		return nil
	}
	observed := &observedModelClient{
		generate: func(ctx context.Context, request cacheObservedRequest, callbacks llm.StreamCallbacks, onProviderReturn func()) (llm.Response, error) {
			response, err := client.Generate(ctx, request.request, callbacks)
			if onProviderReturn != nil {
				onProviderReturn()
			}
			if err != nil {
				return llm.Response{}, err
			}
			if err := request.complete(response.Usage); err != nil {
				return llm.Response{}, &cacheObservationDispatchError{cause: err}
			}
			return response, nil
		},
	}
	if compactor, ok := client.(llm.CompactionClient); ok {
		observed.compact = func(ctx context.Context, request cacheObservedRequest, onProviderReturn func()) (llm.CompactionResponse, error) {
			response, err := compactor.Compact(ctx, request.request)
			if onProviderReturn != nil {
				onProviderReturn()
			}
			if err != nil {
				return llm.CompactionResponse{}, err
			}
			if err := request.complete(response.Usage); err != nil {
				return llm.CompactionResponse{}, &cacheObservationDispatchError{cause: err}
			}
			return response, nil
		}
	}
	if provider, ok := client.(llm.ProviderCapabilitiesClient); ok {
		observed.providerCapabilities = provider.ProviderCapabilities
	}
	return observed
}

func (c *observedModelClient) generateObserved(ctx context.Context, request cacheObservedRequest, callbacks llm.StreamCallbacks, onProviderReturn func()) (llm.Response, error) {
	if c == nil || c.generate == nil {
		return llm.Response{}, errors.New("model generation client is unavailable")
	}
	return c.generate(ctx, request, callbacks, onProviderReturn)
}

func (c *observedModelClient) compactObserved(ctx context.Context, request cacheObservedRequest, onProviderReturn func()) (llm.CompactionResponse, error) {
	if c == nil || c.compact == nil {
		return llm.CompactionResponse{}, errors.New("model compaction client is unavailable")
	}
	return c.compact(ctx, request, onProviderReturn)
}

func (c *observedModelClient) supportsCompaction() bool {
	return c != nil && c.compact != nil
}

func (c *observedModelClient) capabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	if c == nil || c.providerCapabilities == nil {
		return llm.ProviderCapabilities{}, errors.New("provider capabilities are unavailable")
	}
	return c.providerCapabilities(ctx)
}
