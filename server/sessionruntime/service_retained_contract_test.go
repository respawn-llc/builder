package sessionruntime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/runtimewire"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestServicePassesRuntimeClientFactoryIntoInteractiveRuntime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	calls := 0
	factory := runtimewire.RuntimeClientFactoryFunc(func(_ context.Context, request runtimewire.RuntimeClientRequest) (llm.Client, error) {
		calls++
		if request.Purpose != runtimewire.RuntimeClientPurposeMain {
			t.Fatalf("factory purpose = %v, want main", request.Purpose)
		}
		return &sessionRuntimeTestLLMClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("ok"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}}}, nil
	})
	fixture.api = NewAPI(fixture.metadata, fixture.authority, APIOptions{RuntimeClientFactory: factory})

	activation, err := fixture.api.ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID:       "retained-runtime-client-factory",
		SessionID:             fixture.store.Meta().SessionID,
		OwnerID:               "retained-runtime-client-factory",
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ActiveSettings: config.Settings{
			Model:              "gpt-5",
			ThinkingLevel:      "medium",
			ModelContextWindow: 200000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Timeouts:           config.Timeouts{ModelRequestSeconds: 1},
			Shell:              config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		EnabledToolIDs: []string{string(toolspec.ToolExecCommand)},
		Source:         config.SourceReport{Sources: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	if calls != 1 {
		t.Fatalf("runtime client factory calls = %d, want 1", calls)
	}
	if _, err := fixture.api.ReleaseSessionRuntime(t.Context(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-retained-runtime-client-factory",
		Attachment:      activation.Attachment,
		OwnerID:         "retained-runtime-client-factory",
		DropOwner:       true,
		ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
}
