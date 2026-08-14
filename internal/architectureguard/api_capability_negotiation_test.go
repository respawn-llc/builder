package architectureguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckNoAPICapabilityNegotiationRejectsFormerClientMechanism(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
	}{
		{
			name:    "remote attach Supports declaration",
			path:    "cli/app/internal/remoteattach/remoteattach.go",
			content: "package remoteattach\ntype Supports func()\n",
		},
		{
			name:    "headless remote attach Supports field",
			path:    "cli/app/internal/remoteattach/remoteattach.go",
			content: "package remoteattach\ntype HeadlessRequest struct { Supports func() }\n",
		},
		{
			name:    "interactive remote attach Supports field",
			path:    "cli/app/internal/remoteattach/remoteattach.go",
			content: "package remoteattach\ntype InteractiveRequest struct { Supports func() }\n",
		},
		{
			name:    "run prompt support helper",
			path:    "cli/app/internal/remoteattach/remoteattach.go",
			content: "package remoteattach\nfunc SupportsRunPrompt() {}\n",
		},
		{
			name:    "runtime live support helper",
			path:    "cli/app/internal/remoteattach/remoteattach.go",
			content: "package remoteattach\nfunc SupportsRuntimeLiveControl() {}\n",
		},
		{
			name:    "server attach incompatible sentinel",
			path:    "cli/app/internal/serverattach/attach.go",
			content: "package serverattach\nvar ErrServerIncompatible error\n",
		},
		{
			name:    "server attach incompatible error",
			path:    "cli/app/internal/serverattach/attach.go",
			content: "package serverattach\ntype IncompatibleServerError struct{}\n",
		},
		{
			name:    "server attach capability verdict",
			path:    "cli/app/internal/serverattach/attach.go",
			content: "package serverattach\ntype capabilityVerdict struct{}\n",
		},
		{
			name:    "server attach incompatible error constructor",
			path:    "cli/app/internal/serverattach/attach.go",
			content: "package serverattach\nfunc newIncompatibleServerError() {}\n",
		},
		{
			name:    "server attach incompatibility reason",
			path:    "cli/app/internal/serverattach/attach.go",
			content: "package serverattach\nfunc incompatibilityReason() {}\n",
		},
		{
			name:    "startup compatibility issue",
			path:    "cli/app/session_server_target.go",
			content: "package app\ntype startupRemoteCompatibilityIssue uint8\n",
		},
		{
			name:    "startup compatibility error",
			path:    "cli/app/session_server_target.go",
			content: "package app\ntype startupRemoteCompatibilityError struct{}\n",
		},
		{
			name:    "remote attach identity capabilities selector",
			path:    "cli/app/internal/remoteattach/remoteattach.go",
			content: "package remoteattach\nfunc bad(identity any) { _ = identity.Capabilities }\n",
		},
		{
			name:    "server attach identity capabilities selector",
			path:    "cli/app/internal/serverattach/attach.go",
			content: "package serverattach\nfunc bad(identity any) { _ = identity.Capabilities }\n",
		},
		{
			name:    "run target identity capabilities selector",
			path:    "cli/app/run_prompt_target.go",
			content: "package app\nfunc bad(identity any) { _ = identity.Capabilities }\n",
		},
		{
			name:    "startup identity capabilities selector",
			path:    "cli/app/session_server_target.go",
			content: "package app\nfunc bad(identity any) { _ = identity.Capabilities }\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeAPICapabilityGuardFixture(t, root, test.path, test.content)
			if err := CheckNoAPICapabilityNegotiation(root); err == nil {
				t.Fatal("forbidden client capability mechanism was accepted")
			}
		})
	}
}

