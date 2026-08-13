package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

type requestAssemblyProbeClient struct {
	capabilityCalls int
}

func (*requestAssemblyProbeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (c *requestAssemblyProbeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	c.capabilityCalls++
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true}, nil
}

func TestDispatchRequestAssemblyRequiresOwningIdentityBeforeProviderWork(t *testing.T) {
	client := &requestAssemblyProbeClient{}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	client.capabilityCalls = 0
	_, err := engine.buildDispatchRequest(context.Background(), "", nil, true, dispatchRequestIdentity{})
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("build dispatch request error = %v, want ErrInvalidRequest", err)
	}
	if client.capabilityCalls != 0 {
		t.Fatalf("provider capability calls = %d, want none before identity validation", client.capabilityCalls)
	}
}
func TestCompactionWindowAdvancesOnlyAfterCommittedReplacement(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors:    []error{errors.New("terminal compaction failure")},
		compactionResponses: []llm.CompactionResponse{remoteCompactionReplacement(1_000, 100, 2_500)},
	}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{Model: "gpt-5", CompactionMode: "native"})
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatal(err)
	}
	withCompactionRetryDelays(t, nil)
	if err := engine.CompactContext(context.Background(), ""); err == nil || codexIdentity(t, client.compactionCalls[0].CodexDispatch).WindowID != store.Meta().SessionID+":0" {
		t.Fatal("failed compaction changed the pre-compaction window")
	}
	client.compactionErr = nil
	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("successful compaction: %v", err)
	}
	if codexIdentity(t, client.compactionCalls[1].CodexDispatch).WindowID != store.Meta().SessionID+":0" {
		t.Fatal("replacement request did not use the pre-compaction window")
	}

	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		request, buildErr := engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
		if buildErr == nil && codexIdentity(t, request.CodexDispatch).WindowID != store.Meta().SessionID+":1" {
			t.Fatal("post-compaction dispatch did not advance to committed generation")
		}
		return buildErr
	})
	if err != nil {
		t.Fatalf("build post-compaction dispatch: %v", err)
	}
}
