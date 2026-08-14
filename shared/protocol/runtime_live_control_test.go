package protocol

import "testing"

func TestRuntimeLiveControlProtocolConstants(t *testing.T) {
	if MethodRuntimeLiveSteer != "runtime.liveSteer" {
		t.Fatalf("MethodRuntimeLiveSteer = %q", MethodRuntimeLiveSteer)
	}
	if MethodRuntimeLiveStop != "runtime.liveStop" {
		t.Fatalf("MethodRuntimeLiveStop = %q", MethodRuntimeLiveStop)
	}
	if MethodRuntimeLiveWait != "runtime.liveWait" {
		t.Fatalf("MethodRuntimeLiveWait = %q", MethodRuntimeLiveWait)
	}
	if ErrCodeRuntimeNoActiveRun == 0 || ErrCodeRuntimeNoFinalAnswer == 0 || ErrCodeRuntimeNoActiveRun == ErrCodeRuntimeNoFinalAnswer {
		t.Fatalf("runtime live-control error codes are invalid: %d %d", ErrCodeRuntimeNoActiveRun, ErrCodeRuntimeNoFinalAnswer)
	}
}
