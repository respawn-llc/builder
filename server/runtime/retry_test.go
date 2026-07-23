package runtime

import (
	"testing"
	"time"
)

func withIdleStallRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()
	previous := idleStallRetryDelays
	idleStallRetryDelays = append([]time.Duration(nil), delays...)
	t.Cleanup(func() {
		idleStallRetryDelays = previous
	})
}
