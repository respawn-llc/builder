package main

import (
	"context"
	"encoding/json"
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
	"core/shared/config"
	"core/shared/llmerrors"
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

type runJSONResult struct {
	Status      string        `json:"status"`
	Result      string        `json:"result,omitempty"`
	SessionID   string        `json:"session_id,omitempty"`
	SessionName string        `json:"session_name,omitempty"`
	ContinueID  string        `json:"continue_id,omitempty"`
	ContinueCmd string        `json:"continue_command,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
	DurationMS  int64         `json:"duration_ms"`
	Error       *runJSONError `json:"error,omitempty"`
}

type runJSONError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type runOutputMode string

const (
	runOutputModeFinalText runOutputMode = "final-text"
	runOutputModeJSON      runOutputMode = "json"
)

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
	if len(args) > 0 && args[0] == "workflow" {
		return workflowSubcommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && (args[0] == "task" || args[0] == "tasks") {
		return taskSubcommand(args[1:], stdout, stderr)
	}

	rootFS := newCommandFlagSet(config.Command, stderr, rootUsage)
	showVersion := rootFS.Bool("version", false, "print version and exit")
	forceInteractive := rootFS.Bool("force-interactive", false, "run interactive UI even when stdin/stdout are not terminals")
	agentRoleRaw := rootFS.String("agent", "", "subagent role override for the interactive session")
	persistenceRoot := rootFS.String("persistence-root", "", "config and data root directory (overrides KENT_PERSISTENCE_ROOT and the default ~/.kent)")
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
	switch liveControlSubcommand(args) {
	case "steer":
		return runLiveSteerSubcommand(args[1:])
	case "stop":
		return runLiveStopSubcommand(args[1:])
	case "wait":
		return runLiveWaitSubcommand(args[1:])
	}
	runFS := flag.NewFlagSet(config.Command+" run", flag.ContinueOnError)
	runFS.SetOutput(os.Stderr)
	runFS.Usage = func() { runUsage.write(runFS) }
	flags := registerCommonFlags(runFS, true)
	agentRoleRaw := runFS.String("agent", "", "subagent role override")
	fastRole := runFS.Bool("fast", false, "use the built-in fast subagent role")
	timeoutRaw := runFS.String("timeout", "", "optional timeout duration (e.g. 30s, 2m); default is no timeout")
	outputModeRaw := runFS.String("output-mode", string(runOutputModeFinalText), "output mode: final-text|json")
	progressModeRaw := runFS.String("progress-mode", string(runProgressModeQuiet), "progress mode: quiet|stderr")
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
	if sessionID == "" {
		if envSessionID, ok := sessionenv.LookupSessionID(os.LookupEnv); ok {
			workspaceContextSessionID = envSessionID
		}
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

	var progress io.Writer
	if progressMode == runProgressModeStderr {
		progress = os.Stderr
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
				Error: &runJSONError{
					Code:    code,
					Message: runErrorMessage(runErr),
				},
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
		emitRunFinalText(os.Stdout, result.Warnings, result.Result, continueHint)
	}
	return 0
}

func liveControlSubcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	verb := args[0]
	switch verb {
	case "steer", "stop", "wait":
	default:
		return ""
	}
	positionals, help := liveControlPositionals(verb, args[1:])
	if help || len(args) == 1 || len(positionals) == 0 {
		return verb
	}
	if _, err := runtimeids.ParseSessionID(positionals[0]); err == nil {
		return verb
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
	fs := flag.NewFlagSet(config.Command+" run steer", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	persistenceRoot := fs.String("persistence-root", "", "config and data root directory")
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
	fs := flag.NewFlagSet(config.Command+" run stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	persistenceRoot := fs.String("persistence-root", "", "config and data root directory")
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
	fs := flag.NewFlagSet(config.Command+" run wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	persistenceRoot := fs.String("persistence-root", "", "config and data root directory")
	outputModeRaw := fs.String("output-mode", string(runOutputModeFinalText), "output mode: final-text|json")
	usageOutputMode := inferRunOutputMode(args)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		emitRunUsageError(usageOutputMode, err.Error())
		return 2
	}
	outputMode, err := parseRunOutputMode(*outputModeRaw)
	if err != nil {
		emitRunUsageError(usageOutputMode, err.Error())
		return 2
	}
	remaining := fs.Args()
	if len(remaining) != 1 {
		emitRunUsageError(outputMode, "usage: kent run wait <session-id>")
		return 2
	}
	sessionID, err := parseCLILiveSessionID(remaining[0])
	if err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}
	if err := rejectSelfTarget(sessionID, "kent run wait "+strings.Join(args, " ")); err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}
	if err := publishPersistenceRootEnv(*persistenceRoot); err != nil {
		emitRunUsageError(outputMode, err.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, runErr := runLiveWaitApp(ctx, app.Options{ConfigRoot: strings.TrimSpace(*persistenceRoot)}, sessionID)
	continueRoot := continueCommandPersistenceRoot(*persistenceRoot)
	continueHint := buildRunContinueHint(result.SessionID, continueRoot)
	continueCmd := prompts.ContinueRunCommandWithRoot(result.SessionID, continueRoot)
	if runErr != nil {
		if outputMode == runOutputModeJSON {
			emitRunJSON(runJSONResult{Status: "error", SessionID: result.SessionID, SessionName: result.SessionName, ContinueID: result.SessionID, ContinueCmd: continueCmd, DurationMS: result.Duration.Milliseconds(), Error: &runJSONError{Code: runErrorCode(runErr), Message: runErrorMessage(runErr)}})
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
		emitRunJSON(runJSONResult{Status: "ok", Result: result.Result, SessionID: result.SessionID, SessionName: result.SessionName, ContinueID: result.SessionID, ContinueCmd: continueCmd, DurationMS: result.Duration.Milliseconds()})
		return 0
	}
	emitRunFinalText(os.Stdout, result.Warnings, result.Result, continueHint)
	return 0
}

func runLiveFlagError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	emitRunUsageError(runOutputModeFinalText, err.Error())
	return 2
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
	fs.StringVar(&flags.WorkspaceRoot, "workspace", ".", "workspace root")
	if includeSession {
		registerSessionFlagVars(fs, flags)
	}
	fs.StringVar(&flags.Model, "model", "", "model name override")
	fs.StringVar(&flags.ProviderOverride, "provider-override", "", "provider override for custom/alias model names")
	fs.StringVar(&flags.ThinkingLevel, "thinking-level", "", "thinking level override (low|medium|high|xhigh)")
	fs.StringVar(&flags.Theme, "theme", "", "theme override (light|dark)")
	fs.IntVar(&flags.ModelTimeoutSeconds, "model-timeout-seconds", 0, "model request timeout override in seconds")
	fs.StringVar(&flags.Tools, "tools", "", "enabled tools override as csv (e.g. shell,patch)")
	fs.StringVar(&flags.OpenAIBaseURL, "openai-base-url", "", "OpenAI-compatible base URL override")
	fs.StringVar(&flags.PersistenceRoot, "persistence-root", "", "config and data root directory (overrides KENT_PERSISTENCE_ROOT and the default ~/.kent)")
	return flags
}

func registerSessionFlags(fs *flag.FlagSet) *commonFlags {
	flags := &commonFlags{}
	registerSessionFlagVars(fs, flags)
	return flags
}

func registerSessionFlagVars(fs *flag.FlagSet, flags *commonFlags) {
	fs.StringVar(&flags.SessionID, "session", "", "session id to resume")
	fs.StringVar(&flags.ContinueID, "continue", "", "session id to continue")
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

func parseRunOutputMode(raw string) (runOutputMode, error) {
	switch runOutputMode(strings.TrimSpace(raw)) {
	case runOutputModeFinalText:
		return runOutputModeFinalText, nil
	case runOutputModeJSON:
		return runOutputModeJSON, nil
	default:
		return "", fmt.Errorf("invalid --output-mode value %q", raw)
	}
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

func runErrorMessage(err error) string {
	if message := llmerrors.UserFacingError(err); message != "" {
		return message
	}
	return err.Error()
}

func runErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return "runtime"
}

func emitRunJSON(v runJSONResult) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode JSON output: %v\n", err)
	}
}

func emitRunUsageError(mode runOutputMode, message string) {
	if mode == runOutputModeJSON {
		emitRunJSON(runJSONResult{
			Status: "error",
			Error:  &runJSONError{Code: "usage", Message: message},
		})
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, message)
}

func emitRunFinalText(w io.Writer, warnings []string, result string, continueHint string) {
	if w == nil {
		return
	}
	emitWarnings(w, warnings)
	trimmedResult := strings.TrimRight(result, "\n")
	trimmedHint := strings.TrimSpace(continueHint)
	switch {
	case trimmedResult != "" && trimmedHint != "":
		_, _ = fmt.Fprintf(w, "%s\n\n%s\n", trimmedResult, trimmedHint)
	case trimmedResult != "":
		_, _ = fmt.Fprintln(w, trimmedResult)
	case trimmedHint != "":
		_, _ = fmt.Fprintln(w, trimmedHint)
	}
}

func emitWarnings(w io.Writer, warnings []string) {
	if w == nil || len(warnings) == 0 {
		return
	}
	for _, warning := range warnings {
		trimmed := strings.TrimSpace(warning)
		if trimmed == "" {
			continue
		}
		_, _ = fmt.Fprintln(w, trimmed)
	}
	_, _ = fmt.Fprintln(w)
}

func effectiveRunAgentRole(raw string, fast bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	normalized := ""
	if trimmed != "" {
		lower := strings.ToLower(trimmed)
		if lower == config.DefaultSubagentRole {
			normalized = config.DefaultSubagentRole
		} else {
			normalized = config.NormalizeSubagentSelector(trimmed)
			if normalized == "" {
				return "", fmt.Errorf("invalid --agent value %q", raw)
			}
		}
	}
	if fast {
		if normalized != "" && normalized != config.BuiltInSubagentRoleFast {
			return "", fmt.Errorf("--fast conflicts with --agent %q", raw)
		}
		return config.BuiltInSubagentRoleFast, nil
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
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		quoted = append(quoted, shellQuote(token))
	}
	return strings.Join(quoted, " ")
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

func inferRunOutputMode(args []string) runOutputMode {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--output-mode" || arg == "-output-mode":
			if i+1 >= len(args) {
				return runOutputModeFinalText
			}
			if mode, err := parseRunOutputMode(args[i+1]); err == nil {
				return mode
			}
			return runOutputModeFinalText
		case strings.HasPrefix(arg, "--output-mode="):
			if mode, err := parseRunOutputMode(strings.TrimPrefix(arg, "--output-mode=")); err == nil {
				return mode
			}
			return runOutputModeFinalText
		case strings.HasPrefix(arg, "-output-mode="):
			if mode, err := parseRunOutputMode(strings.TrimPrefix(arg, "-output-mode=")); err == nil {
				return mode
			}
			return runOutputModeFinalText
		}
	}
	return runOutputModeFinalText
}
