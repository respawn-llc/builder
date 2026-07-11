package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/cli/app"
	"core/server/launch"
	"core/server/metadata"
	"core/server/registry"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionlaunch"
	serverstartup "core/server/startup"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/llmerrors"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"

	"github.com/google/uuid"
)

type stubServeServer struct {
	serveErr error
}

func (s *stubServeServer) Close() error { return nil }
func (s *stubServeServer) Serve(context.Context) error {
	return s.serveErr
}

func TestRootCommandPrintsVersion(t *testing.T) {
	original := config.Version
	config.Version = "1.2.3"
	t.Cleanup(func() {
		config.Version = original
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--version"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := stdout.String(); got != "1.2.3\n" {
		t.Fatalf("stdout = %q, want version output", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRootHelpShowsInteractiveContinueCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	got := stderr.String()
	for _, want := range []string{
		"kent --continue <session-id>",
		"reopens a previous session in the interactive TUI",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
	}
}

func TestRootCommandRejectsUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"prompt", "--help"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	_ = stderr
}

func TestRootCommandRejectsNonInteractiveMode(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand(nil, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "interactive mode requires a terminal on stdin and stdout") {
		t.Fatalf("stderr = %q, want non-interactive error", got)
	}
}

func TestRootCommandForceInteractiveBypassesTerminalCheck(t *testing.T) {
	original := runInteractiveApp
	t.Cleanup(func() {
		runInteractiveApp = original
	})
	called := false
	runInteractiveApp = func(ctx context.Context, opts app.Options) error {
		called = true
		return nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--force-interactive"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !called {
		t.Fatal("expected interactive app to run when --force-interactive is set")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRootCommandMapsSessionFlagsToInteractiveApp(t *testing.T) {
	original := runInteractiveApp
	t.Cleanup(func() {
		runInteractiveApp = original
	})
	var got app.Options
	runInteractiveApp = func(ctx context.Context, opts app.Options) error {
		got = opts
		return nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{
		"--force-interactive",
		"--session", "session-123",
	}
	if code := rootCommand(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got.WorkspaceRoot != "." || got.WorkspaceRootExplicit {
		t.Fatalf("unexpected workspace mapping: %+v", got)
	}
	if got.SessionID != "session-123" {
		t.Fatalf("unexpected interactive option mapping: %+v", got)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRootCommandMapsAgentFlagToInteractiveApp(t *testing.T) {
	original := runInteractiveApp
	t.Cleanup(func() {
		runInteractiveApp = original
	})
	var got app.Options
	runInteractiveApp = func(ctx context.Context, opts app.Options) error {
		got = opts
		return nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{
		"--force-interactive",
		"--agent", "reviewer",
		"--session", "session-123",
	}
	if code := rootCommand(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got.AgentRole != "reviewer" || got.SessionID != "session-123" {
		t.Fatalf("unexpected interactive option mapping: %+v", got)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRootCommandContinueAgentRejectsLockedRoleChange(t *testing.T) {
	original := runInteractiveApp
	t.Cleanup(func() {
		runInteractiveApp = original
	})
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	workspace := t.TempDir()
	t.Chdir(workspace)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	reviewerSettings := cfg.Settings
	reviewerSettings.Model = "gpt-5.6-sol"
	workerSettings := cfg.Settings
	workerSettings.Model = "gpt-5.4-mini"
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"reviewer": {Settings: reviewerSettings},
		"worker":   {Settings: workerSettings},
	}
	meta, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	binding, err := meta.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store, err := session.Create(containerDir, filepath.Base(filepath.Clean(cfg.WorkspaceRoot)), cfg.WorkspaceRoot, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("reviewer")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "gpt-5.6-sol", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	service := sessionlaunch.NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: meta.AuthoritativeSessionStoreOptions(),
	}, registry.NewSessionStoreRegistry())
	runInteractiveApp = func(ctx context.Context, opts app.Options) error {
		if opts.SessionID != store.Meta().SessionID || opts.AgentRole != "worker" {
			t.Fatalf("interactive options = %+v, want locked session and worker role", opts)
		}
		_, err := service.PlanSession(ctx, serverapi.SessionPlanRequest{
			ClientRequestID:   "root-command-regression",
			Mode:              serverapi.SessionLaunchModeInteractive,
			SelectedSessionID: opts.SessionID,
			Overrides:         serverapi.RunPromptOverrides{AgentRole: opts.AgentRole},
		})
		if !errors.Is(err, launch.ErrLockedAgentRoleChange) {
			t.Fatalf("PlanSession error = %v, want locked role change", err)
		}
		return err
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	args := []string{
		"--force-interactive",
		"--continue", store.Meta().SessionID,
		"--agent", "worker",
	}
	if code := rootCommand(args, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, launch.ErrLockedAgentRoleChange.Error()) {
		t.Fatalf("stderr = %q, want locked role error", got)
	}
}

func TestRootCommandRejectsInvalidAgentFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--force-interactive", "--agent", "none"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if got := stderr.String(); !strings.Contains(got, `invalid --agent value "none"`) {
		t.Fatalf("stderr = %q, want invalid agent error", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRootCommandIgnoresKentSessionEnvByDefault(t *testing.T) {
	original := runInteractiveApp
	t.Cleanup(func() {
		runInteractiveApp = original
	})
	var got app.Options
	runInteractiveApp = func(ctx context.Context, opts app.Options) error {
		got = opts
		return nil
	}
	t.Setenv(sessionenv.SessionIDEnv, "session-from-env")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--force-interactive"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got.SessionID != "" {
		t.Fatalf("session id = %q, want empty", got.SessionID)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRootCommandRejectsRemovedStartupConfigFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--force-interactive", "--model", "gpt-5"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -model") {
		t.Fatalf("stderr = %q, want undefined model flag rejection", stderr.String())
	}
}

func TestRootCommandInteractiveInterruptReturns130(t *testing.T) {
	original := runInteractiveApp
	t.Cleanup(func() {
		runInteractiveApp = original
	})
	runInteractiveApp = func(ctx context.Context, opts app.Options) error {
		return context.Canceled
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"--force-interactive"}, strings.NewReader(""), &stdout, &stderr); code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRootCommandServeUsesStandaloneServerPath(t *testing.T) {
	originalStart := startServeServer
	originalHandlers := newServeStartupHandlers
	t.Cleanup(func() {
		startServeServer = originalStart
		newServeStartupHandlers = originalHandlers
	})
	var called bool
	var got serverstartup.Request
	startServeServer = func(_ context.Context, req serverstartup.Request, _ serverstartup.AuthHandler, _ serverstartup.OnboardingHandler) (serveCommandServer, error) {
		called = true
		got = req
		return &stubServeServer{serveErr: context.Canceled}, nil
	}
	newServeStartupHandlers = func() (serverstartup.AuthHandler, serverstartup.OnboardingHandler) {
		return nil, nil
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"serve"}, strings.NewReader(""), &stdout, &stderr); code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
	if !called {
		t.Fatal("expected serve startup path to run")
	}
	if got.WorkspaceRoot != "" || got.WorkspaceRootExplicit {
		t.Fatalf("unexpected workspace mapping: %+v", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got.SessionID != "" {
		t.Fatalf("expected empty session id for serve request, got %q", got.SessionID)
	}
}

func TestServeSubcommandRejectsRemovedStartupConfigFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"serve", "--workspace", "/tmp/work"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -workspace") {
		t.Fatalf("stderr = %q, want undefined workspace flag rejection", stderr.String())
	}
}

func TestServeSubcommandRejectsSessionFlags(t *testing.T) {
	originalHandlers := newServeStartupHandlers
	t.Cleanup(func() {
		newServeStartupHandlers = originalHandlers
	})
	newServeStartupHandlers = func() (serverstartup.AuthHandler, serverstartup.OnboardingHandler) {
		return nil, nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := rootCommand([]string{"serve", "--session", "session-123"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -session") {
		t.Fatalf("stderr = %q, want undefined session flag rejection", stderr.String())
	}
}

func TestRunSubcommandMapsCommonFlagsToRunPrompt(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	var gotOpts app.Options
	var gotPrompt string
	var gotTimeout time.Duration
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotOpts = opts
		gotPrompt = prompt
		gotTimeout = timeout
		return app.RunPromptResult{Result: "done"}, nil
	}

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
	})

	args := []string{
		"run",
		"--workspace", "/tmp/run-workspace",
		"--session", "session-456",
		"--model", "gpt-5-mini",
		"--provider-override", "openai",
		"--thinking-level", "medium",
		"--theme", "light",
		"--model-timeout-seconds", "12",
		"--tools", "shell",
		"--openai-base-url", "http://run.example/v1",
		"--timeout", "2m",
		"hello from test",
	}
	if code := rootCommand(args, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if gotPrompt != "hello from test" || gotTimeout != 2*time.Minute {
		t.Fatalf("unexpected run prompt mapping prompt=%q timeout=%v", gotPrompt, gotTimeout)
	}
	if gotOpts.WorkspaceRoot != "/tmp/run-workspace" || !gotOpts.WorkspaceRootExplicit {
		t.Fatalf("unexpected workspace mapping: %+v", gotOpts)
	}
	if gotOpts.SessionID != "session-456" || gotOpts.Model != "gpt-5-mini" || gotOpts.ProviderOverride != "openai" || gotOpts.ThinkingLevel != "medium" || gotOpts.Theme != "light" {
		t.Fatalf("unexpected run option mapping: %+v", gotOpts)
	}
	if gotOpts.ModelTimeoutSeconds != 12 {
		t.Fatalf("unexpected timeout mapping: %+v", gotOpts)
	}
	if gotOpts.Tools != "shell" {
		t.Fatalf("tools = %q, want shell", gotOpts.Tools)
	}
	if gotOpts.OpenAIBaseURL != "http://run.example/v1" || !gotOpts.OpenAIBaseURLExplicit {
		t.Fatalf("unexpected base url mapping: %+v", gotOpts)
	}
}

func TestRunSubcommandStreamsFinalizedAssistantResponsesAndNoticesByDefault(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() { runPromptApp = original })

	sessionID := uuid.MustParse("018fdd67-89ab-4cde-8123-456789abcdef")
	commentary := "checking the runtime boundary"
	finalResponse := "implemented the fix"
	steeredText := "preserve the typed contract"
	runPromptApp = func(_ context.Context, _ app.Options, _ string, _ time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		if progress == nil {
			t.Fatal("default run progress sink is absent")
		}
		progress.PublishRunPromptProgress(serverapi.RunPromptProgress{
			Kind:           serverapi.RunPromptProgressKindSessionStarted,
			SessionStarted: &serverapi.RunPromptSessionStarted{SessionID: sessionID},
		})
		progress.PublishRunPromptProgress(serverapi.RunPromptProgress{
			Kind: serverapi.RunPromptProgressKindAssistantMessage,
			AssistantMessage: &serverapi.RunPromptVisibleResponse{
				Phase:   clientui.MessagePhaseCommentary,
				Content: commentary,
			},
		})
		progress.PublishRunPromptProgress(serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindCompactionStarted})
		progress.PublishRunPromptProgress(serverapi.RunPromptProgress{
			Kind:           serverapi.RunPromptProgressKindSteeredMessage,
			SteeredMessage: &serverapi.RunPromptSteeredMessage{Content: steeredText},
		})
		progress.PublishRunPromptProgress(serverapi.RunPromptProgress{
			Kind: serverapi.RunPromptProgressKindAssistantMessage,
			AssistantMessage: &serverapi.RunPromptVisibleResponse{
				Phase:   clientui.MessagePhaseFinal,
				Content: finalResponse,
			},
		})
		return app.RunPromptResult{SessionID: sessionID.String(), Result: finalResponse}, nil
	}

	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Count(stdout, commentary) != 1 || strings.Count(stdout, finalResponse) != 1 {
		t.Fatalf("stdout = %q, want each finalized assistant response once", stdout)
	}
	if strings.Count(stderr, sessionID.String()) != 1 || strings.Count(stderr, steeredText) != 1 {
		t.Fatalf("stderr = %q, want session and steering notices", stderr)
	}
}

func TestRunSubcommandQuietAliasPreservesFinalOnlyOutput(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() { runPromptApp = original })

	finalResponse := "quiet final response"
	runPromptApp = func(_ context.Context, _ app.Options, _ string, _ time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		if progress != nil {
			t.Fatal("quiet run should not subscribe to progress")
		}
		return app.RunPromptResult{Result: finalResponse}, nil
	}

	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "-q", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Count(stdout, finalResponse) != 1 || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want final-only output", stdout, stderr)
	}
}

func TestHelperProcessRootCommand(t *testing.T) {
	if os.Getenv("KENT_ROOT_HELPER_PROCESS") != "1" {
		return
	}
	if os.Getenv("KENT_ROOT_HELPER_STUB_SERVE") == "1" {
		startServeServer = func(_ context.Context, _ serverstartup.Request, _ serverstartup.AuthHandler, _ serverstartup.OnboardingHandler) (serveCommandServer, error) {
			return &stubServeServer{serveErr: context.Canceled}, nil
		}
		newServeStartupHandlers = func() (serverstartup.AuthHandler, serverstartup.OnboardingHandler) {
			return nil, nil
		}
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			os.Exit(rootCommand(args[i+1:], strings.NewReader(""), os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func runRootCommandWithCapturedProcessOutput(t *testing.T, args []string) (string, string, int) {
	t.Helper()
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
	})
	code := rootCommand(args, strings.NewReader(""), io.Discard, io.Discard)
	stdout := readCapturedFile(t, stdoutFile)
	stderr := readCapturedFile(t, stderrFile)
	return stdout, stderr, code
}

func readCapturedFile(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatalf("seek captured file: %v", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read captured file: %v", err)
	}
	return string(data)
}

func TestRunSubcommandMapsFastFlagToAgentRole(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	var gotOpts app.Options
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotOpts = opts
		return app.RunPromptResult{Result: "done"}, nil
	}

	originalStdout := os.Stdout
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	os.Stdout = stdoutFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = stdoutFile.Close()
	})

	if code := rootCommand([]string{"run", "--fast", "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if gotOpts.AgentRole != config.BuiltInSubagentRoleFast {
		t.Fatalf("agent role = %q, want fast", gotOpts.AgentRole)
	}
}

func TestRunSubcommandPreservesPromptStartingWithLiveWaitVerb(t *testing.T) {
	originalPrompt := runPromptApp
	originalWait := runLiveWaitApp
	t.Cleanup(func() {
		runPromptApp = originalPrompt
		runLiveWaitApp = originalWait
	})
	var gotPrompt string
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotPrompt = prompt
		return app.RunPromptResult{SessionID: "018fdd67-89ab-4cde-8123-456789abcdef", Result: "done"}, nil
	}
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		t.Fatal("live wait app should not be called for ordinary prompt")
		return app.RunPromptResult{}, nil
	}

	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "wait", "for", "CI", "to", "finish"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if gotPrompt != "wait for CI to finish" {
		t.Fatalf("prompt = %q, want joined prompt", gotPrompt)
	}
	if stdout == "" {
		t.Fatal("stdout is empty; want final run output")
	}
}

func TestRunSubcommandPreservesShortPromptStartingWithLiveStopVerb(t *testing.T) {
	originalPrompt := runPromptApp
	originalStop := runLiveStopApp
	t.Cleanup(func() {
		runPromptApp = originalPrompt
		runLiveStopApp = originalStop
	})
	var gotPrompt string
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotPrompt = prompt
		return app.RunPromptResult{SessionID: "018fdd67-89ab-4cde-8123-456789abcdef", Result: "done"}, nil
	}
	runLiveStopApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunLiveStopResult, error) {
		t.Fatal("live stop app should not be called for ordinary prompt")
		return app.RunLiveStopResult{}, nil
	}

	if _, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "stop", "now"}); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if gotPrompt != "stop now" {
		t.Fatalf("prompt = %q, want joined prompt", gotPrompt)
	}
}

