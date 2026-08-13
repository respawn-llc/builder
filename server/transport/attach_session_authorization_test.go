package transport

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/session"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type countingSessionAttachmentMetadata struct {
	target         clientui.SessionExecutionTarget
	targetErr      error
	binding        metadata.Binding
	bindingErr     error
	targetLookups  int
	bindingLookups int
}

func (m *countingSessionAttachmentMetadata) ResolveSessionExecutionTarget(context.Context, string) (clientui.SessionExecutionTarget, error) {
	m.targetLookups++
	return m.target, m.targetErr
}

func (m *countingSessionAttachmentMetadata) LookupWorkspaceBindingByID(context.Context, string) (metadata.Binding, error) {
	m.bindingLookups++
	return m.binding, m.bindingErr
}

func TestResolveAuthorizedSessionAttachmentUsesOneTargetAndBindingLookup(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	source := &countingSessionAttachmentMetadata{
		target: clientui.SessionExecutionTarget{WorkspaceID: "workspace-1"},
		binding: metadata.Binding{
			ProjectID:     "project-1",
			WorkspaceID:   "workspace-1",
			CanonicalRoot: "/canonical/workspace",
		},
	}

	got, err := resolveAuthorizedSessionAttachment(context.Background(), source, "project-1", sessionID.String())
	if err != nil {
		t.Fatalf("resolveAuthorizedSessionAttachment: %v", err)
	}
	if source.targetLookups != 1 || source.bindingLookups != 1 {
		t.Fatalf("metadata lookups = target %d binding %d, want 1 each", source.targetLookups, source.bindingLookups)
	}
	if got.SessionID != sessionID ||
		got.ProjectID != source.binding.ProjectID ||
		got.WorkspaceID != source.binding.WorkspaceID ||
		got.CanonicalRoot != source.binding.CanonicalRoot {
		t.Fatalf("authorization = %+v", got)
	}
}

func TestResolveAuthorizedSessionAttachmentFailureLookupCountsAndErrors(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	targetErr := errors.New("missing target")
	bindingErr := serverapi.ErrWorkspaceNotRegistered

	tests := []struct {
		name               string
		source             *countingSessionAttachmentMetadata
		activeProjectID    string
		wantErr            error
		wantTargetLookups  int
		wantBindingLookups int
	}{
		{
			name:              "missing target",
			source:            &countingSessionAttachmentMetadata{targetErr: targetErr},
			activeProjectID:   "project-1",
			wantErr:           targetErr,
			wantTargetLookups: 1,
		},
		{
			name: "missing binding",
			source: &countingSessionAttachmentMetadata{
				target:     clientui.SessionExecutionTarget{WorkspaceID: "workspace-1"},
				bindingErr: bindingErr,
			},
			activeProjectID:    "project-1",
			wantErr:            bindingErr,
			wantTargetLookups:  1,
			wantBindingLookups: 1,
		},
		{
			name: "cross project",
			source: &countingSessionAttachmentMetadata{
				target: clientui.SessionExecutionTarget{WorkspaceID: "workspace-1"},
				binding: metadata.Binding{
					ProjectID:     "project-2",
					WorkspaceID:   "workspace-1",
					CanonicalRoot: "/canonical/workspace",
				},
			},
			activeProjectID:    "project-1",
			wantErr:            errSessionOutsideActiveProject,
			wantTargetLookups:  1,
			wantBindingLookups: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveAuthorizedSessionAttachment(context.Background(), test.source, test.activeProjectID, sessionID.String())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if test.source.targetLookups != test.wantTargetLookups ||
				test.source.bindingLookups != test.wantBindingLookups {
				t.Fatalf("metadata lookups = target %d binding %d, want target %d binding %d",
					test.source.targetLookups, test.source.bindingLookups,
					test.wantTargetLookups, test.wantBindingLookups)
			}
		})
	}
}

func TestAttachSessionValidationRejectsBeforeMetadataResolution(t *testing.T) {
	source := &countingSessionAttachmentMetadata{}
	_, err := apicontract.WithValidated(
		protocol.AttachSessionRequest{SessionID: " invalid "},
		apicontract.SemanticValidationRequired,
		func(validated apicontract.Validated[protocol.AttachSessionRequest]) (apicontract.AuthorizedSessionAttachment, error) {
			return resolveAuthorizedSessionAttachment(context.Background(), source, "project-1", validated.Value().SessionID)
		},
	)
	if err == nil {
		t.Fatal("invalid Attach Session request unexpectedly passed semantic validation")
	}
	if source.targetLookups != 0 || source.bindingLookups != 0 {
		t.Fatalf("invalid request reached metadata: target %d binding %d", source.targetLookups, source.bindingLookups)
	}
}

