package runtime

import (
	"testing"
	"time"
)

func withGenerateRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()
	previous := generateRetryDelays
	generateRetryDelays = append([]time.Duration(nil), delays...)
	t.Cleanup(func() {
		generateRetryDelays = previous
	})
}

func withIdleStallRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()
	previous := idleStallRetryDelays
	idleStallRetryDelays = append([]time.Duration(nil), delays...)
	t.Cleanup(func() {
		idleStallRetryDelays = previous
	})
}

func withCompactionRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()
	previous := compactionRetryDelays
	compactionRetryDelays = append([]time.Duration(nil), delays...)
	t.Cleanup(func() {
		compactionRetryDelays = previous
	})
}
