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
	activateErr      error
	releaseErr       error
	activateRequests []serverapi.SessionRuntimeActivateRequest
	releaseRequests  []serverapi.SessionRuntimeReleaseRequest
}

func (s *fakeRuntimeService) ActivateSessionRuntime(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	s.activateRequests = append(s.activateRequests, req)
	if s.activateErr != nil {
		return serverapi.SessionRuntimeActivateResponse{}, s.activateErr
	}
	return serverapi.SessionRuntimeActivateResponse{}, nil
}

func (s *fakeRuntimeService) ReleaseSessionRuntime(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	s.releaseRequests = append(s.releaseRequests, req)
	return serverapi.SessionRuntimeReleaseResponse{}, s.releaseErr
}

func TestActivateBuildsRequest(t *testing.T) {
	service := &fakeRuntimeService{}
	_, err := Activate(context.Background(), service, Request{
		SessionID:          "session-1",
		EnabledTools:       []toolspec.ID{"shell", "patch"},
		ActiveSettings:     config.Settings{Model: "gpt-test"},
		Source:             config.SourceReport{SettingsPath: "/config.toml"},
		NewClientRequestID: fixedIDs("request-1"),
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(service.activateRequests) != 1 {
		t.Fatalf("activate requests = %d, want 1", len(service.activateRequests))
	}
	req := service.activateRequests[0]
	if req.ClientRequestID != "request-1" || req.SessionID != "session-1" {
		t.Fatalf("request ids = %+v, want request/session ids", req)
	}
	if !reflect.DeepEqual(req.EnabledToolIDs, []string{"shell", "patch"}) {
		t.Fatalf("enabled tools = %#v, want shell/patch", req.EnabledToolIDs)
	}
	if req.ActiveSettings.Model != "gpt-test" || req.Source.SettingsPath != "/config.toml" {
		t.Fatalf("request config = %+v source = %+v", req.ActiveSettings, req.Source)
	}
}

func TestActivateReactivatesRuntimeWithFreshRequestID(t *testing.T) {
	service := &fakeRuntimeService{}
	lease, err := Activate(context.Background(), service, Request{
		SessionID:          "session-1",
		NewClientRequestID: fixedIDs("request-1", "request-2"),
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := lease.Reactivate(context.Background()); err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	gotIDs := []string{service.activateRequests[0].ClientRequestID, service.activateRequests[1].ClientRequestID}
	if !reflect.DeepEqual(gotIDs, []string{"request-1", "request-2"}) {
		t.Fatalf("request ids = %#v, want fresh ids", gotIDs)
	}
	ownerID := service.activateRequests[0].OwnerID
	if ownerID == "" {
		t.Fatal("activate owner id is empty")
	}
	if service.activateRequests[1].OwnerID != ownerID || lease.OwnerID != ownerID {
		t.Fatalf("owner id not stable across reactivation: activate=%q reactivate=%q lease=%q", ownerID, service.activateRequests[1].OwnerID, lease.OwnerID)
	}
}

func TestReleaseSkipsNilServiceAndIssuesRequest(t *testing.T) {
	Release(nil, "session-1", "owner-1")
	service := &fakeRuntimeService{releaseErr: errors.New("release failed")}
	Release(service, "session-1", "owner-1")
	if len(service.releaseRequests) != 1 {
		t.Fatalf("release requests = %d, want 1", len(service.releaseRequests))
	}
	req := service.releaseRequests[0]
	if req.SessionID != "session-1" || req.ClientRequestID == "" || !req.OnlyIfIdle || !req.DropOwner || req.OwnerID != "owner-1" {
		t.Fatalf("release request = %+v, want session/request/owner ids", req)
	}
}

func fixedIDs(ids ...string) func() string {
	index := 0
	return func() string {
		if index >= len(ids) {
			return ids[len(ids)-1]
		}
		id := ids[index]
		index++
		return id
	}
}
