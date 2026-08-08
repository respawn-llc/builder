package runtimecontrol

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestLiveSteerMemoEqualityIncludesCallerProvenance(t *testing.T) {
	target := runtimeids.NewSessionID()
	first := runtimeids.NewSessionID()
	second := runtimeids.NewSessionID()
	base := liveSteerMemoRequest{
		SessionID: target,
		Text:      "text",
	}
	if !sameLiveSteerMemoRequest(base, base) {
		t.Fatal("identical human memo requests are not equal")
	}
	withCaller := base
	withCaller.CallerSessionID = serverapi.OptionalStringKey{Present: true, Value: first.String()}
	if sameLiveSteerMemoRequest(base, withCaller) {
		t.Fatal("human and Session-issued memo requests are equal")
	}
	otherCaller := withCaller
	otherCaller.CallerSessionID.Value = second.String()
	if sameLiveSteerMemoRequest(withCaller, otherCaller) {
		t.Fatal("different caller Sessions are equal")
	}
}
