package serverapi

import (
	"testing"

	"core/shared/clientui"
	"core/shared/textutil"
)

func TestRuntimeSubmitUserTurnResponseValidatesTypedOutcome(t *testing.T) {
	t.Parallel()
	blank := ""
	tests := []struct {
		name     string
		response RuntimeSubmitUserTurnResponse
		wantErr  bool
	}{
		{name: "missing result kind", response: RuntimeSubmitUserTurnResponse{}, wantErr: true},
		{
			name:     "unknown result kind",
			response: RuntimeSubmitUserTurnResponse{ResultKind: clientui.UserTurnResultKind("future")},
			wantErr:  true,
		},
		{
			name: "valid queued",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind:  clientui.UserTurnResultKindQueued,
				Steered:     true,
				QueueItemID: "queue-1",
			},
		},
		{
			name: "queued requires queue identity",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindQueued,
				Steered:    true,
			},
			wantErr: true,
		},
		{
			name: "valid assistant final",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindAssistantFinal,
				Message:    textutil.Value("answer"),
			},
		},
		{
			name: "assistant final requires message",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindAssistantFinal,
			},
			wantErr: true,
		},
		{
			name: "assistant final rejects blank message",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindAssistantFinal,
				Message:    textutil.Value(" \n\t "),
			},
			wantErr: true,
		},
		{
			name: "valid no final",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindNoFinal,
			},
		},
		{
			name: "no final rejects message",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindNoFinal,
				Message:    textutil.Value("unexpected"),
			},
			wantErr: true,
		},
		{
			name: "valid silent final preserves present empty message",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindSilentFinal,
				Message:    &blank,
			},
		},
		{
			name: "silent final requires present empty message",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindSilentFinal,
			},
			wantErr: true,
		},
		{
			name: "silent final rejects nonempty message",
			response: RuntimeSubmitUserTurnResponse{
				ResultKind: clientui.UserTurnResultKindSilentFinal,
				Message:    textutil.Value("not blank"),
			},
			wantErr: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.response.Validate()
			if (err != nil) != testCase.wantErr {
				t.Fatalf("Validate() error = %v, wantErr=%t", err, testCase.wantErr)
			}
		})
	}
}
