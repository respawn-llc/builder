package runtime

import (
	"context"
	"net/http"
	"testing"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/server/llm"
	"core/shared/config"

	"github.com/google/uuid"
)

type workflowCompatibleHTTPAuth struct{}

func (workflowCompatibleHTTPAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer workflow-black-box", nil
}

func TestWorkflowCompatibleResponsesHTTPBlackBoxGatesUnphasedAnswer(t *testing.T) {
	invalid := "ordinary prose"
	valid := `{"commentary":"complete","summary":"done"}`
	stub, err := modelstub.StartResponsesStub([]modelstub.RequiredOperation{
		{
			ID:            uuid.New(),
			Route:         modelstub.RouteResponses,
			Outcome:       modelstub.OutcomeStream,
			Output:        &invalid,
			ResponsePhase: modelstub.NewResponsePhase(modelstub.ResponsePhaseAbsent),
		},
		{
			ID:            uuid.New(),
			Route:         modelstub.RouteResponses,
			Outcome:       modelstub.OutcomeStream,
			Output:        &valid,
			ResponsePhase: modelstub.NewResponsePhase(modelstub.ResponsePhaseAbsent),
		},
	})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(func() {
		if err := stub.Stop(); err != nil {
			t.Errorf("stop Responses stub: %v", err)
		}
	})

	transport := llm.NewHTTPTransport(workflowCompatibleHTTPAuth{})
	transport.BaseURL = stub.URL()
	transport.BaseURLExplicit = true
	transport.Client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	transport.ContextWindowTokens = 200000
	transport.ProviderCapabilitiesOverride = &llm.ProviderCapabilities{
		ProviderID:           "openai-compatible",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   false,
	}

	controller := &fakeWorkflowController{}
	eng := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		llm.NewOpenAIClient(transport),
		testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured),
		Config{},
	)

	if _, err := eng.SubmitUserMessage(context.Background(), "run"); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if got := controller.violations.Load(); got != 1 {
		t.Fatalf("violations = %d, want 1", got)
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	terminal := eng.WorkflowTerminalState()
	if !terminal.Completed || terminal.Source != WorkflowCompletionSourceUnstructured {
		t.Fatalf("terminal state = %+v, want unstructured completion", terminal)
	}
	if err := stub.Verify(); err != nil {
		t.Fatalf("verify Responses stub: %v", err)
	}
	snapshot := stub.Snapshot()
	if len(snapshot.Observed) != 2 ||
		snapshot.Observed[0].Route != modelstub.RouteResponses ||
		snapshot.Observed[1].Route != modelstub.RouteResponses {
		t.Fatalf("observed provider calls = %+v, want exactly two Responses requests", snapshot.Observed)
	}
}
