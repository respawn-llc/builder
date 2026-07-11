package analyzer

func EncodePhaseMarker(marker PhaseMarker) ([]byte, error) {
	return Encode(Marker{
		Sequence: marker.Sequence,
		Kind:     marker.Phase,
		WindowID: marker.WindowID,
	})
}