func TestRunSubcommandPreservesPromptWhenLiveVerbArityDoesNotMatch(t *testing.T) {
	originalPrompt := runPromptApp
	originalWait := runLiveWaitApp
	originalSteer := runLiveSteerApp
	t.Cleanup(func() {
		runPromptApp = originalPrompt
		runLiveWaitApp = originalWait
		runLiveSteerApp = originalSteer
	})
	var prompts []string
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		prompts = append(prompts, prompt)
		return app.RunPromptResult{SessionID: "018fdd67-89ab-4cde-8123-456789abcdef", Result: "done"}, nil
	}
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		t.Fatal("live wait app should not be called when wait has extra prompt words")
		return app.RunPromptResult{}, nil
	}
	runLiveSteerApp = func(context.Context, app.Options, runtimeids.SessionID, string) (app.RunLiveSteerResult, error) {
		t.Fatal("live steer app should not be called without a message")
		return app.RunLiveSteerResult{}, nil
	}

	if _, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "wait", "018fdd67-89ab-4cde-8123-456789abcdef", "for", "CI"}); code != 0 {
		t.Fatalf("wait prompt exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if _, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "steer", "018fdd67-89ab-4cde-8123-456789abcdef"}); code != 0 {
		t.Fatalf("steer prompt exit code = %d, want 0; stderr=%q", code, stderr)
	}
	want := []string{"wait 018fdd67-89ab-4cde-8123-456789abcdef for CI", "steer 018fdd67-89ab-4cde-8123-456789abcdef"}
	if len(prompts) != len(want) {
		t.Fatalf("prompts = %+v, want %+v", prompts, want)
	}
	for i := range want {
		if prompts[i] != want[i] {
			t.Fatalf("prompts = %+v, want %+v", prompts, want)
		}
	}
}

