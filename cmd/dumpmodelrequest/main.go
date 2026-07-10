// Command dumpmodelrequest captures the exact model-request payload for a Kent
// session without executing a model turn or performing any network I/O.
//
// Given a session ID (and an optional persistence root), it resolves the session
// via the SQLite metadata index, reconstructs the production request-assembly path
// (session store -> runtime.Engine -> llm.Request -> OpenAI transport payload),
// and writes the literal OpenAI-compatible wire payload plus the provider-agnostic
// llm.Request to a JSON file.
//
// Every byte of the wire payload is produced by the real production code path
// (HTTPTransport.buildPayload -> responses.ResponseNewParams.MarshalJSON, the same
// encoder the openai-go SDK uses to build the HTTP body). No proxy, mock,
// approximation, or live OpenAI request is involved.
//
// Usage:
//
//	dumpmodelrequest -session <id> [-persistence-root <path>] [-provider openai|openai-compatible|chatgpt-codex] [-output <path>] [-no-tools]
//
// The persistence root defaults to KENT_PERSISTENCE_ROOT, then ~/.kent. The output
// path defaults to ./kent-<sessionID>-<unix-seconds>.json.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dumpmodelrequest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sessionID := fs.String("session", "", "session ID (required)")
	persistenceRoot := fs.String("persistence-root", "", "persistence root directory (overrides KENT_PERSISTENCE_ROOT and the default ~/.kent)")
	providerOverride := fs.String("provider", "", "provider id override (openai | openai-compatible | chatgpt-codex); defaults to resolving from the session's locked provider contract")
	output := fs.String("output", "", "output JSON path; defaults to ./kent-<sessionID>-<unix>.json")
	noTools := fs.Bool("no-tools", false, "build the request without tool definitions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*sessionID) == "" {
		fmt.Fprintln(stderr, "dumpmodelrequest: -session is required")
		fs.Usage()
		return 2
	}

	root, err := resolvePersistenceRoot(*persistenceRoot)
	if err != nil {
		fmt.Fprintf(stderr, "dumpmodelrequest: resolve persistence root: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := captureSessionRequest(ctx, root, strings.TrimSpace(*sessionID), strings.TrimSpace(*providerOverride), !*noTools)
	if err != nil {
		fmt.Fprintf(stderr, "dumpmodelrequest: %v\n", err)
		return 1
	}

	outPath, err := writeOutput(*output, result)
	if err != nil {
		fmt.Fprintf(stderr, "dumpmodelrequest: write output: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, outPath)
	return 0
}

// resolvePersistenceRoot mirrors the production precedence: explicit flag,
// KENT_PERSISTENCE_ROOT env, then the default ~/.kent.
func resolvePersistenceRoot(flagValue string) (string, error) {
	if trimmed := strings.TrimSpace(flagValue); trimmed != "" {
		return config.NormalizePersistenceRoot(trimmed)
	}
	if env := strings.TrimSpace(os.Getenv(config.PersistenceRootEnvName)); env != "" {
		return config.NormalizePersistenceRoot(env)
	}
	return config.NormalizePersistenceRoot(config.DefaultPersistence)
}

type capturedRequest struct {
	SessionID   string          `json:"session_id"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	GeneratedAt string          `json:"generated_at"`
	WirePayload json.RawMessage `json:"wire_payload"`
	WireRaw     string          `json:"wire_payload_raw"`
	Request     llm.Request     `json:"request"`
}

// captureSessionRequest reproduces the production request-prep path for a session
// and returns the prepared provider-agnostic request plus the literal OpenAI wire
// payload bytes. No model turn runs and no HTTP is performed.
func captureSessionRequest(ctx context.Context, persistenceRoot, sessionID, providerOverride string, allowTools bool) (capturedRequest, error) {
	md, err := metadata.Open(persistenceRoot)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("open metadata: %w", err)
	}
	defer func() { _ = md.Close() }()

	// Fileless persistence permits the runtime's legacy contract backfills in
	// memory without changing the session directory or SQLite metadata.
	store, err := session.OpenByID(
		persistenceRoot,
		sessionID,
		session.WithPersistedSessionResolver(md),
		session.WithFilelessMetadataPersistence(),
	)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("open read-only session: %w", err)
	}
	meta := store.Meta()
	workspaceRoot := strings.TrimSpace(meta.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = meta.WorkspaceContainer
	}
	if workspaceRoot == "" {
		workspaceRoot, _ = os.Getwd()
	}

	// Load config against the session's workspace so provider settings (Store,
	// ModelVerbosity, OpenAIBaseURL, capabilities override) resolve exactly as the
	// live runtime resolves them.
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{ConfigRoot: persistenceRoot})
	if err != nil {
		return capturedRequest{}, fmt.Errorf("load config: %w", err)
	}
	active := launchEffectiveSettings(cfg, &meta)
	authState, err := auth.NewEnvAPIKeyOverrideStore(
		auth.NewFileStore(config.GlobalAuthConfigPath(cfg)),
		os.LookupEnv,
	).Load(ctx)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("load auth state: %w", err)
	}

	caps, providerID, err := resolveProviderCapabilities(&meta, active, providerOverride)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("resolve provider capabilities: %w", err)
	}

	registry := buildInspectionRegistry(&meta, active)

	engineCfg := buildEngineConfig(&meta, active, cfg, workspaceRoot, persistenceRoot, caps)
	eng, err := runtime.New(store, inspectionStubClient{}, registry, engineCfg)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("construct engine: %w", err)
	}

	req, err := runtime.PrepareInspectionRequest(ctx, eng, allowTools)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("prepare request: %w", err)
	}

	openAIReq := llm.RequestAsOpenAI(req)
	mode := llm.OpenAIAuthModeForAuthState(authState)
	storeFlag := active.Store
	modelVerbosity := string(active.ModelVerbosity)
	wireBytes, err := llm.MarshalOpenAIWirePayload(openAIReq, storeFlag, modelVerbosity, mode, caps)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("marshal wire payload: %w", err)
	}

	var prettyWire json.RawMessage
	if info, mErr := prettyPrint(wireBytes); mErr == nil {
		prettyWire = info
	} else {
		prettyWire = wireBytes
	}

	return capturedRequest{
		SessionID:   meta.SessionID,
		Provider:    providerID,
		Model:       req.Model,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		WirePayload: prettyWire,
		WireRaw:     string(wireBytes),
		Request:     req,
	}, nil
}

// launchEffectiveSettings mirrors the launch planner's effective-settings
// resolution: base settings reconciled with the session's locked contract so the
// returned settings reflect what the live runtime would actually use.
func launchEffectiveSettings(app config.App, meta *session.Meta) config.Settings {
	// Prefer the locked model when present (the runtime always runs the locked
	// model); otherwise fall back to the configured model.
	active := app.Settings
	if meta != nil && meta.Locked != nil && strings.TrimSpace(meta.Locked.Model) != "" {
		active.Model = meta.Locked.Model
	}
	return active
}

// resolveProviderCapabilities resolves the provider id and capability contract for
// the session. Precedence: explicit override flag > session's locked provider
// contract > settings provider override > config capabilities override.
func resolveProviderCapabilities(meta *session.Meta, active config.Settings, providerOverride string) (llm.ProviderCapabilities, string, error) {
	if pid := strings.TrimSpace(providerOverride); pid != "" {
		caps, ok := llm.LookupProviderCapabilityContract(pid)
		if !ok {
			return llm.ProviderCapabilities{}, "", fmt.Errorf("unknown provider id %q", pid)
		}
		return caps, pid, nil
	}
	if meta != nil && meta.Locked != nil {
		if caps, ok := llm.ProviderCapabilitiesFromLocked(meta.Locked); ok {
			return caps, caps.ProviderID, nil
		}
	}
	if pid := strings.TrimSpace(active.ProviderOverride); pid != "" {
		if caps, ok := llm.LookupProviderCapabilityContract(pid); ok {
			return caps, pid, nil
		}
	}
	if pid := strings.TrimSpace(string(active.ProviderCapabilities.ProviderID)); pid != "" {
		if caps, ok := llm.LookupProviderCapabilityContract(pid); ok {
			return caps, pid, nil
		}
	}
	caps, ok := llm.LookupProviderCapabilityContract("openai")
	if !ok {
		return llm.ProviderCapabilities{}, "", errors.New("no provider capabilities resolved and openai fallback unavailable")
	}
	return caps, "openai", nil
}

// buildInspectionRegistry registers the session's enabled tools with no-op
// handlers. Tool definitions (name, description, JSON schema) come from the
// centralized catalog, so the request exposes the exact same tool shapes the live
// runtime exposes. Handlers are never invoked because no model turn runs.
func buildInspectionRegistry(meta *session.Meta, active config.Settings) *tools.Registry {
	enabled := enabledToolIDs(meta, active)
	noop := noopHandler{}
	handlers := make([]tools.HandlerRegistration, 0, len(enabled))
	for _, id := range enabled {
		def, ok := tools.DefinitionFor(id)
		if !ok {
			continue
		}
		if !def.AvailableInLocalRuntime() {
			continue
		}
		handlers = append(handlers, tools.HandlerRegistration{ID: id, Handler: noop})
	}
	return tools.NewRegistry(handlers...)
}

func enabledToolIDs(meta *session.Meta, active config.Settings) []toolspec.ID {
	if meta != nil && meta.Locked != nil && len(meta.Locked.EnabledTools) > 0 {
		ids := make([]toolspec.ID, 0, len(meta.Locked.EnabledTools))
		for _, raw := range meta.Locked.EnabledTools {
			trimmed := strings.TrimSpace(raw)
			if trimmed != "" {
				ids = append(ids, toolspec.ID(trimmed))
			}
		}
		return ids
	}
	out := make([]toolspec.ID, 0, len(active.EnabledTools))
	for id, on := range active.EnabledTools {
		if on {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return tools.DefaultEnabledToolIDs()
	}
	return out
}

// buildEngineConfig constructs the runtime.Config for inspection, sourcing every
// field from the session's locked contract and effective settings so the prepared
// request matches what the live runtime would produce.
func buildEngineConfig(meta *session.Meta, active config.Settings, app config.App, workspaceRoot, persistenceRoot string, caps llm.ProviderCapabilities) runtime.Config {
	cfg := runtime.Config{
		ProviderCapabilitiesOverride: &caps,
		EnabledTools:                 enabledToolIDs(meta, active),
		FastModeEnabled:              active.PriorityRequestMode,
		HeadlessMode:                 true,
		GlobalConfigDir:              persistenceRoot,
		WebSearchMode:                strings.TrimSpace(active.WebSearch),
	}
	if meta != nil && meta.Locked != nil {
		cfg.Model = meta.Locked.Model
		cfg.Temperature = meta.Locked.Temperature
		cfg.MaxTokens = meta.Locked.MaxOutputToken
		cfg.ContextWindowTokens = meta.Locked.ContextWindow
		cfg.EffectiveContextWindowPercent = meta.Locked.ContextPercent
		cfg.ModelCapabilities = meta.Locked.ModelCapabilities
		cfg.SystemPromptFiles = app.Settings.SystemPromptFiles
	} else {
		cfg.Model = active.Model
		cfg.SystemPromptFiles = active.SystemPromptFiles
	}
	cfg.ThinkingLevel = active.ThinkingLevel
	cfg.Reviewer = runtime.ReviewerConfig{Frequency: "off"}
	return cfg
}

func prettyPrint(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

func writeOutput(flagPath string, result capturedRequest) (string, error) {
	path := strings.TrimSpace(flagPath)
	if path == "" {
		path = fmt.Sprintf("kent-%s-%d.json", result.SessionID, time.Now().Unix())
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// inspectionStubClient satisfies runtime's llm.Client requirement without ever
// performing I/O. It is never invoked: inspection only builds the request, never
// runs a turn, and provider capabilities are supplied via the engine config
// override so the engine never consults the client for them.
type inspectionStubClient struct{}

func (inspectionStubClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("dumpmodelrequest: model generation is not supported (inspection-only)")
}

// noopHandler is a placeholder tool handler used only so the tool registry's
// non-nil-handler invariant is satisfied. It is never invoked during inspection.
type noopHandler struct{}

func (noopHandler) Call(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{IsError: true, Output: []byte(`{"error":"inspection-only registry"}`)}, nil
}
