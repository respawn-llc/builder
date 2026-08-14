package architectureguard

import (
	"go/ast"
)

var forbiddenAPICapabilityDeclarations = map[string]map[string]struct{}{
	"shared/protocol/protocol.go": {
		"CapabilityFlags": {},
	},
	"shared/protocol/handshake.go": {
		"ClientCapabilities": {},
	},
	"shared/apicontract/rpc_routes.go": {
		"Dependency":                      {},
		"DependencyProtocol":              {},
		"DependencyProtocolAttach":        {},
		"DependencyServerStatus":          {},
		"DependencyAuthBootstrap":         {},
		"DependencyAuthStatus":            {},
		"DependencyCapabilityFacts":       {},
		"DependencyPromptCommandCatalog":  {},
		"DependencyOnboardingFinalize":    {},
		"DependencyProjectView":           {},
		"DependencySessionLaunch":         {},
		"DependencyChatSettings":          {},
		"DependencySessionView":           {},
		"DependencySessionLifecycle":      {},
		"DependencySessionRuntime":        {},
		"DependencyWorktree":              {},
		"DependencyRuntimeControl":        {},
		"DependencyProcessView":           {},
		"DependencyProcessControl":        {},
		"DependencyAskView":               {},
		"DependencyApprovalView":          {},
		"DependencyPromptControl":         {},
		"DependencyAttentionNotification": {},
		"DependencySessionTranscript":     {},
		"DependencyRunPrompt":             {},
		"DependencyStreamNotification":    {},
		"DependencyWorkflow":              {},
	},
	"server/transport/gateway.go": {
		"GatewayDependencyAvailability":      {},
		"RuntimeLiveControlRoutesExecutable": {},
	},
	"server/transport/gateway_stream_handlers.go": {
		"legacyTranscriptSubscription":  {},
		"legacyTranscriptSequenceError": {},
	},
	"server/startup/serve_server.go": {
		"RouteDependencyAvailable": {},
		"serverCapabilityFlags":    {},
	},
	"cli/app/internal/remoteattach/remoteattach.go": {
		"Supports":                   {},
		"SupportsRunPrompt":          {},
		"SupportsRuntimeLiveControl": {},
	},
	"cli/app/internal/serverattach/attach.go": {
		"ErrServerIncompatible":      {},
		"IncompatibleServerError":    {},
		"capabilityVerdict":          {},
		"incompatibilityReason":      {},
		"newIncompatibleServerError": {},
	},
	"cli/app/session_server_target.go": {
		"startupRemoteCompatibilityIssue":            {},
		"startupRemoteCompatibilityError":            {},
		"startupRemoteProtocolVersionMismatch":       {},
		"startupRemoteAuthBootstrapUnavailable":      {},
		"startupRemoteOnboardingFinalizeUnavailable": {},
	},
}

var forbiddenAPICapabilityFields = map[string]map[string]map[string]struct{}{
	"shared/protocol/protocol.go": {
		"ServerIdentity": {"Capabilities": {}},
	},
	"shared/protocol/handshake.go": {
		"HandshakeRequest": {"ClientCapabilities": {}},
	},
	"shared/apicontract/rpc_routes.go": {
		"Route": {"Dependency": {}},
	},
	"server/transport/gateway.go": {
		"connectionState": {"clientCapabilities": {}},
	},
	"cli/app/internal/remoteattach/remoteattach.go": {
		"HeadlessRequest":    {"Supports": {}},
		"InteractiveRequest": {"Supports": {}},
	},
}

var forbiddenAPICapabilitySelectors = map[string]map[string]struct{}{
	"server/transport/gateway.go": {
		"Dependency": {},
	},
	"server/transport/gateway_stream_handlers.go": {
		"Dependency": {},
	},
	"cli/app/internal/remoteattach/remoteattach.go": {
		"Capabilities": {},
	},
	"cli/app/internal/serverattach/attach.go": {
		"Capabilities": {},
	},
	"cli/app/run_prompt_target.go": {
		"Capabilities": {},
	},
	"cli/app/session_server_target.go": {
		"Capabilities": {},
	},
}

func CheckNoAPICapabilityNegotiation(root string) error {
	return checkProductionGo(root, goASTPolicy{
		errorHeading: "API capability negotiation is forbidden",
		inspect: func(source goSourceFile) []string {
			declarations := forbiddenAPICapabilityDeclarations[source.relativePath]
			fields := forbiddenAPICapabilityFields[source.relativePath]
			selectors := forbiddenAPICapabilitySelectors[source.relativePath]
			if len(declarations) == 0 && len(fields) == 0 && len(selectors) == 0 {
				return nil
			}

			var violations []string
			ast.Inspect(source.file, func(node ast.Node) bool {
				switch declaration := node.(type) {
				case *ast.TypeSpec:
					if _, forbidden := declarations[declaration.Name.Name]; forbidden {
						violations = append(violations, source.violation(declaration.Name, declaration.Name.Name))
					}
					forbiddenFields := fields[declaration.Name.Name]
					if structType, ok := declaration.Type.(*ast.StructType); ok && len(forbiddenFields) != 0 {
						for _, field := range structType.Fields.List {
							for _, name := range field.Names {
								if _, forbidden := forbiddenFields[name.Name]; forbidden {
									violations = append(violations, source.violation(name, declaration.Name.Name+"."+name.Name))
								}
							}
						}
					}
				case *ast.FuncDecl:
					if _, forbidden := declarations[declaration.Name.Name]; forbidden {
						violations = append(violations, source.violation(declaration.Name, declaration.Name.Name))
					}
				case *ast.ValueSpec:
					for _, name := range declaration.Names {
						if _, forbidden := declarations[name.Name]; forbidden {
							violations = append(violations, source.violation(name, name.Name))
						}
					}
				case *ast.SelectorExpr:
					if _, forbidden := selectors[declaration.Sel.Name]; forbidden {
						violations = append(violations, source.violation(declaration.Sel, declaration.Sel.Name+" selector"))
					}
				}
				return true
			})
			return violations
		},
	})
}