func TestRunSteerSubcommandQueuesLiveMessage(t *testing.T) {
	original := runLiveSteerApp
	t.Cleanup(func() { runLiveSteerApp = original })
	var gotSession string
	var gotMessage string
	var gotRoot string
	runLiveSteerApp = func(ctx context.Context, opts app.Options, targetSessionID runtimeids.SessionID, text string) (app.RunLiveSteerResult, error) {
		gotSession = targetSessionID.String()
		gotMessage = text
		gotRoot = opts.ConfigRoot
		return app.RunLiveSteerResult{}, nil
	}
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "steer", "--persistence-root", "test-root", "018fdd67-89ab-4cde-8123-456789abcdef", "hello", "there"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if gotSession != "018fdd67-89ab-4cde-8123-456789abcdef" || gotMessage != "hello there" || gotRoot != "test-root" {
		t.Fatalf("unexpected live steer mapping session=%q message=%q root=%q", gotSession, gotMessage, gotRoot)
	}
	if stdout == "" || stderr != "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunSteerNoActiveRunPrintsContinueCommand(t *testing.T) {
	original := runLiveSteerApp
	t.Cleanup(func() { runLiveSteerApp = original })
	runLiveSteerApp = func(context.Context, app.Options, runtimeids.SessionID, string) (app.RunLiveSteerResult, error) {
		return app.RunLiveSteerResult{}, errors.Join(serverapi.ErrRuntimeNoActiveRun, errors.New("inactive"))
	}
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "steer", "--persistence-root", "root with space", "018fdd67-89ab-4cde-8123-456789abcdef", "say", "hi"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Fatal("stderr is empty; want no-active guidance")
	}
}

