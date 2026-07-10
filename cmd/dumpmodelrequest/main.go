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
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/shared/config"
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
	bootstrap, err := launch.ResolveBootstrapPlan(persistenceRoot, launch.BootstrapRequest{SessionID: sessionID})
	if err != nil {
		return capturedRequest{}, fmt.Errorf("resolve launch bootstrap: %w", err)
	}
	cfg, err := loadSessionConfig(bootstrap, persistenceRoot)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("load config: %w", err)
	}
	resolved, err := launch.ResolvePromptFacingSnapshotConfig(cfg, store, false)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("resolve session launch settings: %w", err)
	}
	authState, err := auth.NewEnvAPIKeyOverrideStore(
		auth.NewFileStore(config.GlobalAuthConfigPath(cfg)),
		os.LookupEnv,
	).Load(ctx)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("load auth state: %w", err)
	}

	caps, err := resolveProviderCapabilities(authState, resolved.Settings, providerOverride)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("resolve provider capabilities: %w", err)
	}
	wiring, err := runtimewire.NewRuntimeWiring(
		store,
		resolved.Settings,
		resolved.ActiveToolIDs,
		cfg.WorkspaceRoot,
		nil,
		nil,
		runtimewire.RuntimeWiringOptions{
			Context:         ctx,
			Client:          inspectionCapabilityClient{capabilities: caps},
			Headless:        false,
			Sources:         resolved.Source.Sources,
			GlobalConfigDir: persistenceRoot,
		},
	)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("construct runtime wiring: %w", err)
	}
	defer func() {
		_ = wiring.Close()
		if wiring.Background != nil {
			_ = wiring.Background.Close()
		}
	}()

	req, err := runtime.PrepareInspectionRequest(ctx, wiring.Engine, allowTools)
	if err != nil {
		return capturedRequest{}, fmt.Errorf("prepare request: %w", err)
	}

	openAIReq := llm.RequestAsOpenAI(req)
	mode := llm.OpenAIAuthModeForAuthState(authState)
	storeFlag := resolved.Settings.Store
	modelVerbosity := string(resolved.Settings.ModelVerbosity)
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
		Provider:    string(caps.ProviderID),
		Model:       req.Model,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		WirePayload: prettyWire,
		WireRaw:     string(wireBytes),
		Request:     req,
	}, nil
}

func loadSessionConfig(bootstrap launch.BootstrapPlan, persistenceRoot string) (config.App, error) {
	options := config.LoadOptions{
		ConfigRoot:    persistenceRoot,
		OpenAIBaseURL: bootstrap.OpenAIBaseURL,
	}
	if strings.TrimSpace(bootstrap.WorkspaceRoot) == "" {
		return config.LoadGlobal(options)
	}
	return config.Load(bootstrap.WorkspaceRoot, options)
}

func resolveProviderCapabilities(authState auth.State, active config.Settings, providerOverride string) (llm.ProviderCapabilities, error) {
	settings := active
	if override := strings.TrimSpace(providerOverride); override != "" {
		settings.ProviderOverride = override
		settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{}
	}
	return llm.ResolveRuntimeProviderCapabilities(authState, settings)
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
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(body); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// inspectionCapabilityClient exposes the already-resolved production capability
// contract while rejecting generation. Request inspection never dispatches.
type inspectionCapabilityClient struct {
	capabilities llm.ProviderCapabilities
}

func (inspectionCapabilityClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("dumpmodelrequest: model generation is not supported (inspection-only)")
}

func (c inspectionCapabilityClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return c.capabilities, nil
}
