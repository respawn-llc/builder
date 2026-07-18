package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"core/cli/app"
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

	stdout, stderr := runRootCommandOK(t, "--version")
	if got := stdout; got != "1.2.3\n" {
		t.Fatalf("stdout = %q, want version output", got)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestRootHelpSmoke(t *testing.T) {
	stdout, stderr := runRootCommandOK(t, "--help")
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("help output is empty")
	}
}

func TestRootCommandRejectsUnknownCommand(t *testing.T) {
	stdout, stderr, code := runRootCommand("prompt", "--help")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	_ = stderr
}

func TestRootCommandRejectsNonInteractiveMode(t *testing.T) {
	stdout, stderr, code := runRootCommand()
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if got := stderr; !strings.Contains(got, "interactive mode requires a terminal on stdin and stdout") {
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

	_, stderr := runRootCommandOK(t, "--force-interactive")
	if !called {
		t.Fatal("expected interactive app to run when --force-interactive is set")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
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

	args := []string{
		"--force-interactive",
		"--session", "session-123",
	}
	stdout, stderr := runRootCommandOK(t, args...)
	if got.WorkspaceRoot != "." || got.WorkspaceRootExplicit {
		t.Fatalf("unexpected workspace mapping: %+v", got)
	}
	if got.SessionID != "session-123" {
		t.Fatalf("unexpected interactive option mapping: %+v", got)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
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

	args := []string{
		"--force-interactive",
		"--agent", "reviewer",
		"--session", "session-123",
	}
	stdout, stderr := runRootCommandOK(t, args...)
	if got.AgentRole == nil || *got.AgentRole != "reviewer" || got.SessionID != "session-123" {
		t.Fatalf("unexpected interactive option mapping: %+v", got)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRootCommandRejectsInvalidAgentFlag(t *testing.T) {
	stdout, stderr, code := runRootCommand("--force-interactive", "--agent", "none")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if got := stderr; !strings.Contains(got, `invalid --agent value "none"`) {
		t.Fatalf("stderr = %q, want invalid agent error", got)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
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

	stdout, stderr := runRootCommandOK(t, "--force-interactive")
	if got.SessionID != "" {
		t.Fatalf("session id = %q, want empty", got.SessionID)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRootCommandRejectsRemovedStartupConfigFlags(t *testing.T) {
	_, stderr, code := runRootCommand("--force-interactive", "--model", "gpt-5")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "flag provided but not defined: -model") {
		t.Fatalf("stderr = %q, want undefined model flag rejection", stderr)
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

	_, stderr, code := runRootCommand("--force-interactive")
	if code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
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

	stdout, _, code := runRootCommand("serve")
	if code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
	if !called {
		t.Fatal("expected serve startup path to run")
	}
	if got.WorkspaceRoot != "" || got.WorkspaceRootExplicit {
		t.Fatalf("unexpected workspace mapping: %+v", got)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if got.SessionID != "" {
		t.Fatalf("expected empty session id for serve request, got %q", got.SessionID)
	}
}

func TestServeSubcommandRejectsRemovedStartupConfigFlags(t *testing.T) {
	_, stderr, code := runRootCommand("serve", "--workspace", "/tmp/work")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "flag provided but not defined: -workspace") {
		t.Fatalf("stderr = %q, want undefined workspace flag rejection", stderr)
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
	_, stderr, code := runRootCommand("serve", "--session", "session-123")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "flag provided but not defined: -session") {
		t.Fatalf("stderr = %q, want undefined session flag rejection", stderr)
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

	t.Setenv(config.PersistenceRootEnvName, "inherited-root")
	persistenceRoot := t.TempDir()
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
		"--persistence-root", persistenceRoot,
		"--timeout", "2m",
		"hello from test",
	}
	_, stderr, code := runRootCommandWithCapturedProcessOutput(t, args)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
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
	if gotOpts.ConfigRoot != persistenceRoot || os.Getenv(config.PersistenceRootEnvName) != persistenceRoot {
		t.Fatalf("persistence root mapping: opts=%q env=%q, want %q", gotOpts.ConfigRoot, os.Getenv(config.PersistenceRootEnvName), persistenceRoot)
	}
}

func TestRunSubcommandStreamsFinalizedAssistantResponsesAndNoticesByDefault(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() { runPromptApp = original })

	sessionID := uuid.MustParse("018fdd67-89ab-4cde-8123-456789abcdef")
	commentary := "checking the runtime boundary"
	finalResponse := "implemented the fix"
	steeredText := "preserve the typed contract"
	runPromptApp = func(_ context.Context, _ app.Options, _ string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		if progress == nil {
			t.Fatal("default run progress sink is absent")
		}
		if timeout != 0 {
			t.Fatalf("default timeout = %v, want infinite", timeout)
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
	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
	}()
	t.Cleanup(func() {
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

	_, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "--fast", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if gotOpts.AgentRole == nil || *gotOpts.AgentRole != config.BuiltInSubagentRoleFast {
		t.Fatalf("agent role = %v, want fast", gotOpts.AgentRole)
	}
}

func TestRunSubcommandPreservesPromptsThatDoNotMatchLiveControlArity(t *testing.T) {
	originalPrompt := runPromptApp
	originalSteer := runLiveSteerApp
	originalStop := runLiveStopApp
	originalWait := runLiveWaitApp
	t.Cleanup(func() {
		runPromptApp = originalPrompt
		runLiveSteerApp = originalSteer
		runLiveStopApp = originalStop
		runLiveWaitApp = originalWait
	})
	var prompts []string
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		prompts = append(prompts, prompt)
		return app.RunPromptResult{SessionID: "018fdd67-89ab-4cde-8123-456789abcdef", Result: "done"}, nil
	}
	runLiveSteerApp = func(context.Context, app.Options, runtimeids.SessionID, string) (app.RunLiveSteerResult, error) {
		t.Fatal("live steer app should not be called for ordinary prompt")
		return app.RunLiveSteerResult{}, nil
	}
	runLiveStopApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunLiveStopResult, error) {
		t.Fatal("live stop app should not be called for ordinary prompt")
		return app.RunLiveStopResult{}, nil
	}
	runLiveWaitApp = func(context.Context, app.Options, runtimeids.SessionID) (app.RunPromptResult, error) {
		t.Fatal("live wait app should not be called for ordinary prompt")
		return app.RunPromptResult{}, nil
	}

	cases := [][]string{
		{"run", "wait", "for", "CI", "to", "finish"},
		{"run", "stop", "now"},
		{"run", "wait", "018fdd67-89ab-4cde-8123-456789abcdef", "for", "CI"},
		{"run", "steer", "018fdd67-89ab-4cde-8123-456789abcdef"},
	}
	for _, args := range cases {
		if _, stderr, code := runRootCommandWithCapturedProcessOutput(t, args); code != 0 {
			t.Fatalf("args=%q exit code = %d, want 0; stderr=%q", args, code, stderr)
		}
	}
	want := []string{
		"wait for CI to finish",
		"stop now",
		"wait 018fdd67-89ab-4cde-8123-456789abcdef for CI",
		"steer 018fdd67-89ab-4cde-8123-456789abcdef",
	}
	if !slices.Equal(prompts, want) {
		t.Fatalf("prompts = %+v, want %+v", prompts, want)
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
	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "steer", "--persistence-root", "/tmp/root $HOME", "018fdd67-89ab-4cde-8123-456789abcdef", "say 'hi' and $(stay literal)"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	want := "kent run --persistence-root '/tmp/root $HOME' --continue 018fdd67-89ab-4cde-8123-456789abcdef 'say '\\''hi'\\'' and $(stay literal)'"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want shell-safe continuation %q", stderr, want)
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

	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "--output-mode=json", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "warning one\n\n") {
		t.Fatalf("expected stdout to stay json-only, got %q", stdout)
	}
	var decoded runJSONResult
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode json output: %v; raw=%q", err, stdout)
	}
	if len(decoded.Warnings) != 1 || decoded.Warnings[0] != "warning one" {
		t.Fatalf("unexpected warnings: %+v", decoded.Warnings)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestRunSubcommandRejectsInvalidPublicFlags(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() { runPromptApp = original })
	called := false
	runPromptApp = func(context.Context, app.Options, string, time.Duration, serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		called = true
		return app.RunPromptResult{}, nil
	}
	cases := [][]string{
		{"run", "--timeout", "not-a-duration", "hello"},
		{"run", "--output-mode", "verbose", "hello"},
		{"run", "--progress-mode", "chatty", "hello"},
		{"run", "--bash-timeout-seconds", "1", "hello"},
		{"run", "--session", "one", "--continue", "two", "hello"},
		{"run", "--agent", "default", "--fast", "hello"},
		{"run", "--agent", "none", "hello"},
		{"run", "--agent", "self", "hello"},
		{"run", "--continue", "session-123", "--agent=", "hello"},
	}
	for _, args := range cases {
		called = false
		_, stderr, code := runRootCommandWithCapturedProcessOutput(t, args)
		if code != 2 || called || stderr == "" {
			t.Fatalf("args=%q code=%d called=%v stderr=%q, want usage rejection", args, code, called, stderr)
		}
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

	args := []string{"run", "please keep --workspace unchanged and ignore --openai-base-url"}
	_, stderr, code := runRootCommandWithCapturedProcessOutput(t, args)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if gotOpts.SessionID != "" {
		t.Fatalf("session id = %q, want empty", gotOpts.SessionID)
	}
	if gotOpts.WorkspaceContextSessionID != "session-from-env" {
		t.Fatalf("workspace context session id = %q, want env session", gotOpts.WorkspaceContextSessionID)
	}
	if gotOpts.WorkspaceRoot != "." || gotOpts.WorkspaceRootExplicit || gotOpts.OpenAIBaseURL != "" || gotOpts.OpenAIBaseURLExplicit {
		t.Fatalf("flag-looking prompt text changed explicit overrides: %+v", gotOpts)
	}
}

func TestRunSubcommandKeepsKentSessionEnvAsCallerWhenSelectingSession(t *testing.T) {
	original := runPromptApp
	t.Cleanup(func() {
		runPromptApp = original
	})
	var gotOpts app.Options
	runPromptApp = func(ctx context.Context, opts app.Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (app.RunPromptResult, error) {
		gotOpts = opts
		return app.RunPromptResult{Result: "done"}, nil
	}
	t.Setenv(sessionenv.SessionIDEnv, "caller-from-env")

	stdout, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "--session", "selected-session", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if gotOpts.SessionID != "selected-session" {
		t.Fatalf("selected session = %q, want selected-session", gotOpts.SessionID)
	}
	if gotOpts.WorkspaceContextSessionID != "caller-from-env" {
		t.Fatalf("caller session = %q, want caller-from-env", gotOpts.WorkspaceContextSessionID)
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

	_, stderr, code := runRootCommandWithCapturedProcessOutput(t, []string{"run", "--continue", "session-123", "--agent", "default", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if gotOpts.SessionID != "session-123" {
		t.Fatalf("session id = %q, want session-123", gotOpts.SessionID)
	}
	if gotOpts.AgentRole == nil || *gotOpts.AgentRole != config.DefaultSubagentRole {
		t.Fatalf("agent role = %v, want default", gotOpts.AgentRole)
	}
}

func TestSessionIDSubcommandPrintsKentSessionEnv(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, " session-from-env ")
	stdout, stderr := runRootCommandOK(t, "session-id")
	if stdout != "session-from-env\n" {
		t.Fatalf("stdout = %q, want session id", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestSessionIDSubcommandFailsOutsideKentShell(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	stdout, stderr, code := runRootCommand("session-id")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, sessionenv.SessionIDEnv+" is not set") {
		t.Fatalf("stderr = %q, want missing env error", stderr)
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
	runRootCommandOK(t, "--version")
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
	runRootCommandOK(t, "--version")
}