func TestRunStopSubcommandPrintsIdleAsSuccess(t *testing.T) {
	original := runLiveStopApp
	t.Cleanup(func() { runLiveStopApp = original })
	runLiveStopApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunLiveStopResult, error) {
		return app.RunLiveStopResult{Status: serverapi.RuntimeLiveStopStatusIdle}, nil
	}
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "stop", "018fdd67-89ab-4cde-8123-456789abcdef"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "No active run\n" || stderr != "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunWaitSubcommandEmitsRunJSONShape(t *testing.T) {
	original := runLiveWaitApp
	t.Cleanup(func() { runLiveWaitApp = original })
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		return app.RunPromptResult{
			SessionID:   "018fdd67-89ab-4cde-8123-456789abcdef",
			SessionName: "live session",
			Result:      "done",
			Duration:    2 * time.Second,
		}, nil
	}
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "wait", "--output-mode=json", "018fdd67-89ab-4cde-8123-456789abcdef"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var decoded runJSONResult
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode wait json: %v; raw=%q", err, stdout)
	}
	if decoded.Status != "ok" || decoded.Result != "done" || decoded.SessionID != "018fdd67-89ab-4cde-8123-456789abcdef" || decoded.DurationMS != 2000 {
		t.Fatalf("unexpected wait json: %+v", decoded)
	}
}

