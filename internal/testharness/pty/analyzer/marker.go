package analyzer

import "core/internal/testharness/pty/checkpoint"

func EncodePhaseMarker(marker PhaseMarker) ([]byte, error) {
	return checkpoint.Encode(checkpoint.Marker{
		Sequence: marker.Sequence,
		Kind:     marker.Phase,
		WindowID: marker.WindowID,
	})
}
