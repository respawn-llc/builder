package transcript

import "strings"

const NoopFinalToken = "NO_OP"

func IsNoopFinalText(text string) bool {
	return strings.TrimSpace(text) == NoopFinalToken
}
