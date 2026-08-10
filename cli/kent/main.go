package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"core/cli/app"
	"core/prompts"
	"core/shared/client"
	"core/shared/config"
	"core/shared/imagefileio"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessionenv"
	"golang.org/x/term"
)

type commonFlags struct {
	WorkspaceRoot         string
	WorkspaceExplicit     bool
	SessionID             string
	ContinueID            string
	Model                 string
	ProviderOverride      string
	ThinkingLevel         string
	Theme                 string
	ModelTimeoutSeconds   int
	Tools                 string
	OpenAIBaseURL         string
	OpenAIBaseURLExplicit bool
	PersistenceRoot       string
}

type runProgressMode string

const (
	runProgressModeQuiet  runProgressMode = "quiet"
	runProgressModeStderr runProgressMode = "stderr"
)

var runInteractiveApp = app.Run
var runPromptApp = app.RunPrompt
var runLiveSteerApp = app.RunLiveSteer
var runLiveStopApp = app.RunLiveStop
var runLiveWaitApp = app.RunLiveWait

func main() {
	imagefileio.ExitIfWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	redirectServiceLogs()
	if exitCode := rootCommand(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); exitCode != 0 {
		os.Exit(exitCode)
	}
}

func rootCommand(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	// Best-effort: normalize an inherited relative KENT_PERSISTENCE_ROOT to an
	// absolute path before dispatch so root-checking client subcommands (project,
	// attach, rebind, goal, workflow, task) hash the same root the server stamped
	// rather than re-resolving the relative value against the current directory.
	// This is intentionally non-fatal: a command that owns a --persistence-root
	// flag re-publishes below, where the flag must win over a bad inherited env,
	// and a flag-less command that genuinely cannot resolve its root surfaces the
	// error at its own resolution boundary instead of aborting every command here.
	// The blank-flag call is idempotent and leaves a default root (no env) untouched.
	_ = publishPersistenceRootEnv("")
	if len(args) > 0 && args[0] == "run" {
		return runSubcommand(args[1:])
	}
	if len(args) > 0 && args[0] == "project" {
		return projectSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "attach" {
		return attachSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "detach" {
		return detachSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "rebind" {
		return rebindSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "serve" {
		return serveSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "service" {
		return serviceSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "session-id" {
		return sessionIDSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "goal" {
		return goalSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && (args[0] == "question" || args[0] == "questions") {
		return questionSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "worktree" {
		return worktreeSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "workflow" {
		return workflowSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && (args[0] == "task" || args[0] == "tasks") {
		return taskSubcommand(args[1:], stdout, stderr)
	}

	rootFS := newCommandFlagSet(config.Command, stderr, rootUsage)
	showVersion := rootFS.Bool("version", false, "print the Kent version")
	forceInteractive := rootFS.Bool("force-interactive", false, "start the TUI even when stdin or stdout is not a terminal")
	agentRoleRaw := rootFS.String("agent", "", "configured subagent role for a new interactive session")
	persistenceRoot := rootFS.String("persistence-root", "", persistenceRootFlagUsage)
	flags := registerSessionFlags(rootFS)
	if ok, exitCode := parseCommandFlags(rootFS, args); !ok {
		return exitCode
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, config.Version)
		return 0
	}
	if remaining := rootFS.Args(); len(remaining) > 0 {
		fmt.Fprintf(stderr, "unknown command or arguments: %s\n\n", strings.Join(remaining, " "))
		rootFS.Usage()
		return 2
	}
	if err := requireInteractiveTerminal(stdin, stdout, *forceInteractive); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	markExplicitCommonFlags(rootFS, flags)
	sessionID, err := effectiveSessionID(*flags)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if flagExplicit(rootFS, "agent") && strings.TrimSpace(*agentRoleRaw) == "" {
		fmt.Fprintf(stderr, "invalid --agent value %q\n", *agentRoleRaw)
		return 2
	}
	agentRole, err := effectiveRunAgentRole(*agentRoleRaw, false)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	opts := app.Options{
		WorkspaceRoot: ".",
		SessionID:     sessionID,
		AgentRole:     agentRole,
		ConfigRoot:    strings.TrimSpace(*persistenceRoot),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runInteractiveApp(ctx, opts); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func requireInteractiveTerminal(stdin io.Reader, stdout io.Writer, force bool) error {
	if force {
		return nil
	}
	if !isTerminalReader(stdin) || !isTerminalWriter(stdout) {
		return errors.New("interactive mode requires a terminal on stdin and stdout; use `" + config.Command + " run ...` for headless usage or pass --force-interactive to bypass this check")
	}
	return nil
}

func isTerminalReader(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// publishPersistenceRootEnv normalizes the effective config+data root to an
// absolute path and exports it as KENT_PERSISTENCE_ROOT so the resolved root
// propagates to child processes (subagents launched via `kent run`, shell
// ripgrep config) and any downstream re-resolution. The flag value wins; when
// it is blank, an inherited KENT_PERSISTENCE_ROOT is normalized in place so a
// relative env value (e.g. `KENT_PERSISTENCE_ROOT=rel kent serve`) does not get
// re-resolved against a child's different working directory. A blank flag with
// no inherited env leaves the environment untouched.
func publishPersistenceRootEnv(flagValue string) error {
	trimmed := strings.TrimSpace(flagValue)
	if trimmed == "" {
		trimmed = strings.TrimSpace(os.Getenv(config.PersistenceRootEnvName))
		if trimmed == "" {
			return nil
		}
	}
	abs, err := config.NormalizePersistenceRoot(trimmed)
	if err != nil {
		return err
	}
	return os.Setenv(config.PersistenceRootEnvName, abs)
}

func runSubcommand(args []string) int {
	if len(args) > 0 && args[0] == "wait" {
		return runLiveWaitSubcommand(args[1:])
	}
	switch liveControlSubcommand(args) {
	case "steer":
		return runLiveSteerSubcommand(args[1:])
	case "stop":
		return runLiveStopSubcommand(args[1:])
	case "watch":
		return runLiveWatchSubcommand(args[1:])
	}
	runFS := flag.NewFlagSet(config.Command+" run", flag.ContinueOnError)
	runFS.SetOutput(os.Stderr)
	runFS.Usage = func() { runUsage.write(runFS) }
	flags := registerCommonFlags(runFS, true)
	agentRoleRaw := runFS.String("agent", "", "configured subagent role; use default for base settings")
	fastRole := runFS.Bool("fast", false, "use the built-in fast subagent role")
	timeoutRaw := runFS.String("timeout", "", "maximum run duration, such as 30s or 2m")
	outputModeRaw := runFS.String("output-mode", string(runOutputModeFinalText), "result format: final-text|json")
	progressModeRaw := runFS.String("progress-mode", string(runProgressModeStderr), "live output: stderr|quiet")
	quiet := false
	runFS.BoolVar(&quiet, "quiet", false, "suppress live output and print only the final result")
	runFS.BoolVar(&quiet, "q", false, "shorthand for --quiet")
	usageOutputMode := inferRunOutputMode(args)
	if err := runFS.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		emitRunUsageError(usageOutputMode, err.Error())
		return 2
	}
	markExplicitCommonFlags(runFS, flags)
	sessionID, err := effectiveSessionID(*flags)
	if err != nil {
		emitRunUsageError(usageOutputMode, err.Error())
		return 2
	}
	workspaceContextSessionID := ""
	if envSessionID, ok := sessionenv.LookupSessionID(os.LookupEnv); ok {
		workspaceContextSessionID = envSessionID
	}
	outputMode, err := parseRunOutputMode(*outputModeRaw)
	if err != nil {
		emitRunUsageError(usageOutputMode, err.Error())
		return 2
	}
	if flagExplicit(runFS, "agent") && strings.TrimSpace(*agentRoleRaw) == "" {
		emitRunUsageError(outputMode, "invalid --agent value "+strconv.Quote(*agentRoleRaw))
		return 2
	}
	agentRole, err := effectiveRunAgentRole(*agentRoleRaw, *fastRole)
	if err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}

	remaining := runFS.Args()
	if len(remaining) == 0 {
		emitRunUsageError(outputMode, "prompt argument is required")
		return 2
	}
	prompt := strings.TrimSpace(strings.Join(remaining, " "))
	if prompt == "" {
		emitRunUsageError(outputMode, "prompt argument is required")
		return 2
	}

	timeout, err := parseRunTimeout(*timeoutRaw)
	if err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}
	progressMode, err := parseRunProgressMode(*progressModeRaw)
	if err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}
	if quiet {
		if flagExplicit(runFS, "progress-mode") && progressMode != runProgressModeQuiet {
			emitRunUsageError(outputMode, "--quiet conflicts with --progress-mode="+string(progressMode))
			return 2
		}
		progressMode = runProgressModeQuiet
	}
	if outputMode == runOutputModeJSON {
		progressMode = runProgressModeQuiet
	}
	if err := publishPersistenceRootEnv(flags.PersistenceRoot); err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := app.Options{
		WorkspaceRoot:             flags.WorkspaceRoot,
		WorkspaceRootExplicit:     flags.WorkspaceExplicit,
		SessionID:                 sessionID,
		WorkspaceContextSessionID: workspaceContextSessionID,
		AgentRole:                 agentRole,
		Model:                     flags.Model,
		ProviderOverride:          flags.ProviderOverride,
		ThinkingLevel:             flags.ThinkingLevel,
		Theme:                     flags.Theme,
		ModelTimeoutSeconds:       flags.ModelTimeoutSeconds,
		Tools:                     flags.Tools,
		OpenAIBaseURL:             flags.OpenAIBaseURL,
		OpenAIBaseURLExplicit:     flags.OpenAIBaseURLExplicit,
		ConfigRoot:                strings.TrimSpace(flags.PersistenceRoot),
	}

	var progress serverapi.RunPromptProgressSink
	var progressRenderer *runProgressRenderer
	if progressMode == runProgressModeStderr {
		progressRenderer = newRunProgressRenderer(os.Stdout, os.Stderr)
		progress = progressRenderer
	}
	result, runErr := runPromptApp(ctx, opts, prompt, timeout, progress)
	continueID := strings.TrimSpace(result.SessionID)
	continueRoot := continueCommandPersistenceRoot(flags.PersistenceRoot)
	continueCmd := prompts.ContinueRunCommandWithRoot(continueID, continueRoot)
	continueHint := buildRunContinueHint(continueID, continueRoot)
	if runErr != nil {
		code := runErrorCode(runErr)
		if outputMode == runOutputModeJSON {
			emitRunJSON(runJSONResult{
				Status:      "error",
				SessionID:   result.SessionID,
				SessionName: result.SessionName,
				ContinueID:  continueID,
				ContinueCmd: continueCmd,
				Warnings:    append([]string(nil), result.Warnings...),
				DurationMS:  result.Duration.Milliseconds(),
				Error:       newRunJSONError(runErr),
			})
		} else {
			emitWarnings(os.Stderr, result.Warnings)
			fmt.Fprintln(os.Stderr, runErrorMessage(runErr))
			if continueHint != "" {
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, continueHint)
			}
		}
		if code == "interrupted" {
			return 130
		}
		return 1
	}
	if outputMode == runOutputModeJSON {
		emitRunJSON(runJSONResult{
			Status:      "ok",
			Result:      result.Result,
			SessionID:   result.SessionID,
			SessionName: result.SessionName,
			ContinueID:  continueID,
			ContinueCmd: continueCmd,
			Warnings:    append([]string(nil), result.Warnings...),
			DurationMS:  result.Duration.Milliseconds(),
		})
	} else {
		if progressRenderer != nil {
			progressRenderer.Complete(result.Result, result.Warnings, continueHint)
		} else {
			emitRunFinalText(os.Stdout, result.Warnings, result.Result, continueHint)
		}
	}
	return 0
}

func liveControlSubcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	verb := args[0]
	switch verb {
	case "steer", "stop", "watch":
	default:
		return ""
	}
	positionals, help := liveControlPositionals(verb, args[1:])
	if help || len(args) == 1 || len(positionals) == 0 {
		return verb
	}
	if verb == "watch" {
		return verb
	}
	sessionID, err := runtimeids.ParseSessionID(positionals[0])
	if err != nil || !sessionID.IsCanonicalUUIDv4() {
		return ""
	}
	switch verb {
	case "steer":
		if len(positionals) >= 2 {
			return verb
		}
	case "stop":
		if len(positionals) == 1 {
			return verb
		}
	}
	return ""
}

func liveControlPositionals(verb string, args []string) ([]string, bool) {
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			positionals = append(positionals, args[i+1:]...)
			return positionals, false
		case arg == "-h" || arg == "--help":
			return positionals, true
		case arg == "--persistence-root":
			i++
		case strings.HasPrefix(arg, "--persistence-root="):
		case verb == "wait" && arg == "--output-mode":
			i++
		case verb == "wait" && strings.HasPrefix(arg, "--output-mode="):
		default:
			positionals = append(positionals, arg)
		}
	}
	return positionals, false
}

func runLiveSteerSubcommand(args []string) int {
	fs := newCommandFlagSet(config.Command+" run steer", os.Stderr, leafCommandUsage(
		config.Command+" run steer [--persistence-root <root>] <session-id> <message...>",
		"Queue a message for an active run.",
		"",
		"Use `run --continue` when the session is idle.",
	))
	persistenceRoot := fs.String("persistence-root", "", persistenceRootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return runLiveFlagError(err)
	}
	remaining := fs.Args()
	if len(remaining) < 2 {
		emitRunUsageError(runOutputModeFinalText, "usage: kent run steer <session-id> <message>")
		return 2
	}
	sessionID, err := parseCLILiveSessionID(remaining[0])
	if err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	message := strings.TrimSpace(strings.Join(remaining[1:], " "))
	if message == "" {
		emitRunUsageError(runOutputModeFinalText, "message is required")
		return 2
	}
	if err := rejectSelfTarget(sessionID, "kent run steer "+strings.Join(args, " ")); err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	if _, err := app.LiveSteerCallerSessionID(); err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := runLiveSteerApp(ctx, app.Options{ConfigRoot: strings.TrimSpace(*persistenceRoot)}, sessionID, message); err != nil {
		fmt.Fprintln(os.Stderr, runErrorMessage(err))
		if errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintf(os.Stderr, "To continue this session instead, execute `%s`.\n", buildRunSteerContinueCommand(sessionID.String(), continueCommandPersistenceRoot(*persistenceRoot), message))
		}
		return 1
	}
	fmt.Fprintln(os.Stdout, "ok")
	return 0
}

func runLiveStopSubcommand(args []string) int {
	fs := newCommandFlagSet(config.Command+" run stop", os.Stderr, leafCommandUsage(
		config.Command+" run stop [--persistence-root <root>] <session-id>",
		"Interrupt an active run.",
	))
	persistenceRoot := fs.String("persistence-root", "", persistenceRootFlagUsage)
	if err := fs.Parse(args); err != nil {
		return runLiveFlagError(err)
	}
	remaining := fs.Args()
	if len(remaining) != 1 {
		emitRunUsageError(runOutputModeFinalText, "usage: kent run stop <session-id>")
		return 2
	}
	sessionID, err := parseCLILiveSessionID(remaining[0])
	if err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	if err := rejectSelfTarget(sessionID, "kent run stop "+strings.Join(args, " ")); err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runLiveStopApp(ctx, app.Options{ConfigRoot: strings.TrimSpace(*persistenceRoot)}, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, runErrorMessage(err))
		return 1
	}
	if result.Status == serverapi.RuntimeLiveStopStatusStopped {
		fmt.Fprintln(os.Stdout, "Stopped")
	} else {
		fmt.Fprintln(os.Stdout, "No active run")
	}
	return 0
}

func runLiveWaitSubcommand(args []string) int {
	var diagnostics bytes.Buffer
	fs := newCommandFlagSet(config.Command+" run wait", &diagnostics, leafCommandUsage(
		config.Command+" run wait [--output-mode <mode>] [--persistence-root <root>] <session-id>",
		"Wait for an active run and print its final result.",
	))
	persistenceRoot := fs.String("persistence-root", "", persistenceRootFlagUsage)
	outputModeRaw := fs.String("output-mode", string(runOutputModeFinalText), "result format: final-text|json")
	outputMode, ok, code := parseObservationFlags(fs, args, outputModeRaw, &diagnostics)
	if !ok {
		return code
	}
	remaining := fs.Args()
	if len(remaining) != 1 {
		return runObservationUsage(outputMode, "usage: kent run wait <session-id>")
	}
	sessionID, err := parseCLILiveSessionID(remaining[0])
	if err != nil {
		return runObservationUsage(outputMode, err.Error())
	}
	if !sessionID.IsCanonicalUUIDv4() {
		return runObservationUsage(outputMode, "session ID must be a canonical UUIDv4")
	}
	if err := rejectSelfTarget(sessionID, "kent run wait "+strings.Join(args, " ")); err != nil {
		return runObservationUsage(outputMode, err.Error())
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		return runObservationUsage(outputMode, err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result app.RunPromptResult
	var runErr error
	var closeFn func() error
	if outputMode == runOutputModeJSON {
		observed := app.RunLiveWaitWithCleanup(ctx, app.Options{ConfigRoot: strings.TrimSpace(*persistenceRoot)}, sessionID)
		result, runErr, closeFn = observed.Result, observed.Error, observed.Close
	} else {
		result, runErr = runLiveWaitApp(ctx, app.Options{ConfigRoot: strings.TrimSpace(*persistenceRoot)}, sessionID)
	}
	continueRoot := continueCommandPersistenceRoot(*persistenceRoot)
	continueHint := buildRunContinueHint(result.SessionID, continueRoot)
	if runErr != nil {
		if outputMode == runOutputModeJSON {
			return emitRunWaitJSON(os.Stdout, sessionID.String(), result, runErr, ctx, closeFn)
		} else {
			fmt.Fprintln(os.Stderr, runErrorMessage(runErr))
			if continueHint != "" {
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, continueHint)
			}
		}
		if runErrorCode(runErr) == "interrupted" {
			return 130
		}
		return 1
	}
	if outputMode == runOutputModeJSON {
		return emitRunWaitJSON(os.Stdout, sessionID.String(), result, nil, ctx, closeFn)
	}
	emitRunFinalText(os.Stdout, result.Warnings, result.Result, continueHint)
	return 0
}

func runLiveWatchSubcommand(args []string) int {
	return runLiveWatchSubcommandWithCleanup(args, app.RunLiveWatchWithCleanup)
}

type liveWatchCleanupRunner func(context.Context, app.Options, runtimeids.SessionID) app.RunLiveWatchResult

func runLiveWatchSubcommandWithCleanup(args []string, runWithCleanup liveWatchCleanupRunner) int {
	var diagnostics bytes.Buffer
	fs := newCommandFlagSet(config.Command+" run watch", &diagnostics, leafCommandUsage(
		config.Command+" run watch [--output-mode <mode>] [--persistence-root <root>] <session-id>",
		"Watch the next active run outcome.",
	))
	persistenceRoot := fs.String("persistence-root", "", persistenceRootFlagUsage)
	outputModeRaw := fs.String("output-mode", string(runOutputModeFinalText), "result format: final-text|json")
	outputMode, ok, code := parseObservationFlags(fs, args, outputModeRaw, &diagnostics)
	if !ok {
		return code
	}
	if len(fs.Args()) != 1 {
		return runObservationUsage(outputMode, "usage: kent run watch <session-id>")
	}
	sessionID, err := parseCLILiveSessionID(fs.Args()[0])
	if err != nil {
		return runObservationUsage(outputMode, err.Error())
	}
	if err := rejectSelfTarget(sessionID, "kent run watch "+strings.Join(args, " ")); err != nil {
		return runObservationUsage(outputMode, err.Error())
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		return runObservationUsage(outputMode, err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := runWithCleanup(ctx, app.Options{ConfigRoot: strings.TrimSpace(*persistenceRoot)}, sessionID)
	response := result.Response
	if result.Error != nil {
		if outputMode == runOutputModeJSON {
			return emitObservationError(os.Stdout, observationOperationRunWatch, observationTargetSession(sessionID.String()), ctx, result.Error, nil, result.Close)
		}
		if result.Close != nil {
			_ = result.Close()
		}
		fmt.Fprintln(os.Stderr, result.Error)
		if errors.Is(result.Error, context.Canceled) {
			return 130
		}
		return 1
	}
	if outputMode == runOutputModeJSON {
		envelope, exitCode, projectionErr := projectRunWatchJSON(sessionID.String(), response)
		if projectionErr != nil {
			return emitObservationError(os.Stdout, observationOperationRunWatch, observationTargetSession(sessionID.String()), ctx, &client.InvalidResponseError{
				Operation: "runtime live watch", Cause: projectionErr,
			}, nil, result.Close)
		}
		return emitObservationJSONWithCleanup(os.Stdout, envelope, exitCode, nil, result.Close)
	}
	if result.Close != nil {
		_ = result.Close()
	}
	return writeRunWatchResponse(os.Stdout, os.Stderr, response, buildRunContinueHint(response.SessionID, continueCommandPersistenceRoot(*persistenceRoot)))
}

func runLiveFlagError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	emitRunUsageError(runOutputModeFinalText, err.Error())
	return 2
}

func runObservationUsage(mode runOutputMode, message string) int {
	if mode == runOutputModeJSON {
		return writeObservationUsage(os.Stdout, message)
	}
	emitRunUsageError(mode, message)
	return 2
}

func parseObservationFlags(fs *flag.FlagSet, args []string, raw *string, diagnostics *bytes.Buffer) (runOutputMode, bool, int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(os.Stderr, diagnostics)
			return "", false, 0
		}
		mode, modeErr := parseRunOutputMode(*raw)
		if modeErr == nil && mode == runOutputModeJSON {
			return "", false, writeObservationUsage(os.Stdout, err.Error())
		}
		_, _ = io.Copy(os.Stderr, diagnostics)
		return "", false, 2
	}
	mode, err := parseRunOutputMode(*raw)
	if err != nil {
		emitRunUsageError(runOutputModeFinalText, err.Error())
		return "", false, 2
	}
	return mode, true, 0
}

func parseCLILiveSessionID(raw string) (runtimeids.SessionID, error) {
	return runtimeids.ParseSessionID(raw)
}

func rejectSelfTarget(targetSessionID runtimeids.SessionID, commandText string) error {
	currentID, ok := currentSessionIDForSelfTarget()
	if !ok {
		return nil
	}
	if currentID == targetSessionID {
		return errors.New(prompts.RenderLiveControlSelfTargetDeniedPrompt(strings.TrimSpace(commandText)))
	}
	return nil
}

func currentSessionIDForSelfTarget() (runtimeids.SessionID, bool) {
	current, ok := sessionenv.LookupSessionID(os.LookupEnv)
	if !ok {
		return runtimeids.SessionID{}, false
	}
	currentID, err := parseCLILiveSessionID(current)
	if err != nil {
		return runtimeids.SessionID{}, false
	}
	return currentID, true
}

func registerCommonFlags(fs *flag.FlagSet, includeSession bool) *commonFlags {
	flags := &commonFlags{}
	fs.StringVar(&flags.WorkspaceRoot, "workspace", ".", "workspace for a new session")
	if includeSession {
		registerSessionFlagVars(fs, flags)
	}
	fs.StringVar(&flags.Model, "model", "", "model for this session")
	fs.StringVar(&flags.ProviderOverride, "provider-override", "", "provider for a custom or aliased model name")
	fs.StringVar(&flags.ThinkingLevel, "thinking-level", "", "reasoning effort: low|medium|high|xhigh")
	fs.StringVar(&flags.Theme, "theme", "", "theme: light|dark")
	fs.IntVar(&flags.ModelTimeoutSeconds, "model-timeout-seconds", 0, "model request timeout in seconds")
	fs.StringVar(&flags.Tools, "tools", "", "comma-separated enabled tool IDs, such as shell,patch")
	fs.StringVar(&flags.OpenAIBaseURL, "openai-base-url", "", "base URL for an OpenAI-compatible API")
	fs.StringVar(&flags.PersistenceRoot, "persistence-root", "", persistenceRootFlagUsage)
	return flags
}

func registerSessionFlags(fs *flag.FlagSet) *commonFlags {
	flags := &commonFlags{}
	registerSessionFlagVars(fs, flags)
	return flags
}

func registerSessionFlagVars(fs *flag.FlagSet, flags *commonFlags) {
	fs.StringVar(&flags.SessionID, "session", "", "session ID to resume")
	fs.StringVar(&flags.ContinueID, "continue", "", "session ID to resume; alias of --session")
}

func effectiveSessionID(flags commonFlags) (string, error) {
	sessionID := strings.TrimSpace(flags.SessionID)
	continueID := strings.TrimSpace(flags.ContinueID)
	if sessionID != "" && continueID != "" && sessionID != continueID {
		return "", fmt.Errorf("--session and --continue must match when both are provided")
	}
	if continueID != "" {
		return continueID, nil
	}
	if sessionID != "" {
		return sessionID, nil
	}
	return "", nil
}

func sessionIDSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	sessionFS := newCommandFlagSet(config.Command+" session-id", stderr, sessionIDUsage)
	if ok, exitCode := parseCommandFlags(sessionFS, args); !ok {
		return exitCode
	}
	if remaining := sessionFS.Args(); len(remaining) > 0 {
		fmt.Fprintf(stderr, "unknown arguments: %s\n\n", strings.Join(remaining, " "))
		sessionFS.Usage()
		return 2
	}
	sessionID, ok := sessionenv.LookupSessionID(os.LookupEnv)
	if !ok {
		fmt.Fprintf(stderr, "%s is not set; this command only works inside "+config.Product+" shell commands\n", sessionenv.SessionIDEnv)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, sessionID)
	return 0
}

func markExplicitCommonFlags(fs *flag.FlagSet, flags *commonFlags) {
	if fs == nil || flags == nil {
		return
	}
	fs.Visit(func(f *flag.Flag) {
		switch strings.TrimSpace(f.Name) {
		case "workspace":
			flags.WorkspaceExplicit = true
		case "openai-base-url":
			flags.OpenAIBaseURLExplicit = true
		}
	})
}

func flagExplicit(fs *flag.FlagSet, name string) bool {
	if fs == nil {
		return false
	}
	found := false
	fs.Visit(func(f *flag.Flag) {
		if strings.TrimSpace(f.Name) == name {
			found = true
		}
	})
	return found
}

func parseRunTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid --timeout value %q", raw)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("invalid --timeout value %q", raw)
	}
	return parsed, nil
}

