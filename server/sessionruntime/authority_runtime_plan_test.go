package sessionruntime

import (
	"context"
	"errors"
	"testing"
)

func TestOpenRuntimeRequiresPlanOnlyWhenSessionHasNoReadyRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)

	_, err := fixture.authority.OpenRuntime(t.Context(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "chat-mutation",
	})
	if !errors.Is(err, ErrAgentRuntimePlanRequired) {
		t.Fatalf("open dormant runtime without plan error = %v, want ErrAgentRuntimePlanRequired", err)
	}

	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	first := openLifecycleRuntime(t, fixture.authority, sessionID, "existing-owner", &plan)
	second, err := fixture.authority.OpenRuntime(t.Context(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "chat-mutation",
	})
	if err != nil {
		t.Fatalf("attach ready runtime without plan: %v", err)
	}
	if second.Resource() != first.Resource() {
		t.Fatalf("attached resource = %v, want ready resource %v", second.Resource(), first.Resource())
	}

	if _, err := second.Release(context.Background(), RuntimeReleaseDetach); err != nil {
		t.Fatalf("release second attachment: %v", err)
	}
	if _, err := first.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("close ready runtime: %v", err)
	}
}
