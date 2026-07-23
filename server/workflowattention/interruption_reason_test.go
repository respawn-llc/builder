package workflowattention

import "testing"

func TestShouldNotifyInterruptedRunExcludesNonErrorInterruptions(t *testing.T) {
	for _, reason := range []string{"", InterruptionReasonUserInterrupt, InterruptionReasonRuntimeCanceled} {
		if ShouldNotifyInterruptedRun(reason) {
			t.Fatalf("ShouldNotifyInterruptedRun(%q) = true, want false", reason)
		}
	}
	if !ShouldNotifyInterruptedRun("workflow_runtime_failed") {
		t.Fatal("ShouldNotifyInterruptedRun(workflow_runtime_failed) = false, want true")
	}
}
