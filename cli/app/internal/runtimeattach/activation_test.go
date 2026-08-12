package runtimeattach

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type fakeRuntimeService struct {
	failActivateCall int
	activateErr      error
	releaseErr       error
	activateRequests []serverapi.SessionRuntimeActivateRequest
	releaseRequests  []serverapi.SessionRuntimeReleaseRequest
}

func (s *fakeRuntimeService) ActivateSessionRuntime(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	s.activateRequests = append(s.activateRequests, req)
	if len(s.activateRequests) == s.failActivateCall {
		return serverapi.SessionRuntimeActivateResponse{}, s.activateErr
	}
	return serverapi.SessionRuntimeActivateResponse{
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  req.SessionID,
			Generation: uint64(len(s.activateRequests)),
		},
	}, nil
}

func (s *fakeRuntimeService) ReleaseSessionRuntime(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	s.releaseRequests = append(s.releaseRequests, req)
	return serverapi.SessionRuntimeReleaseResponse{}, s.releaseErr
}

func TestSessionRuntimeAttachmentValidation(t *testing.T) {
	for _, attachment := range []serverapi.SessionRuntimeAttachment{
		{Generation: 1},
		{SessionID: "session-1"},
	} {
		if err := attachment.Validate(); err == nil {
			t.Fatalf("Validate(%+v) succeeded, want error", attachment)
		}
	}
}

func TestActivateBuildsRequest(t *testing.T) {
	service := &fakeRuntimeService{}
	_, err := Activate(context.Background(), service, Request{
		SessionID:                "session-1",
		EnabledTools:             []toolspec.ID{"shell", "patch"},
		ActiveSettings:           config.Settings{Model: "gpt-test"},
		ThinkingOverrideExplicit: true,
		Source:                   config.SourceReport{SettingsPath: "/config.toml"},
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(service.activateRequests) != 1 {
		t.Fatalf("activate requests = %d, want 1", len(service.activateRequests))
	}
	req := service.activateRequests[0]
	if req.ClientRequestID == "" || req.SessionID != "session-1" {
		t.Fatalf("request ids = %+v, want non-empty client id and session id", req)
	}
	if !reflect.DeepEqual(req.EnabledToolIDs, []string{"shell", "patch"}) {
		t.Fatalf("enabled tools = %#v, want shell/patch", req.EnabledToolIDs)
	}
	if req.ActiveSettings.Model != "gpt-test" || req.Source.SettingsPath != "/config.toml" {
		t.Fatalf("request config = %+v source = %+v", req.ActiveSettings, req.Source)
	}
	if !req.ThinkingOverrideExplicit {
		t.Fatal("explicit Thinking override was not forwarded")
	}
}

func TestActivateReactivatesRuntimeWithFreshRequestID(t *testing.T) {
	service := &fakeRuntimeService{}
	lease, err := Activate(context.Background(), service, Request{
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := lease.Reactivate(context.Background()); err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	firstID := service.activateRequests[0].ClientRequestID
	secondID := service.activateRequests[1].ClientRequestID
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("request ids = %q,%q, want two distinct non-empty fresh ids", firstID, secondID)
	}
	ownerID := service.activateRequests[0].OwnerID
	if ownerID == "" {
		t.Fatal("activate owner id is empty")
	}
	if service.activateRequests[1].OwnerID != ownerID {
		t.Fatalf("owner id not stable across reactivation: activate=%q reactivate=%q", ownerID, service.activateRequests[1].OwnerID)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := service.releaseRequests[0].Attachment; got.SessionID != "session-1" || got.Generation != 2 {
		t.Fatalf("released attachment = %+v, want reactivated generation 2", got)
	}
}

func TestFailedReactivationPreservesAttachment(t *testing.T) {
	service := &fakeRuntimeService{
		failActivateCall: 2,
		activateErr:      errors.New("reactivation failed"),
		releaseErr:       errors.New("release failed"),
	}
	lease, err := Activate(context.Background(), service, Request{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := lease.Reactivate(context.Background()); !errors.Is(err, service.activateErr) {
		t.Fatalf("Reactivate error = %v, want reactivation failed", err)
	}
	if err := lease.Release(); !errors.Is(err, service.releaseErr) {
		t.Fatalf("Release error = %v, want release failed", err)
	}
	if len(service.releaseRequests) != 1 {
		t.Fatalf("release requests = %d, want 1", len(service.releaseRequests))
	}
	req := service.releaseRequests[0]
	if req.Attachment.SessionID != "session-1" || req.Attachment.Generation != 1 || req.ClientRequestID == "" || !req.DropOwner || req.OwnerID == "" {
		t.Fatalf("release request = %+v, want exact attachment/request/owner ids", req)
	}
}

func TestReleaseWithDetachOnlyUsesOwnerDropClosePolicy(t *testing.T) {
	service := &fakeRuntimeService{}
	lease, err := Activate(context.Background(), service, Request{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := lease.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyDetachOnly); err != nil {
		t.Fatalf("ReleaseWithClosePolicy: %v", err)
	}
	if len(service.releaseRequests) != 1 {
		t.Fatalf("release requests = %d, want 1", len(service.releaseRequests))
	}
	req := service.releaseRequests[0]
	if !req.DropOwner || req.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyDetachOnly || req.OwnerID == "" {
		t.Fatalf("release request = %+v, want detach-only owner drop", req)
	}
}