func TestRunWaitNoActiveRunJSONHasCleanTypedError(t *testing.T) {
	original := runLiveWaitApp
	t.Cleanup(func() { runLiveWaitApp = original })
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		return app.RunPromptResult{SessionID: "018fdd67-89ab-4cde-8123-456789abcdef"}, serverapi.ErrRuntimeNoActiveRun
	}
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "wait", "--output-mode=json", "018fdd67-89ab-4cde-8123-456789abcdef"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var decoded runJSONResult
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode wait error json: %v; raw=%q", err, stdout)
	}
	if decoded.Status != "error" || decoded.Error == nil || decoded.Error.Message != serverapi.ErrRuntimeNoActiveRun.Error() {
		t.Fatalf("unexpected wait error json: %+v", decoded)
	}
}

func TestRunWaitNoActiveRunFinalTextIncludesContinueHint(t *testing.T) {
	original := runLiveWaitApp
	t.Cleanup(func() { runLiveWaitApp = original })
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		return app.RunPromptResult{SessionID: "018fdd67-89ab-4cde-8123-456789abcdef"}, serverapi.ErrRuntimeNoActiveRun
	}
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "wait", "018fdd67-89ab-4cde-8123-456789abcdef"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "run --continue \"018fdd67-89ab-4cde-8123-456789abcdef\"") {
		t.Fatalf("stderr = %q, want continue hint", stderr)
	}
}

func TestRunLiveControlsRejectSelfTargetBeforeAppCall(t *testing.T) {
	original := runLiveSteerApp
	t.Cleanup(func() { runLiveSteerApp = original })
	called := false
	runLiveSteerApp = func(context.Context, app.Options, runtimeids.SessionID, string) (app.RunLiveSteerResult, error) {
		called = true
		return app.RunLiveSteerResult{}, nil
	}
	t.Setenv(sessionenv.SessionIDEnv, "018fdd67-89ab-4cde-8123-456789abcdef")
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "steer", "018fdd67-89ab-4cde-8123-456789abcdef", "nope"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if called {
		t.Fatal("live steer app was called for self-targeting command")
	}
	if stdout != "" || stderr == "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunSteerContinueCommandUsesShellSafeQuoting(t *testing.T) {
	got := buildRunSteerContinueCommand("018fdd67-89ab-4cde-8123-456789abcdef", "/tmp/root $HOME", "say 'hi' and $(stay literal)")
	want := "kent run --persistence-root '/tmp/root $HOME' --continue 018fdd67-89ab-4cde-8123-456789abcdef 'say '\\''hi'\\'' and $(stay literal)'"
	if got != want {
		t.Fatalf("continue command = %q, want %q", got, want)
	}
}

