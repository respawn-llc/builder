package runtime

import (
	"time"
)

var generateRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
var idleStallRetryDelays = []time.Duration{1 * time.Second}
var compactionRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
