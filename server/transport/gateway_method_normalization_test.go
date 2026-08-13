package transport

import (
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
)

func TestGatewayPreservesMethodNormalizationBoundaries(t *testing.T) {
	t.Run("progress scheduling normalizes method", func(t *testing.T) {
		request := protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "progress",
			Method:  " \t" + protocol.MethodRunPrompt + "\n",
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("whitespace-bearing progress request validation: %v", err)
		}

		schedule := gatewayRequestScheduleFor(request)
		if schedule.kind != gatewayRequestScheduleProgress {
			t.Fatalf("schedule kind = %d, want progress", schedule.kind)
		}
		if schedule.progress == nil {
			t.Fatal("progress schedule has no handler")
		}
		if schedule.progressRoute.Method != protocol.MethodRunPrompt ||
			schedule.progressRoute.Kind != apicontract.KindProgress {
			t.Fatalf("progress route = %+v, want canonical Run Prompt progress route", schedule.progressRoute)
		}
	})

	t.Run("ordinary dispatch does not normalize method", func(t *testing.T) {
		request := protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "ordinary",
			Method:  " " + protocol.MethodProjectList + " ",
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("whitespace-bearing ordinary request validation: %v", err)
		}
		if schedule := gatewayRequestScheduleFor(request); schedule.kind != gatewayRequestScheduleOrdinary {
			t.Fatalf("schedule kind = %d, want ordinary", schedule.kind)
		}

		response := (&Gateway{}).dispatch(context.Background(), &connectionState{handshakeDone: true}, request)
		if response.Error == nil || response.Error.Code != protocol.ErrCodeMethodNotFound {
			t.Fatalf("ordinary response = %+v, want method-not-found", response)
		}
	})
}
