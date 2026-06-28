package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func newRemoteTestServer(t *testing.T, handle func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer func() { _ = ws.Close() }()
		handle(ws)
	}))
	t.Cleanup(server.Close)
	return server
}

func acceptRemoteHandshake(t *testing.T, ws *websocket.Conn) protocol.Request {
	t.Helper()
	var req protocol.Request
	if err := websocket.JSON.Receive(ws, &req); err != nil {
		t.Fatalf("receive handshake: %v", err)
	}
	if req.Method != protocol.MethodHandshake {
		t.Fatalf("handshake method = %q", req.Method)
	}
	if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
		t.Fatalf("send handshake response: %v", err)
	}
	return req
}

func TestRemoteRunPromptPublishesProgressNotifications(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("receive run prompt: %v", err)
		}
		if req.Method != protocol.MethodRunPrompt {
			t.Fatalf("run prompt method = %q", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodRunPromptProgress, Params: mustJSON(t, serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Running tool"})}); err != nil {
			t.Fatalf("send progress: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodRunPromptProgress, Params: mustJSON(t, serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindStatus, Message: "Tool finished"})}); err != nil {
			t.Fatalf("send progress: %v", err)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.RunPromptResponse{SessionID: "session-1", SessionName: "Session 1", Result: "done"})); err != nil {
			t.Fatalf("send response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	var updates []serverapi.RunPromptProgress
	resp, err := remote.RunPrompt(context.Background(), serverapi.RunPromptRequest{ClientRequestID: "req-1", Prompt: "hello"}, serverapi.RunPromptProgressFunc(func(progress serverapi.RunPromptProgress) {
		updates = append(updates, progress)
	}))
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if resp.SessionID != "session-1" || resp.Result != "done" {
		t.Fatalf("unexpected run prompt response: %+v", resp)
	}
	if len(updates) != 2 || updates[0].Message != "Running tool" || updates[1].Message != "Tool finished" {
		t.Fatalf("unexpected progress updates: %+v", updates)
	}
}

func TestRemoteSessionActivitySubscriptionNextHonorsCanceledContext(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "session", SessionID: "session-1"})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			return
		}
		<-time.After(2 * time.Second)
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := sub.Next(ctx)
		errCh <- err
	}()

	<-time.After(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Next error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Next to honor cancellation")
	}
}

func TestRemoteSessionActivitySubscriptionPreservesTranscriptEntries(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "session", SessionID: "session-1"})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			return
		}
		evt := protocol.SessionActivityEventParams{Event: clientui.Event{
			Kind: clientui.EventToolCallStarted,
			TranscriptEntries: []clientui.ChatEntry{{
				Role:       "tool_call",
				Text:       "pwd",
				ToolCallID: "call-1",
			}},
		}}
		_ = websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityEvent, Params: mustJSON(t, evt)})
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if evt.Kind != clientui.EventToolCallStarted {
		t.Fatalf("event kind = %q, want %q", evt.Kind, clientui.EventToolCallStarted)
	}
	if len(evt.TranscriptEntries) != 1 {
		t.Fatalf("transcript entries len = %d, want 1", len(evt.TranscriptEntries))
	}
	if evt.TranscriptEntries[0].Role != "tool_call" || evt.TranscriptEntries[0].Text != "pwd" {
		t.Fatalf("unexpected transcript entry: %+v", evt.TranscriptEntries[0])
	}
}