func TestAttachSessionAuthorizationFactPopulatesConnectionAndResponse(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolved := resolveGatewayTestConfig(t, workspace)
	binding := registerGatewayTestBinding(t, resolved.Config)
	store, err := metadata.Open(resolved.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	sessionStore, err := session.Create(
		filepath.Join(resolved.Config.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
		"attach-session",
		resolved.Config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	appCore, _ := newGatewayTestServerForConfig(t, resolved.Config)
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ServerID: "server", ProtocolVersion: protocol.Version})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true, attachedProject: binding.ProjectID}
	request := protocol.AttachSessionRequest{SessionID: sessionStore.Meta().SessionID}
	response := gateway.dispatch(context.Background(), state, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "attach-session",
		Method:  protocol.MethodAttachSession,
		Params:  mustJSON(t, request),
	})
	if response.Error != nil {
		t.Fatalf("Attach Session error: %+v", response.Error)
	}

	if state.attachedSession == nil || state.attachedSession.String() != request.SessionID {
		t.Fatalf("attached Session = %v, want %q", state.attachedSession, request.SessionID)
	}
	if state.attachedProject != binding.ProjectID ||
		state.attachedWorkspaceID != binding.WorkspaceID ||
		state.attachedWorkspaceRoot != binding.CanonicalRoot {
		t.Fatalf("attached state = project %q workspace %q root %q, want project %q workspace %q root %q",
			state.attachedProject, state.attachedWorkspaceID, state.attachedWorkspaceRoot,
			binding.ProjectID, binding.WorkspaceID, binding.CanonicalRoot)
	}

	var attached protocol.AttachResponse
	if err := json.Unmarshal(response.Result, &attached); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	got, ok := attached.Session()
	if !ok {
		t.Fatal("response does not contain a Session attachment")
	}
	if got.SessionID != request.SessionID ||
		got.ProjectID != binding.ProjectID ||
		got.WorkspaceID != binding.WorkspaceID ||
		got.WorkspaceRoot != binding.CanonicalRoot {
		t.Fatalf("attachment response = %+v", got)
	}
}

func TestAttachSessionFailuresDoNotMutateConnectionStateAndPreserveErrorMapping(t *testing.T) {
	targetErr := errors.New("missing target")
	tests := []struct {
		name     string
		source   *countingSessionAttachmentMetadata
		wantErr  error
		wantCode int
	}{
		{
			name:     "missing target",
			source:   &countingSessionAttachmentMetadata{targetErr: targetErr},
			wantErr:  targetErr,
			wantCode: protocol.ErrCodeInternalError,
		},
		{
			name: "missing binding",
			source: &countingSessionAttachmentMetadata{
				target:     clientui.SessionExecutionTarget{WorkspaceID: "workspace-2"},
				bindingErr: serverapi.ErrWorkspaceNotRegistered,
			},
			wantErr:  serverapi.ErrWorkspaceNotRegistered,
			wantCode: protocol.ErrCodeWorkspaceNotRegistered,
		},
		{
			name: "cross project",
			source: &countingSessionAttachmentMetadata{
				target: clientui.SessionExecutionTarget{WorkspaceID: "workspace-2"},
				binding: metadata.Binding{
					ProjectID:     "project-2",
					WorkspaceID:   "workspace-2",
					CanonicalRoot: "/workspace/after",
				},
			},
			wantErr:  errSessionOutsideActiveProject,
			wantCode: protocol.ErrCodeInternalError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &connectionState{
				handshakeDone:         true,
				attachedProject:       "project-1",
				attachedWorkspaceID:   "workspace-before",
				attachedWorkspaceRoot: "/workspace/before",
			}
			request := protocol.AttachSessionRequest{SessionID: runtimeids.NewSessionID().String()}
			_, err := apicontract.WithValidated(
				request,
				apicontract.SemanticValidationRequired,
				func(validated apicontract.Validated[protocol.AttachSessionRequest]) (protocol.AttachResponse, error) {
					attachment, err := resolveAuthorizedSessionAttachment(
						context.Background(),
						test.source,
						state.attachedProject,
						validated.Value().SessionID,
					)
					if err != nil {
						return protocol.AttachResponse{}, err
					}
					return handleAttachSession(context.Background(), nil, state, validated, attachment)
				},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if state.attachedProject != "project-1" ||
				state.attachedWorkspaceID != "workspace-before" ||
				state.attachedWorkspaceRoot != "/workspace/before" ||
				state.attachedSession != nil {
				t.Fatalf("connection state mutated on authorization failure: %+v", state)
			}
			code, message := protocolError(err)
			if code != test.wantCode || message != err.Error() {
				t.Fatalf("mapped error = code %d message %q, want code %d message %q", code, message, test.wantCode, err.Error())
			}
		})
	}
}
