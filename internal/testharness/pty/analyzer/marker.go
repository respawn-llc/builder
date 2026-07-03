package analyzer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

func EncodePhaseMarker(marker PhaseMarker) ([]byte, error) {
	if marker.Sequence <= 0 {
		return nil, fmt.Errorf("phase marker sequence must be positive: %d", marker.Sequence)
	}
	phase, err := protocolPhase(marker.Phase)
	if err != nil {
		return nil, err
	}
	if err := validateWindowEventID(marker.Phase, marker.WindowID); err != nil {
		return nil, err
	}
	payload := phaseMarkerJSON{
		Version: 1,
		Seq:     marker.Sequence,
		Phase:   phase,
	}
	if marker.WindowID != nil {
		raw := marker.WindowID.String()
		payload.WindowID = &raw
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal phase marker: %w", err)
	}
	output := make([]byte, 0, len(encoded)+64)
	output = append(output, "\x1b]777;kent-pty-phase;"...)
	output = base64.RawURLEncoding.AppendEncode(output, encoded)
	output = append(output, '\a')
	return output, nil
}

func protocolPhase(phase PhaseKind) (string, error) {
	switch phase {
	case PhaseScenarioStart:
		return "ScenarioStart", nil
	case PhaseWindowStart:
		return "WindowStart", nil
	case PhaseWindowEnd:
		return "WindowEnd", nil
	case PhaseReadyForQuit:
		return "ReadyForQuit", nil
	case PhaseScenarioComplete:
		return "ScenarioComplete", nil
	default:
		return "", fmt.Errorf("unknown phase kind %d", phase)
	}
}