func TestRemoteDeleteWorktreeCarriesDeleteBranchFlagAndResponseFields(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive worktree delete: %v", err)
		}
		if req.Method != protocol.MethodWorktreeDelete {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodWorktreeDelete)
		}
		var params serverapi.WorktreeDeleteRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("unmarshal delete params: %v", err)
		}
		if !params.DeleteBranch {
			t.Fatalf("expected delete_branch=true in params, got %+v", params)
		}
		if params.WorktreeID != "wt-1" {
			t.Fatalf("unexpected delete params: %+v", params)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorktreeDeleteResponse{
			Target:               clientui.SessionExecutionTarget{EffectiveWorkdir: "/repo"},
			Worktree:             serverapi.WorktreeView{WorktreeID: "wt-1", DisplayName: "feature-a"},
			BranchDeleted:        true,
			BranchCleanupMessage: "Deleted branch feature-a",
		})); err != nil {
			t.Fatalf("send delete response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.DeleteWorktree(context.Background(), serverapi.WorktreeDeleteRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		WorktreeID:      "wt-1",
		DeleteBranch:    true,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if !resp.BranchDeleted || resp.BranchCleanupMessage != "Deleted branch feature-a" {
		t.Fatalf("unexpected delete response: %+v", resp)
	}
}

func TestRemoteResolveWorktreeCreateTargetCarriesMethodAndPayload(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Fatalf("receive worktree resolve: %v", err)
		}
		if req.Method != protocol.MethodWorktreeCreateTargetResolve {
			t.Fatalf("method = %q, want %q", req.Method, protocol.MethodWorktreeCreateTargetResolve)
		}
		var params serverapi.WorktreeCreateTargetResolveRequest
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("unmarshal resolve params: %v", err)
		}
		if params.SessionID != "session-1" || params.Target != "HEAD~1" {
			t.Fatalf("unexpected resolve params: %+v", params)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.WorktreeCreateTargetResolveResponse{
			Resolution: serverapi.WorktreeCreateTargetResolution{Input: "HEAD~1", Kind: serverapi.WorktreeCreateTargetResolutionKindDetachedRef, ResolvedRef: "abc123"},
		})); err != nil {
			t.Fatalf("send resolve response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ResolveWorktreeCreateTarget(context.Background(), serverapi.WorktreeCreateTargetResolveRequest{SessionID: "session-1", Target: "HEAD~1"})
	if err != nil {
		t.Fatalf("ResolveWorktreeCreateTarget: %v", err)
	}
	if resp.Resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindDetachedRef || resp.Resolution.ResolvedRef != "abc123" {
		t.Fatalf("unexpected resolve response: %+v", resp)
	}
}

func TestRemoteSessionActivitySubscriptionPreservesTranscriptCriticalOrderingWithAssistantDeltaProgress(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "session", SessionID: "session-1"})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			return
		}

		frames := []protocol.Request{
			{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityEvent, Params: mustJSON(t, protocol.SessionActivityEventParams{Event: clientui.Event{
				Kind:              clientui.EventUserMessageFlushed,
				TranscriptEntries: []clientui.ChatEntry{{Role: "user", Text: "run tools"}},
			}})},
			{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityEvent, Params: mustJSON(t, protocol.SessionActivityEventParams{Event: clientui.Event{
				Kind:           clientui.EventAssistantDelta,
				AssistantDelta: "inspecting",
			}})},
			{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityEvent, Params: mustJSON(t, protocol.SessionActivityEventParams{Event: clientui.Event{
				Kind:              clientui.EventToolCallStarted,
				TranscriptEntries: []clientui.ChatEntry{{Role: "tool_call", Text: "pwd", ToolCallID: "call-1"}},
			}})},
			{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityEvent, Params: mustJSON(t, protocol.SessionActivityEventParams{Event: clientui.Event{
				Kind:              clientui.EventToolCallCompleted,
				TranscriptEntries: []clientui.ChatEntry{{Role: "tool_result_ok", Text: "ok", ToolCallID: "call-1"}},
			}})},
			{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityEvent, Params: mustJSON(t, protocol.SessionActivityEventParams{Event: clientui.Event{
				Kind:              clientui.EventAssistantMessage,
				TranscriptEntries: []clientui.ChatEntry{{Role: "assistant", Text: "done", Phase: "final_answer"}},
			}})},
		}
		for _, frame := range frames {
			if err := websocket.JSON.Send(ws, frame); err != nil {
				return
			}
		}
		_ = websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodSessionActivityComplete, Params: mustJSON(t, protocol.StreamCompleteParams{})})
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeSessionActivity(context.Background(), serverapi.SessionActivitySubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionActivity: %v", err)
	}
	defer func() { _ = sub.Close() }()

	// Commentary transcript entries are not currently expressible on the remote
	// session-activity stream, so assistant_delta is the strongest live-progress
	// signal the migrated path can preserve alongside transcript-bearing events.
	sequence := make([]string, 0, 5)
	for len(sequence) < 5 {
		evt, err := sub.Next(context.Background())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch evt.Kind {
		case clientui.EventUserMessageFlushed:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "user" || evt.TranscriptEntries[0].Text != "run tools" {
				t.Fatalf("unexpected user event: %+v", evt)
			}
			sequence = append(sequence, "user")
		case clientui.EventAssistantDelta:
			if evt.AssistantDelta != "inspecting" {
				t.Fatalf("assistant delta = %q, want inspecting", evt.AssistantDelta)
			}
			sequence = append(sequence, "assistant_progress")
		case clientui.EventToolCallStarted:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "tool_call" || evt.TranscriptEntries[0].Text != "pwd" {
				t.Fatalf("unexpected tool call event: %+v", evt)
			}
			sequence = append(sequence, "tool_call")
		case clientui.EventToolCallCompleted:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "tool_result_ok" || evt.TranscriptEntries[0].ToolCallID != "call-1" {
				t.Fatalf("unexpected tool result event: %+v", evt)
			}
			sequence = append(sequence, "tool_result")
		case clientui.EventAssistantMessage:
			if len(evt.TranscriptEntries) != 1 || evt.TranscriptEntries[0].Role != "assistant" || evt.TranscriptEntries[0].Text != "done" || evt.TranscriptEntries[0].Phase != "final_answer" {
				t.Fatalf("unexpected assistant event: %+v", evt)
			}
			sequence = append(sequence, "final")
		}
	}
	want := []string{"user", "assistant_progress", "tool_call", "tool_result", "final"}
	for i := range want {
		if sequence[i] != want[i] {
			t.Fatalf("sequence[%d] = %q, want %q (full=%v)", i, sequence[i], want[i], sequence)
		}
	}
}

