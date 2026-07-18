package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type runtimeAttachmentLeaseServiceStub struct {
	activateErr      error
	releaseErr       error
	activateRequests []serverapi.SessionRuntimeActivateRequest
	releaseRequests  []serverapi.SessionRuntimeReleaseRequest
}

func (s *runtimeAttachmentLeaseServiceStub) ActivateSessionRuntime(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	s.activateRequests = append(s.activateRequests, req)
	if s.activateErr != nil {
		return serverapi.SessionRuntimeActivateResponse{}, s.activateErr
	}
	return serverapi.SessionRuntimeActivateResponse{}, nil
}

func (s *runtimeAttachmentLeaseServiceStub) ReleaseSessionRuntime(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	s.releaseRequests = append(s.releaseRequests, req)
	return serverapi.SessionRuntimeReleaseResponse{}, s.releaseErr
}

func TestRuntimeAttachmentLeaseOwnsActivationAndReleaseRequests(t *testing.T) {
	t.Parallel()

	service := &runtimeAttachmentLeaseServiceStub{}
	lease, err := activateRuntime(context.Background(), service, runtimeActivationRequest{
		SessionID:      "session-1",
		EnabledTools:   []toolspec.ID{"shell", "patch"},
		ActiveSettings: config.Settings{Model: "gpt-test"},
		Source:         config.SourceReport{SettingsPath: "/config.toml"},
	})
	if err != nil {
		t.Fatalf("activate runtime: %v", err)
	}
	if err := lease.Reactivate(context.Background()); err != nil {
		t.Fatalf("reactivate runtime: %v", err)
	}
	if len(service.activateRequests) != 2 {
		t.Fatalf("activation requests = %d, want 2", len(service.activateRequests))
	}
	first, second := service.activateRequests[0], service.activateRequests[1]
	if first.ClientRequestID == "" || first.ClientRequestID == second.ClientRequestID || first.OwnerID == "" || first.OwnerID != second.OwnerID || lease.OwnerID != first.OwnerID {
		t.Fatalf("activation ownership/request ids are invalid: first=%+v second=%+v lease=%+v", first, second, lease)
	}
	if !reflect.DeepEqual(first.EnabledToolIDs, []string{"shell", "patch"}) || first.ActiveSettings.Model != "gpt-test" || first.Source.SettingsPath != "/config.toml" {
		t.Fatalf("activation request = %+v, want request derived from launch plan", first)
	}
	if err := releaseRuntimeWithClosePolicy(service, "session-1", lease.OwnerID, serverapi.SessionRuntimeReleaseClosePolicyDetachOnly); err != nil {
		t.Fatalf("release runtime: %v", err)
	}
	if len(service.releaseRequests) != 1 {
		t.Fatalf("release requests = %d, want 1", len(service.releaseRequests))
	}
	release := service.releaseRequests[0]
	if release.ClientRequestID == "" || release.SessionID != "session-1" || release.OwnerID != lease.OwnerID || !release.OnlyIfIdle || !release.DropOwner || release.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyDetachOnly {
		t.Fatalf("release request = %+v, want detach-only owner drop", release)
	}
}

func TestRuntimeAttachmentLeasePreservesReleaseFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("release failed")
	if err := releaseRuntime(&runtimeAttachmentLeaseServiceStub{releaseErr: want}, "session-1", "owner-1"); !errors.Is(err, want) {
		t.Fatalf("release error = %v, want %v", err, want)
	}
}