func TestRunSubcommandJSONModeKeepsWarningsInJSONOnly(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		if progress != nil {
			t.Fatal("JSON run should not subscribe to progress")
		}
		return app.RunPromptResult{Result: "done", Warnings: []string{"warning one"}}, nil
	}

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
	})

	if code := rootCommand([]string{"run", "--output-mode=json", "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if _, err := stdoutFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stdout: %v", err)
	}
	data, err := io.ReadAll(stdoutFile)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if strings.Contains(string(data), "warning one\n\n") {
		t.Fatalf("expected stdout to stay json-only, got %q", string(data))
	}
	var decoded runJSONResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode json output: %v; raw=%q", err, string(data))
	}
	if len(decoded.Warnings) != 1 || decoded.Warnings[0] != "warning one" {
		t.Fatalf("unexpected warnings: %+v", decoded.Warnings)
	}
	if _, err := stderrFile.Seek(0, 0); err != nil {
		t.Fatalf("seek stderr: %v", err)
	}
	stderrData, err := io.ReadAll(stderrFile)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if strings.TrimSpace(string(stderrData)) != "" {
		t.Fatalf("stderr = %q, want empty", string(stderrData))
	}
}

func TestRequireInteractiveTerminalAllowsForce(t *testing.T) {
	if err := requireInteractiveTerminal(strings.NewReader(""), &bytes.Buffer{}, true); err != nil {
		t.Fatalf("require interactive terminal with force: %v", err)
	}
}

func TestParseRunTimeoutDefaultsToInfinite(t *testing.T) {
	got, err := parseRunTimeout("")
	if err != nil {
		t.Fatalf("parse run timeout: %v", err)
	}
	if got != 0 {
		t.Fatalf("timeout = %v, want 0", got)
	}
}

func TestParseRunTimeoutRejectsInvalid(t *testing.T) {
	if _, err := parseRunTimeout("not-a-duration"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRunTimeoutParsesDuration(t *testing.T) {
	got, err := parseRunTimeout("2m")
	if err != nil {
		t.Fatalf("parse run timeout: %v", err)
	}
	if got != 2*time.Minute {
		t.Fatalf("timeout = %v, want %v", got, 2*time.Minute)
	}
}

func TestRunErrorCode(t *testing.T) {
	if got := runErrorCode(context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("run error code = %q, want timeout", got)
	}
	if got := runErrorCode(context.Canceled); got != "interrupted" {
		t.Fatalf("run error code = %q, want interrupted", got)
	}
	if got := runErrorCode(errors.New("boom")); got != "runtime" {
		t.Fatalf("run error code = %q, want runtime", got)
	}
}

func TestRunErrorMessageRoutesStallThroughUserFacingError(t *testing.T) {
	stall := fmt.Errorf("model generation failed after retries: %w", llmerrors.ErrModelStreamStalled)
	want := llmerrors.UserFacingError(stall)
	if want == "" {
		t.Fatal("expected a stall to have a user-facing message")
	}
	if got := runErrorMessage(stall); got != want {
		t.Fatalf("run error message = %q, want user-facing message %q", got, want)
	}
}

func TestRunErrorMessageFallsBackToRawErrorWhenUnmapped(t *testing.T) {
	raw := errors.New("boom")
	if got := runErrorMessage(raw); got != raw.Error() {
		t.Fatalf("run error message = %q, want raw error %q", got, raw.Error())
	}
}

func TestParseRunOutputMode(t *testing.T) {
	got, err := parseRunOutputMode("final-text")
	if err != nil {
		t.Fatalf("parse output mode: %v", err)
	}
	if got != runOutputModeFinalText {
		t.Fatalf("output mode = %q, want %q", got, runOutputModeFinalText)
	}
	got, err = parseRunOutputMode("json")
	if err != nil {
		t.Fatalf("parse output mode: %v", err)
	}
	if got != runOutputModeJSON {
		t.Fatalf("output mode = %q, want %q", got, runOutputModeJSON)
	}
	if _, err := parseRunOutputMode("verbose"); err == nil {
		t.Fatal("expected invalid output mode error")
	}
}

func TestParseRunProgressMode(t *testing.T) {
	got, err := parseRunProgressMode("quiet")
	if err != nil {
		t.Fatalf("parse progress mode: %v", err)
	}
	if got != runProgressModeQuiet {
		t.Fatalf("progress mode = %q, want %q", got, runProgressModeQuiet)
	}
	got, err = parseRunProgressMode("stderr")
	if err != nil {
		t.Fatalf("parse progress mode: %v", err)
	}
	if got != runProgressModeStderr {
		t.Fatalf("progress mode = %q, want %q", got, runProgressModeStderr)
	}
	if _, err := parseRunProgressMode("chatty"); err == nil {
		t.Fatal("expected invalid progress mode error")
	}
}

func TestEffectiveSessionIDPrefersContinueAlias(t *testing.T) {
	got, err := effectiveSessionID(commonFlags{SessionID: "abc", ContinueID: "abc"})
	if err != nil {
		t.Fatalf("effective session id: %v", err)
	}
	if got != "abc" {
		t.Fatalf("session id = %q, want abc", got)
	}

	got, err = effectiveSessionID(commonFlags{ContinueID: "xyz"})
	if err != nil {
		t.Fatalf("effective session id: %v", err)
	}
	if got != "xyz" {
		t.Fatalf("session id = %q, want xyz", got)
	}

	if _, err := effectiveSessionID(commonFlags{SessionID: "abc", ContinueID: "xyz"}); err == nil {
		t.Fatal("expected conflicting --session/--continue error")
	}
}

func TestRunSubcommandUsesKentSessionEnvAsWorkspaceContext(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	var gotOpts app.Options
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotOpts = opts
		return app.RunPromptResult{Result: "done"}, nil
	}
	t.Setenv(sessionenv.SessionIDEnv, "session-from-env")

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
	})

	if code := rootCommand([]string{"run", "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if gotOpts.SessionID != "" {
		t.Fatalf("session id = %q, want empty", gotOpts.SessionID)
	}
	if gotOpts.WorkspaceContextSessionID != "session-from-env" {
		t.Fatalf("workspace context session id = %q, want env session", gotOpts.WorkspaceContextSessionID)
	}
}