func TestRemoteProcessOutputSubscriptionAttachesProjectBeforeSubscribe(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("expected attach-project before process output subscribe, got %q", req.Method)
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-project: %v", err)
		}
		if attach.ProjectID != "project-1" {
			t.Fatalf("attach project id = %q, want project-1", attach.ProjectID)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: attach.ProjectID})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodProcessSubscribeOutput {
			t.Fatalf("expected process output subscribe after attach-project, got %q", req.Method)
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.SubscribeResponse{})); err != nil {
			return
		}
		_ = websocket.JSON.Send(ws, protocol.Request{JSONRPC: protocol.JSONRPCVersion, Method: protocol.MethodProcessOutputComplete, Params: mustJSON(t, protocol.StreamCompleteParams{})})
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	sub, err := remote.SubscribeProcessOutput(context.Background(), serverapi.ProcessOutputSubscribeRequest{ProcessID: "proc-1"})
	if err != nil {
		t.Fatalf("SubscribeProcessOutput: %v", err)
	}
	defer func() { _ = sub.Close() }()
}

func TestDialRemoteURLForProjectAttachesProjectAndReturnsRemote(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("expected attach-project during dial, got %q", req.Method)
		}
		var attach protocol.AttachProjectRequest
		if err := json.Unmarshal(req.Params, &attach); err != nil {
			t.Fatalf("decode attach-project: %v", err)
		}
		if attach.ProjectID != "project-1" {
			t.Fatalf("attach project id = %q, want project-1", attach.ProjectID)
		}
		_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: attach.ProjectID}))
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if got := remote.ProjectID(); got != "project-1" {
		t.Fatalf("ProjectID = %q, want project-1", got)
	}
	if got := remote.Identity().ServerID; got != "server-1" {
		t.Fatalf("server id = %q, want server-1", got)
	}
}

func TestDialRemoteURLForProjectValidatesAttachProject(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		var req protocol.Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})); err != nil {
			return
		}
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		if req.Method != protocol.MethodAttachProject {
			t.Fatalf("expected attach-project during dial, got %q", req.Method)
		}
		_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, "project not available"))
	})

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-missing")
	if err == nil {
		if remote != nil {
			_ = remote.Close()
		}
		t.Fatal("expected dial to fail when project attach is rejected")
	}
	if remote != nil {
		t.Fatalf("expected nil remote on attach failure, got %v", remote)
	}
}

