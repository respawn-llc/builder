package workflowattention

import "strings"

const (
	InterruptionReasonUserInterrupt   = "user_interrupt"
	InterruptionReasonRuntimeCanceled = "workflow_runtime_canceled"
)

func ShouldNotifyInterruptedRun(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", InterruptionReasonUserInterrupt, InterruptionReasonRuntimeCanceled:
		return false
	default:
		return true
	}
}
