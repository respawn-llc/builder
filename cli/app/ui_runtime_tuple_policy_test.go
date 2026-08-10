package app

import (
	"testing"

	"core/shared/clientui"
)

func TestRuntimeTuplePolicy(t *testing.T) {
	valid := func(epoch string, generation, sequence uint64) clientui.ReadModelVersion {
		return clientui.ReadModelVersion{Epoch: epoch, Generation: generation, Sequence: sequence}
	}
	tests := []struct {
		name     string
		current  clientui.ReadModelVersion
		incoming clientui.ReadModelVersion
		ingress  runtimeTupleIngress
		want     runtimeTupleDecision
	}{
		{name: "first incremental", incoming: valid("epoch-1", 1, 1), ingress: runtimeTupleIngressIncremental, want: runtimeTupleApply},
		{name: "higher same generation incremental", current: valid("epoch-1", 1, 1), incoming: valid("epoch-1", 1, 2), ingress: runtimeTupleIngressIncremental, want: runtimeTupleApply},
		{name: "equal incremental", current: valid("epoch-1", 1, 2), incoming: valid("epoch-1", 1, 2), ingress: runtimeTupleIngressIncremental, want: runtimeTupleIgnore},
		{name: "lower incremental", current: valid("epoch-1", 1, 2), incoming: valid("epoch-1", 1, 1), ingress: runtimeTupleIngressIncremental, want: runtimeTupleIgnore},
		{name: "older generation incremental", current: valid("epoch-1", 2, 1), incoming: valid("epoch-1", 1, 99), ingress: runtimeTupleIngressIncremental, want: runtimeTupleIgnore},
		{name: "forward generation incremental", current: valid("epoch-1", 1, 99), incoming: valid("epoch-1", 2, 1), ingress: runtimeTupleIngressIncremental, want: runtimeTupleRefresh},
		{name: "new epoch incremental", current: valid("epoch-1", 2, 99), incoming: valid("epoch-2", 1, 1), ingress: runtimeTupleIngressIncremental, want: runtimeTupleRefresh},
		{name: "forward generation unary", current: valid("epoch-1", 1, 99), incoming: valid("epoch-1", 2, 1), ingress: runtimeTupleIngressAuthoritativeSnapshot, want: runtimeTupleApply},
		{name: "new epoch unary", current: valid("epoch-1", 2, 99), incoming: valid("epoch-2", 1, 1), ingress: runtimeTupleIngressAuthoritativeSnapshot, want: runtimeTupleApply},
		{name: "forward generation hydration", current: valid("epoch-1", 1, 99), incoming: valid("epoch-1", 2, 1), ingress: runtimeTupleIngressHydration, want: runtimeTupleApply},
		{name: "new epoch hydration", current: valid("epoch-1", 2, 99), incoming: valid("epoch-2", 1, 1), ingress: runtimeTupleIngressHydration, want: runtimeTupleApply},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideRuntimeTuple(tt.current, tt.incoming, tt.ingress); got != tt.want {
				t.Fatalf("decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHydrationRuntimeTupleAdmissionMapping(t *testing.T) {
	current := runtimeTupleTestView(
		11,
		runtimeTupleTestIdleActivity(),
		runtimeTupleTestReconciliation(clientui.RuntimeInputReconciliationCommitted),
	)
	tests := []struct {
		name        string
		incoming    clientui.RuntimeReadModelUpdate
		wantErr     bool
		wantProject bool
	}{
		{
			name: "exact current tuple is accepted",
			incoming: clientui.RuntimeReadModelUpdate{
				Version:             current.Version,
				Activity:            current.Activity,
				InputReconciliation: current.InputReconciliation,
			},
			wantProject: true,
		},
		{
			name: "lower sequence is developer error",
			incoming: clientui.RuntimeReadModelUpdate{
				Version:             clientui.ReadModelVersion{Epoch: current.Version.Epoch, Generation: current.Version.Generation, Sequence: 10},
				Activity:            current.Activity,
				InputReconciliation: current.InputReconciliation,
			},
			wantErr: true,
		},
		{
			name: "older generation is developer error",
			incoming: clientui.RuntimeReadModelUpdate{
				Version:             clientui.ReadModelVersion{Epoch: current.Version.Epoch, Generation: current.Version.Generation - 1, Sequence: 99},
				Activity:            current.Activity,
				InputReconciliation: current.InputReconciliation,
			},
			wantErr: true,
		},
		{
			name: "same version conflict is developer error",
			incoming: clientui.RuntimeReadModelUpdate{
				Version:             current.Version,
				Activity:            runtimeTupleTestRunningActivity(),
				InputReconciliation: current.InputReconciliation,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &sessionRuntimeClient{
				sessionID:   "session-1",
				mainView:    current,
				hasMainView: true,
			}
			message := ongoingHydrationMessage(1)
			payload := message.Payload().(clientui.TranscriptHydration)
			payload.RuntimeReadModelUpdate = tt.incoming
			message = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(payload))
			result, err := client.admitTranscriptMessageState(message)
			if (err != nil) != tt.wantErr {
				t.Fatalf("admission error = %v, wantErr=%t", err, tt.wantErr)
			}
			if result.project != tt.wantProject {
				t.Fatalf("project = %t, want %t", result.project, tt.wantProject)
			}
			if tt.wantErr {
				assertUnchanged(t, "cache after rejected hydration", client.mainView, current)
			}
		})
	}
}