func TestRemoteProjectViewCallsReuseInitialProjectAttach(t *testing.T) {
	var attachCount atomic.Int32
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		for {
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				t.Fatalf("receive project view request: %v", err)
			}
			switch req.Method {
			case protocol.MethodAttachProject:
				attachCount.Add(1)
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1", WorkspaceRoot: "/tmp/attached"}))
			case protocol.MethodProjectResolvePath:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectResolvePathResponse{CanonicalRoot: "/tmp/workspace-a"}))
			case protocol.MethodProjectPlanWorkspaceBinding:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectBindingPlanResponse{Kind: serverapi.ProjectBindingPlanKindBound, Binding: &serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"}}))
			case protocol.MethodProjectCreate:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectCreateResponse{Binding: serverapi.ProjectBinding{ProjectID: "project-1"}}))
			case protocol.MethodProjectAttachWorkspace:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectAttachWorkspaceResponse{Binding: serverapi.ProjectBinding{ProjectID: "project-1"}}))
			case protocol.MethodProjectRebindWorkspace:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectRebindWorkspaceResponse{Binding: serverapi.ProjectBinding{ProjectID: "project-1", WorkspaceID: "workspace-1"}}))
			case protocol.MethodProjectGetOverview:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectGetOverviewResponse{}))
			case protocol.MethodSessionListByProject:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.SessionListByProjectResponse{}))
			case protocol.MethodProjectList:
				_ = websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.ProjectListResponse{}))
			default:
				t.Fatalf("unexpected project view method %q", req.Method)
			}
		}
	})

	remote, err := DialRemoteURLForProjectWorkspace(context.Background(), "ws"+server.URL[len("http"):], "project-1", "/tmp/attached")
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: "/tmp/workspace-a"}); err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if _, err := remote.PlanWorkspaceBinding(context.Background(), serverapi.ProjectBindingPlanRequest{Path: "/tmp/workspace-a", Mode: serverapi.ProjectBindingPlanModeInteractive}); err != nil {
		t.Fatalf("PlanWorkspaceBinding: %v", err)
	}
	if _, err := remote.CreateProject(context.Background(), serverapi.ProjectCreateRequest{DisplayName: "demo", WorkspaceRoot: "/tmp/workspace-a"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := remote.AttachWorkspaceToProject(context.Background(), serverapi.ProjectAttachWorkspaceRequest{ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-b"}); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	if _, err := remote.RebindWorkspace(context.Background(), serverapi.ProjectRebindWorkspaceRequest{OldWorkspaceRoot: "/tmp/workspace-a", NewWorkspaceRoot: "/tmp/workspace-b"}); err != nil {
		t.Fatalf("RebindWorkspace: %v", err)
	}
	if _, err := remote.GetProjectOverview(context.Background(), serverapi.ProjectGetOverviewRequest{ProjectID: "project-1"}); err != nil {
		t.Fatalf("GetProjectOverview: %v", err)
	}
	if _, err := remote.ListSessionsByProject(context.Background(), serverapi.SessionListByProjectRequest{ProjectID: "project-1"}); err != nil {
		t.Fatalf("ListSessionsByProject: %v", err)
	}
	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if got := attachCount.Load(); got != 1 {
		t.Fatalf("attachCount = %d, want 1", got)
	}
}

func TestProtocolErrorMapsPromptTerminalCodes(t *testing.T) {
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodePromptNotFound, Message: "missing"}); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("expected prompt not found, got %v", err)
	}
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodePromptResolved, Message: "resolved"}); !errors.Is(err, serverapi.ErrPromptAlreadyResolved) {
		t.Fatalf("expected prompt already resolved, got %v", err)
	}
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodePromptUnsupported, Message: "unsupported"}); !errors.Is(err, serverapi.ErrPromptUnsupported) {
		t.Fatalf("expected prompt unsupported, got %v", err)
	}
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeMethodNotFound, Message: "missing method"}); !errors.Is(err, serverapi.ErrMethodNotFound) {
		t.Fatalf("expected rpc method not found, got %v", err)
	}
}

func TestProtocolErrorMapsWorkflowTaskNotFoundCode(t *testing.T) {
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorkflowTaskNotFound, Message: "missing task"}); !errors.Is(err, serverapi.ErrWorkflowTaskNotFound) {
		t.Fatalf("expected workflow task not found, got %v", err)
	}
}

func TestProtocolErrorMapsWorkflowTaskCompleteAmbiguousCode(t *testing.T) {
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorkflowTaskCompleteAmbiguous, Message: "ambiguous completion target"}); !errors.Is(err, serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous) {
		t.Fatalf("expected ambiguous workflow task completion target, got %v", err)
	}
}

func TestProtocolErrorMapsWorkflowTaskCompleteNotFoundCode(t *testing.T) {
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeWorkflowTaskCompleteNotFound, Message: "missing completion target"}); !errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		t.Fatalf("expected missing workflow task completion target, got %v", err)
	}
}

func TestProtocolErrorMapsAuthRequiredCode(t *testing.T) {
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeAuthRequired, Message: "auth required"}); !errors.Is(err, serverapi.ErrServerAuthRequired) {
		t.Fatalf("expected server auth required, got %v", err)
	}
}

func TestProtocolErrorMapsRuntimeUnavailableCode(t *testing.T) {
	if err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRuntimeUnavailable, Message: "runtime missing"}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("expected runtime unavailable, got %v", err)
	}
}

func TestProtocolErrorMapsRequestCanceledCodeToClearMessage(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRequestCanceled, Message: "context canceled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !errors.Is(err, errRequestCanceledByClient) {
		t.Fatalf("expected normalized request-canceled error, got %v", err)
	}
}

func TestProtocolErrorMapsEmptyRequestCanceledCodeToClearMessage(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeRequestCanceled})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !errors.Is(err, errRequestCanceledByClient) {
		t.Fatalf("expected normalized request-canceled error, got %v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