func TestRunSubcommandDefaultAgentWithFastUsesFastRole(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	var gotOpts app.Options
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotOpts = opts
		return app.RunPromptResult{Result: "done"}, nil
	}

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
	})

	if code := rootCommand([]string{"run", "--agent=default", "--fast", "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if gotOpts.AgentRole != "" {
		t.Fatalf("run prompt app should not be called, got agent role %q", gotOpts.AgentRole)
	}
}

func TestRunSubcommandContinueDefaultAgentSendsDefaultRoleOverride(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	var gotOpts app.Options
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotOpts = opts
		return app.RunPromptResult{Result: "done"}, nil
	}

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("create stdout temp file: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create stderr temp file: %v", err)
	}
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
	})

	if code := rootCommand([]string{"run", "--continue", "session-123", "--agent", "default", "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if gotOpts.SessionID != "session-123" {
		t.Fatalf("session id = %q, want session-123", gotOpts.SessionID)
	}
	if gotOpts.AgentRole != config.DefaultSubagentRole {
		t.Fatalf("agent role = %q, want default", gotOpts.AgentRole)
	}
}

func TestRunSubcommandRejectsRemovedDefaultAgentAliases(t *testing.T) {
	for _, alias := range []string{"none", "self"} {
		t.Run(alias, func(t *testing.T) {
			original := runPromptApp
			t.Cleanup(func() {
				runPromptApp = original
			})
			called := false
			runPromptApp = func(context.Context, app.Options, string, time.Duration, serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
				called = true
				return app.RunPromptResult{}, nil
			}

			if code := rootCommand([]string{"run", "--agent", alias, "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if called {
				t.Fatal("run prompt app should not be called for invalid role")
			}
		})
	}
}

func TestRunSubcommandRejectsExplicitBlankAgentRole(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	called := false
	runPromptApp = func(context.Context, app.Options, string, time.Duration, serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		called = true
		return app.RunPromptResult{}, nil
	}

	if code := rootCommand([]string{"run", "--continue", "session-123", "--agent=", "hello"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if called {
		t.Fatal("run prompt app should not be called for blank role")
	}
}

func TestSessionIDSubcommandPrintsKentSessionEnv(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, " session-from-env ")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := rootCommand([]string{"session-id"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.String() != "session-from-env\n" {
		t.Fatalf("stdout = %q, want session id", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestSessionIDSubcommandFailsOutsideKentShell(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := rootCommand([]string{"session-id"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), sessionenv.SessionIDEnv+" is not set") {
		t.Fatalf("stderr = %q, want missing env error", stderr.String())
	}
}

func TestRegisterCommonFlagsDoesNotExposeRemovedBashTimeoutAlias(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	registerCommonFlags(fs, true)
	if fs.Lookup("bash-timeout-seconds") != nil {
		t.Fatal("expected removed --bash-timeout-seconds flag to be absent")
	}
}

func TestEffectiveRunAgentRoleRejectsConflictingFastFlag(t *testing.T) {
	if _, err := effectiveRunAgentRole("worker", true); err == nil {
		t.Fatal("expected conflicting fast role error")
	}
	if _, err := effectiveRunAgentRole("default", true); err == nil {
		t.Fatal("expected default role to conflict with fast flag")
	}
	role, err := effectiveRunAgentRole("fast", true)
	if err != nil {
		t.Fatalf("effectiveRunAgentRole: %v", err)
	}
	if role != config.BuiltInSubagentRoleFast {
		t.Fatalf("role = %q, want fast", role)
	}
}

func TestEffectiveRunAgentRoleDefaultAndRemovedAliases(t *testing.T) {
	role, err := effectiveRunAgentRole("default", false)
	if err != nil {
		t.Fatalf("effectiveRunAgentRole: %v", err)
	}
	if role != config.DefaultSubagentRole {
		t.Fatalf("role = %q, want default", role)
	}
	for _, alias := range []string{"none", "self"} {
		t.Run(alias, func(t *testing.T) {
			if _, err := effectiveRunAgentRole(alias, false); err == nil {
				t.Fatal("expected removed alias to be rejected")
			}
		})
	}
}

func TestMarkExplicitCommonFlagsTracksOnlyParsedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	flags := registerCommonFlags(fs, true)
	if err := fs.Parse([]string{"--workspace", "/tmp/w", "--openai-base-url=http://local/v1", "prompt"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	markExplicitCommonFlags(fs, flags)
	if !flags.WorkspaceExplicit {
		t.Fatal("expected workspace override to be marked explicit")
	}
	if !flags.OpenAIBaseURLExplicit {
		t.Fatal("expected openai base url override to be marked explicit")
	}
}

func TestMarkExplicitCommonFlagsIgnoresFlagTextInsidePrompt(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	flags := registerCommonFlags(fs, true)
	prompt := "please keep --workspace unchanged and ignore --openai-base-url"
	if err := fs.Parse([]string{"--continue", "session-123", prompt}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	markExplicitCommonFlags(fs, flags)
	if flags.WorkspaceExplicit {
		t.Fatal("did not expect prompt text to mark workspace explicit")
	}
	if flags.OpenAIBaseURLExplicit {
		t.Fatal("did not expect prompt text to mark openai base url explicit")
	}
}

func TestPublishPersistenceRootEnvNormalizesInheritedRelativeEnv(t *testing.T) {
	t.Setenv(config.PersistenceRootEnvName, "rel-root")
	if err := publishPersistenceRootEnv(""); err != nil {
		t.Fatalf("publishPersistenceRootEnv: %v", err)
	}
	got := os.Getenv(config.PersistenceRootEnvName)
	if !filepath.IsAbs(got) {
		t.Fatalf("inherited relative env root = %q, want republished as absolute", got)
	}
}

func TestRootCommandNormalizesInheritedRelativeEnvBeforeDispatch(t *testing.T) {
	// Root-checking client subcommands (project/attach/rebind/goal/workflow/task)
	// dispatch before the interactive path's publishPersistenceRootEnv call, so
	// rootCommand must normalize an inherited relative env at entry or those
	// commands would hash a root resolved against the wrong directory and reject
	// the server. --version exercises the same entry-point normalization without
	// requiring a reachable server.
	t.Setenv(config.PersistenceRootEnvName, "rel-root")
	var stdout, stderr strings.Builder
	if code := rootCommand([]string{"--version"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("rootCommand --version exit = %d: %s", code, stderr.String())
	}
	if got := os.Getenv(config.PersistenceRootEnvName); !filepath.IsAbs(got) {
		t.Fatalf("inherited relative env root = %q, want normalized to absolute at dispatch entry", got)
	}
}

func TestRootCommandToleratesUnnormalizableInheritedEnvBeforeDispatch(t *testing.T) {
	// A bad inherited KENT_PERSISTENCE_ROOT (here a tilde that cannot expand
	// because HOME is unresolvable) must not abort dispatch at the entry-point
	// normalization: a command that owns a --persistence-root flag re-publishes
	// from the flag (which must win over the env), and a flag-less command surfaces
	// the error at its own resolution boundary. --version exercises the entry-point
	// path and returns before any flag re-publish, so a non-zero exit here would
	// mean the entry normalization wrongly hard-failed.
	t.Setenv("HOME", "")
	t.Setenv(config.PersistenceRootEnvName, "~/cannot-expand")
	var stdout, stderr strings.Builder
	if code := rootCommand([]string{"--version"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("rootCommand --version exit = %d: %s", code, stderr.String())
	}
}

func TestPublishPersistenceRootEnvFlagWinsOverEnv(t *testing.T) {
	t.Setenv(config.PersistenceRootEnvName, "rel-root")
	flagRoot := filepath.Join(string(filepath.Separator), "tmp", "flag-root")
	if err := publishPersistenceRootEnv(flagRoot); err != nil {
		t.Fatalf("publishPersistenceRootEnv: %v", err)
	}
	if got := os.Getenv(config.PersistenceRootEnvName); got != flagRoot {
		t.Fatalf("env root = %q, want flag root %q", got, flagRoot)
	}
}

func TestPublishPersistenceRootEnvLeavesUnsetEnvUntouched(t *testing.T) {
	t.Setenv(config.PersistenceRootEnvName, "")
	if err := publishPersistenceRootEnv(""); err != nil {
		t.Fatalf("publishPersistenceRootEnv: %v", err)
	}
	if got := os.Getenv(config.PersistenceRootEnvName); got != "" {
		t.Fatalf("env root = %q, want empty (untouched)", got)
	}
}
