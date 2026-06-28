package sessionenv

import (
	"strings"

	brand "core/shared/config"
)

const SessionIDEnv = brand.SessionIDEnv
const RunIDEnv = brand.EnvPrefix + "RUN_ID"
const StepIDEnv = brand.EnvPrefix + "STEP_ID"

func LookupSessionID(lookup func(string) (string, bool)) (string, bool) {
	return lookupTrimmed(lookup, SessionIDEnv)
}

func LookupRunStepID(lookup func(string) (string, bool)) (runID string, stepID string) {
	runID, _ = lookupTrimmed(lookup, RunIDEnv)
	stepID, _ = lookupTrimmed(lookup, StepIDEnv)
	return runID, stepID
}

func lookupTrimmed(lookup func(string) (string, bool), key string) (string, bool) {
	if lookup == nil {
		return "", false
	}
	value, ok := lookup(key)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}