func TestCheckNoAPICapabilityNegotiationRejectsFormerServerMechanism(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
	}{
		{
			name:    "server capability declaration",
			path:    "shared/protocol/protocol.go",
			content: "package protocol\ntype CapabilityFlags struct{}\n",
		},
		{
			name:    "server identity capability field",
			path:    "shared/protocol/protocol.go",
			content: "package protocol\ntype ServerIdentity struct { Capabilities any }\n",
		},
		{
			name:    "client capability declaration",
			path:    "shared/protocol/handshake.go",
			content: "package protocol\ntype ClientCapabilities struct{}\n",
		},
		{
			name:    "handshake client capability field",
			path:    "shared/protocol/handshake.go",
			content: "package protocol\ntype HandshakeRequest struct { ClientCapabilities any }\n",
		},
		{
			name:    "route dependency declaration",
			path:    "shared/apicontract/rpc_routes.go",
			content: "package apicontract\ntype Dependency string\n",
		},
		{
			name:    "route dependency constant",
			path:    "shared/apicontract/rpc_routes.go",
			content: "package apicontract\nconst DependencyProtocol = \"protocol\"\n",
		},
		{
			name:    "route dependency field",
			path:    "shared/apicontract/rpc_routes.go",
			content: "package apicontract\ntype Route struct { Dependency string }\n",
		},
		{
			name:    "gateway dependency availability",
			path:    "server/transport/gateway.go",
			content: "package transport\ntype GatewayDependencyAvailability interface{}\n",
		},
		{
			name:    "gateway connection capability state",
			path:    "server/transport/gateway.go",
			content: "package transport\ntype connectionState struct { clientCapabilities any }\n",
		},
		{
			name:    "gateway route dependency selector",
			path:    "server/transport/gateway.go",
			content: "package transport\nfunc bad(route any) { _ = route.Dependency }\n",
		},
		{
			name:    "runtime handler completeness probe",
			path:    "server/transport/gateway.go",
			content: "package transport\nfunc RuntimeLiveControlRoutesExecutable() bool { return true }\n",
		},
		{
			name:    "startup dependency availability",
			path:    "server/startup/serve_server.go",
			content: "package startup\nfunc RouteDependencyAvailable() {}\n",
		},
		{
			name:    "startup capability projection",
			path:    "server/startup/serve_server.go",
			content: "package startup\nfunc serverCapabilityFlags() {}\n",
		},
		{
			name:    "legacy transcript adapter",
			path:    "server/transport/gateway_stream_handlers.go",
			content: "package transport\ntype legacyTranscriptSubscription struct{}\n",
		},
		{
			name:    "legacy transcript sequence error",
			path:    "server/transport/gateway_stream_handlers.go",
			content: "package transport\ntype legacyTranscriptSequenceError struct{}\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeAPICapabilityGuardFixture(t, root, test.path, test.content)
			if err := CheckNoAPICapabilityNegotiation(root); err == nil {
				t.Fatal("forbidden server capability mechanism was accepted")
			}
		})
	}
}

func TestCheckNoAPICapabilityNegotiationAllowsRetainedCapabilityConcepts(t *testing.T) {
	root := t.TempDir()
	writeAPICapabilityGuardFixture(
		t,
		root,
		"cli/app/terminal_capabilities.go",
		"package app\ntype terminalCapabilities struct { MarkdownLinks bool }\n",
	)
	writeAPICapabilityGuardFixture(
		t,
		root,
		"server/capabilityfacts/service.go",
		"package capabilityfacts\ntype CapabilityFacts struct { Models []string }\n",
	)
	writeAPICapabilityGuardFixture(
		t,
		root,
		"server/llm/provider.go",
		"package llm\ntype ProviderCapabilities struct { Models []string }\n",
	)
	writeAPICapabilityGuardFixture(
		t,
		root,
		"server/session/event_log_capability.go",
		"package session\ntype EventLogCapability interface{}\n",
	)
	writeAPICapabilityGuardFixture(
		t,
		root,
		"apps/desktop/packages/native-bridge/src/capabilities.ts",
		"export type NativeCapabilities = { canOpen: boolean };\n",
	)
	if err := CheckNoAPICapabilityNegotiation(root); err != nil {
		t.Fatalf("retained capability concepts were rejected: %v", err)
	}
}

func writeAPICapabilityGuardFixture(t *testing.T, root string, path string, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