func parseRunProgressMode(raw string) (runProgressMode, error) {
	switch runProgressMode(strings.TrimSpace(raw)) {
	case runProgressModeQuiet:
		return runProgressModeQuiet, nil
	case runProgressModeStderr:
		return runProgressModeStderr, nil
	default:
		return "", fmt.Errorf("invalid --progress-mode value %q", raw)
	}
}

func effectiveRunAgentRole(raw string, fast bool) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	var normalized *string
	if trimmed != "" {
		lower := strings.ToLower(trimmed)
		value := ""
		if lower == config.DefaultSubagentRole {
			value = config.DefaultSubagentRole
		} else {
			value = config.NormalizeSubagentSelector(trimmed)
			if value == "" {
				return nil, fmt.Errorf("invalid --agent value %q", raw)
			}
		}
		normalized = &value
	}
	if fast {
		if normalized != nil && *normalized != config.BuiltInSubagentRoleFast {
			return nil, fmt.Errorf("--fast conflicts with --agent %q", raw)
		}
		value := config.BuiltInSubagentRoleFast
		return &value, nil
	}
	return normalized, nil
}

func buildRunContinueHint(sessionID, persistenceRoot string) string {
	command := prompts.ContinueRunCommandWithRoot(sessionID, persistenceRoot)
	if command == "" {
		return ""
	}
	return fmt.Sprintf("To continue this run, execute `%s`.", command)
}

func buildRunSteerContinueCommand(sessionID, persistenceRoot, message string) string {
	sessionID = strings.TrimSpace(sessionID)
	message = strings.TrimSpace(message)
	if sessionID == "" || message == "" {
		return ""
	}
	tokens := []string{"kent", "run"}
	if root := strings.TrimSpace(persistenceRoot); root != "" {
		tokens = append(tokens, "--persistence-root", root)
	}
	tokens = append(tokens, "--continue", sessionID, message)
	return shellCommand(tokens...)
}

// continueCommandPersistenceRoot returns the absolute root to embed in a
// continuation command when the run selected a non-default root via the
// --persistence-root flag. A flag run is one-shot, so the emitted command must
// carry the root to target the same instance; runs that rely on an inherited
// KENT_PERSISTENCE_ROOT keep that env in the caller's shell and need nothing
// added. Returns "" when no flag root was given.
func continueCommandPersistenceRoot(flagValue string) string {
	trimmed := strings.TrimSpace(flagValue)
	if trimmed == "" {
		return ""
	}
	if abs, err := config.NormalizePersistenceRoot(trimmed); err == nil {
		return abs
	}
	return trimmed
}
