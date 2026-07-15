package app

import (
	"reflect"
	"testing"
)

func TestTerminalPolicyDesiredOwnership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		geometry    terminalGeometry
		destination terminalDestination
		wantOwned   bool
	}{
		{
			name:        "unknown ongoing",
			geometry:    terminalGeometryUnknown(),
			destination: terminalDestinationOngoing,
			wantOwned:   false,
		},
		{
			name:        "unknown detail",
			geometry:    terminalGeometryUnknown(),
			destination: terminalDestinationDetail,
			wantOwned:   false,
		},
		{
			name:        "tiny ongoing",
			geometry:    terminalGeometryKnown(39, 9),
			destination: terminalDestinationOngoing,
			wantOwned:   false,
		},
		{
			name:        "narrow but supported ongoing",
			geometry:    terminalGeometryKnown(40, 10),
			destination: terminalDestinationOngoing,
			wantOwned:   true,
		},
		{
			name:        "wide ongoing",
			geometry:    terminalGeometryKnown(120, 40),
			destination: terminalDestinationOngoing,
			wantOwned:   true,
		},
		{
			name:        "detail inhibitor",
			geometry:    terminalGeometryKnown(120, 40),
			destination: terminalDestinationDetail,
			wantOwned:   false,
		},
		{
			name:        "status inhibitor",
			geometry:    terminalGeometryKnown(120, 40),
			destination: terminalDestinationStatus,
			wantOwned:   false,
		},
		{
			name:        "rollback inhibitor",
			geometry:    terminalGeometryKnown(120, 40),
			destination: terminalDestinationRollback,
			wantOwned:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := desiredOngoingOwnership(test.geometry, test.destination); got != test.wantOwned {
				t.Fatalf("desired ownership = %t, want %t", got, test.wantOwned)
			}
		})
	}
}

func TestTerminalGeometryHasNoParallelPresenceAuthority(t *testing.T) {
	t.Parallel()

	unknown := terminalGeometryUnknown()
	if unknown.IsKnown() {
		t.Fatal("unknown geometry reports known")
	}
	if unknown.Size() != nil {
		t.Fatal("unknown geometry exposes a fallback size")
	}

	known := terminalGeometryKnown(80, 24)
	size := known.Size()
	if size == nil || size.width != 80 || size.height != 24 {
		t.Fatalf("known geometry = %+v, want 80×24", size)
	}
}

func TestOwnershipReconcileIsIdempotent(t *testing.T) {
	t.Parallel()

	var calls []bool
	reconciler := newOngoingOwnershipReconciler(func(owned bool) error {
		calls = append(calls, owned)
		return nil
	})

	first := reconciler.Reconcile(terminalGeometryKnown(80, 24), terminalDestinationOngoing)
	if first.Err != nil {
		t.Fatalf("first reconciliation error: %v", first.Err)
	}
	if !first.Changed || !first.DesiredOwned {
		t.Fatalf("first reconciliation = %+v, want one owned transition", first)
	}
	second := reconciler.Reconcile(terminalGeometryKnown(80, 24), terminalDestinationOngoing)
	if second.Changed {
		t.Fatalf("identical reconciliation changed state: %+v", second)
	}
	if !reflect.DeepEqual(calls, []bool{true}) {
		t.Fatalf("setter calls = %v, want one true call", calls)
	}
}

func TestOwnershipReconcileOrderingAcrossOverlayAndResizeTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		steps []struct {
			geometry    terminalGeometry
			destination terminalDestination
		}
		want []bool
	}{
		{
			name: "shrink open detail close detail grow",
			steps: []struct {
				geometry    terminalGeometry
				destination terminalDestination
			}{
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(39, 9), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(39, 9), destination: terminalDestinationDetail},
				{geometry: terminalGeometryKnown(39, 9), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationOngoing},
			},
			want: []bool{true, false, true},
		},
		{
			name: "open detail shrink close detail grow",
			steps: []struct {
				geometry    terminalGeometry
				destination terminalDestination
			}{
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationDetail},
				{geometry: terminalGeometryKnown(39, 9), destination: terminalDestinationDetail},
				{geometry: terminalGeometryKnown(39, 9), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationOngoing},
			},
			want: []bool{true, false, true},
		},
		{
			name: "grow while each overlay remains active",
			steps: []struct {
				geometry    terminalGeometry
				destination terminalDestination
			}{
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(80, 24), destination: terminalDestinationStatus},
				{geometry: terminalGeometryKnown(120, 40), destination: terminalDestinationStatus},
				{geometry: terminalGeometryKnown(120, 40), destination: terminalDestinationRollback},
				{geometry: terminalGeometryKnown(160, 50), destination: terminalDestinationRollback},
				{geometry: terminalGeometryKnown(160, 50), destination: terminalDestinationOngoing},
			},
			want: []bool{true, false, true},
		},
		{
			name: "unknown tiny supported startup",
			steps: []struct {
				geometry    terminalGeometry
				destination terminalDestination
			}{
				{geometry: terminalGeometryUnknown(), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(39, 9), destination: terminalDestinationOngoing},
				{geometry: terminalGeometryKnown(40, 10), destination: terminalDestinationOngoing},
			},
			want: []bool{true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []bool
			reconciler := newOngoingOwnershipReconciler(func(owned bool) error {
				calls = append(calls, owned)
				return nil
			})
			for _, step := range test.steps {
				result := reconciler.Reconcile(step.geometry, step.destination)
				if result.Err != nil {
					t.Fatalf("reconcile %+v: %v", step, result.Err)
				}
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("ownership calls = %v, want %v", calls, test.want)
			}
		})
	}
}
