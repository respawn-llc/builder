package blackbox

import "core/internal/testharness/pty/analyzer"

type RunObservation struct {
	Analysis     *analyzer.Analysis
	ClientExited bool
	ServerReady  bool
	Model        StubSnapshot
}

func (p Predicate) Matches(observation RunObservation) bool {
	switch p.Kind {
	case PredicateParseable:
		return observation.Analysis != nil
	case PredicateBlank:
		return observation.Analysis != nil && observation.Analysis.Screen.IsBlank()
	case PredicateNonBlank:
		return observation.Analysis != nil && !observation.Analysis.Screen.IsBlank()
	case PredicateDimensions:
		return observation.Analysis != nil && p.Rows != nil && p.Cols != nil && observation.Analysis.Dimensions.Rows == *p.Rows && observation.Analysis.Dimensions.Cols == *p.Cols
	case PredicatePrivateMode:
		if observation.Analysis == nil || p.Mode == nil || p.Enabled == nil {
			return false
		}
		for _, change := range observation.Analysis.PrivateModeChanges {
			if change.Mode == *p.Mode && change.Enabled == *p.Enabled {
				return true
			}
		}
		return false
	case PredicatePromptReady:
		if observation.Analysis == nil {
			return false
		}
		var lastAlternateExit *int
		alternateScreenActive := false
		var lastVisibleCursor *int
		for index, change := range observation.Analysis.PrivateModeChanges {
			if change.Mode == 1049 {
				if change.Enabled {
					alternateScreenActive = true
					lastVisibleCursor = nil
				} else {
					alternateScreenActive = false
					lastAlternateExit = &index
					lastVisibleCursor = nil
				}
			}
			if change.Mode == 25 && change.Enabled && !alternateScreenActive && (lastAlternateExit == nil || index > *lastAlternateExit) {
				lastVisibleCursor = &index
			}
		}
		return !alternateScreenActive && lastVisibleCursor != nil && (lastAlternateExit == nil || *lastVisibleCursor > *lastAlternateExit)
	case PredicateProcessExited:
		return observation.ClientExited
	case PredicateServerReady:
		return observation.ServerReady
	case PredicateModelConsumed:
		return observation.Model.RequiredConsumed()
	case PredicateNoActiveModels:
		return observation.Model.ActiveRequests == 0
	case PredicateAll:
		for _, child := range p.Children {
			if !child.Matches(observation) {
				return false
			}
		}
		return true
	case PredicateAny:
		for _, child := range p.Children {
			if child.Matches(observation) {
				return true
			}
		}
		return false
	default:
		return false
	}
}
