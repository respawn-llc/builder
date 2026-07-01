package invariant

import "strings"

func modeFromEnvironment(getenv func(string) string) Mode {
	switch strings.ToLower(strings.TrimSpace(getenv("KENT_INVARIANT_MODE"))) {
	case string(ModePanic):
		return ModePanic
	case string(ModeDiagnostic):
		return ModeDiagnostic
	}
	if debugEnabled(getenv("KENT_DEBUG")) {
		return ModePanic
	}
	return ModeDiagnostic
}

func debugEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
