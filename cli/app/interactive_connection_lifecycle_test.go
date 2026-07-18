package app

import (
	"io"
	"testing"

	"core/shared/llmerrors"
)

func TestInteractiveConnectionOwnerHandoffPreservesDisconnectUntilReachabilityConfirmation(t *testing.T) {
	owner := newInteractiveConnectionOwner()
	owner.ObserveUnary(io.EOF)

	model := newProjectedTestUIModel(&runtimeControlFakeClient{}, WithUIConnectionState(owner))
	if !model.runtimeDisconnectStatusVisible() {
		t.Fatal("main UI did not project the earlier interactive disconnect")
	}

	model.observeRuntimeRequestResult(&llmerrors.APIStatusError{StatusCode: 503})
	if model.runtimeDisconnectStatusVisible() {
		t.Fatal("main UI did not clear disconnect after a reachability-confirming operation failure")
	}
}

func TestInteractiveConnectionMainTransientNoticeOverridesAndThenRevealsDisconnect(t *testing.T) {
	owner := newInteractiveConnectionOwner()
	owner.ObserveUnary(io.EOF)
	model := newProjectedTestUIModel(&runtimeControlFakeClient{}, WithUIConnectionState(owner))

	model.transientStatus = "surface-owned"
	if got := model.layout().renderStatusNotice(statusLineUnboundedWidth); got == "" {
		t.Fatal("surface-owned transient status was not selected while disconnected")
	}
	model.transientStatus = ""
	if got := model.layout().renderStatusNotice(statusLineUnboundedWidth); got == "" {
		t.Fatal("persisted disconnect was not revealed after transient status cleared")
	}
}
