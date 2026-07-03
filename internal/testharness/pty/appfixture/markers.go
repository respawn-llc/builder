package appfixture

import (
	"fmt"

	"core/internal/testharness/pty"

	"github.com/google/uuid"
)

type RawPhaseMarkerEncoder struct{}

func (RawPhaseMarkerEncoder) EncodeRawPhaseMarker(sequence int, phase string, windowID *uuid.UUID) ([]byte, error) {
	kind, err := phaseKind(phase)
	if err != nil {
		return nil, err
	}
	var typedWindowID *pty.WindowID
	if windowID != nil {
		id, err := pty.NewWindowID(windowID.String())
		if err != nil {
			return nil, err
		}
		typedWindowID = &id
	}
	return pty.EncodePhaseMarker(pty.PhaseMarker{Sequence: sequence, Phase: kind, WindowID: typedWindowID})
}

func phaseKind(phase string) (pty.PhaseKind, error) {
	switch phase {
	case "ScenarioStart":
		return pty.PhaseScenarioStart, nil
	case "WindowStart":
		return pty.PhaseWindowStart, nil
	case "WindowEnd":
		return pty.PhaseWindowEnd, nil
	case "ReadyForQuit":
		return pty.PhaseReadyForQuit, nil
	case "ScenarioComplete":
		return pty.PhaseScenarioComplete, nil
	default:
		return 0, fmt.Errorf("unknown terminal phase %q", phase)
	}
}
