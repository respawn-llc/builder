package transport

import (
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type strictSessionPageGatewayProjectView struct {
	apicontract.ProjectViewService
	calls int
}

func (s *strictSessionPageGatewayProjectView) ListSessionPage(context.Context, serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	s.calls++
	return serverapi.SessionPageResponse{}, nil
}

type strictSessionPageGatewayDependencies struct {
	GatewayDependencies
	projectView apicontract.ProjectViewService
}

func (d *strictSessionPageGatewayDependencies) ProjectViewClient() apicontract.ProjectViewService {
	return d.projectView
}

func TestGatewaySessionPageRejectsObsoleteRequestFieldsBeforeProjectView(t *testing.T) {
	for _, test := range []struct {
		name string
		body json.RawMessage
	}{
		{
			name: "old only",
			body: json.RawMessage(`{"project_id":"project-1","category":"main","page_size":50,"position":{"kind":"newest"}}`),
		},
		{
			name: "mixed token and offset",
			body: json.RawMessage(`{"project_id":"project-1","category":"main","offset":0,"limit":50,"position":{"kind":"older","token":"opaque"}}`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectView := &strictSessionPageGatewayProjectView{}
			gateway := &Gateway{deps: &strictSessionPageGatewayDependencies{projectView: projectView}}
			route, ok := apicontract.RouteByMethod(protocol.MethodSessionPage)
			if !ok {
				t.Fatal("session page route is missing")
			}
			_, response, failed := gateway.preflightRouteRequest(
				context.Background(),
				&connectionState{},
				route,
				protocol.Request{
					JSONRPC: protocol.JSONRPCVersion,
					ID:      "session-page",
					Method:  protocol.MethodSessionPage,
					Params:  test.body,
				},
			)
			if !failed {
				t.Fatal("route preflight accepted obsolete Session page fields")
			}
			if response.Error == nil || response.Error.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("response = %+v, want invalid params", response)
			}
			if projectView.calls != 0 {
				t.Fatalf("Project View calls = %d, want 0", projectView.calls)
			}
		})
	}
}

var _ GatewayDependencies = (*strictSessionPageGatewayDependencies)(nil)
var _ apicontract.ProjectViewService = (*strictSessionPageGatewayProjectView)(nil)
