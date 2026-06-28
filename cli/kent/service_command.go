package main

import (
	"context"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/sessionenv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type serviceAction string

const (
	serviceActionStatus    serviceAction = "status"
	serviceActionInstall   serviceAction = "install"
	serviceActionUninstall serviceAction = "uninstall"
	serviceActionStart     serviceAction = "start"
	serviceActionStop      serviceAction = "stop"
	serviceActionRestart   serviceAction = "restart"
	// serviceActionRun is the internal entry the OS service manager invokes to
	// host the background service in-process. It is not advertised in usage and
	// is only meaningful on platforms with an SCM-style host (Windows).
	serviceActionRun serviceAction = "run"
)

type serviceCommandOptions struct {
	JSON        bool
	Force       bool
	NoStart     bool
	KeepRunning bool
	IfInstalled bool
}

const servicePersistenceRootFlagUsage = "config and data root directory (overrides KENT_PERSISTENCE_ROOT and the default ~/.kent)"

// commitServicePersistenceRoot publishes a --persistence-root flag value to
// KENT_PERSISTENCE_ROOT so every service operation resolves the same config+data
// root (install bakes it into the launched unit; status/start/stop target the
// matching instance). It returns (exitCode, false) when the value is invalid.
func commitServicePersistenceRoot(value string, stderr io.Writer) (int, bool) {
	if err := publishPersistenceRootEnv(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 2, false
	}
	return 0, true
}

func serviceSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs := newCommandFlagSet(config.Command+" service", stderr, serviceUsage)
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	action := serviceAction(strings.TrimSpace(args[0]))
	switch action {
	case serviceActionStatus:
		return serviceStatusSubcommand(args[1:], stdout, stderr)
	case serviceActionInstall:
		return serviceInstallSubcommand(args[1:], stdout, stderr)
	case serviceActionUninstall:
		return serviceUninstallSubcommand(args[1:], stdout, stderr)
	case serviceActionStart:
		return serviceLifecycleSubcommand(action, args[1:], stdout, stderr)
	case serviceActionStop:
		return serviceLifecycleSubcommand(action, args[1:], stdout, stderr)
	case serviceActionRestart:
		return serviceRestartSubcommand(args[1:], stdout, stderr)
	case serviceActionRun:
		return serviceRunSubcommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown service command: %s\n\n", args[0])
		fs := newCommandFlagSet(config.Command+" service", stderr, serviceUsage)
		serviceUsage.write(fs)
		return 2
	}
}

func serviceStatusSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" service status", stderr, serviceStatusUsage)
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	persistenceRoot := fs.String("persistence-root", "", servicePersistenceRootFlagUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "service status does not accept positional arguments")
		return 2
	}
	if code, ok := commitServicePersistenceRoot(*persistenceRoot, stderr); !ok {
		return code
	}
	return runServiceCommandAction(context.Background(), serviceActionStatus, serviceCommandOptions{JSON: *jsonOut}, stdout, stderr)
}

// guardBeforeElevation evaluates the current-session lifecycle guard before any
// UAC relaunch. The elevated child does not inherit KENT_SESSION_ID, so without
// this the guard (enforced later in runServiceCommandAction) would pass in the
// elevated process and let a user reinstall/stop the service hosting their own
// active session. Returns (code, true) when the caller should stop.
func guardBeforeElevation(action serviceAction, opts serviceCommandOptions, stderr io.Writer) (int, bool) {
	if err := ensureServiceLifecycleAllowed(action, opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1, true
	}
	return 0, false
}

func serviceInstallSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" service install", stderr, serviceInstallUsage)
	force := fs.Bool("force", false, "rewrite existing service registration")
	noStart := fs.Bool("no-start", false, "install service without starting it")
	persistenceRoot := fs.String("persistence-root", "", servicePersistenceRootFlagUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "service install does not accept positional arguments")
		return 2
	}
	if code, ok := commitServicePersistenceRoot(*persistenceRoot, stderr); !ok {
		return code
	}
	opts := serviceCommandOptions{Force: *force, NoStart: *noStart}
	if code, blocked := guardBeforeElevation(serviceActionInstall, opts, stderr); blocked {
		return code
	}
	if code, handled := elevateServiceAction(serviceActionInstall); handled {
		return code
	}
	return runServiceCommandAction(context.Background(), serviceActionInstall, opts, stdout, stderr)
}

func serviceUninstallSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" service uninstall", stderr, serviceUninstallUsage)
	keepRunning := fs.Bool("keep-running", false, "remove service registration without stopping current server process")
	persistenceRoot := fs.String("persistence-root", "", servicePersistenceRootFlagUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "service uninstall does not accept positional arguments")
		return 2
	}
	if code, ok := commitServicePersistenceRoot(*persistenceRoot, stderr); !ok {
		return code
	}
	opts := serviceCommandOptions{KeepRunning: *keepRunning}
	if code, blocked := guardBeforeElevation(serviceActionUninstall, opts, stderr); blocked {
		return code
	}
	if code, handled := elevateServiceAction(serviceActionUninstall); handled {
		return code
	}
	return runServiceCommandAction(context.Background(), serviceActionUninstall, opts, stdout, stderr)
}

func serviceLifecycleSubcommand(action serviceAction, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" service "+string(action), stderr, commandUsage{
		title: "Usage of " + config.Command + " service " + string(action) + ":",
		lines: []string{"  " + config.Command + " service " + string(action)},
	})
	persistenceRoot := fs.String("persistence-root", "", servicePersistenceRootFlagUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintf(stderr, "service %s does not accept positional arguments\n", action)
		return 2
	}
	if code, ok := commitServicePersistenceRoot(*persistenceRoot, stderr); !ok {
		return code
	}
	return runServiceCommandAction(context.Background(), action, serviceCommandOptions{}, stdout, stderr)
}

func serviceRestartSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" service restart", stderr, serviceRestartUsage)
	ifInstalled := fs.Bool("if-installed", false, "exit successfully without action when service is not installed")
	persistenceRoot := fs.String("persistence-root", "", servicePersistenceRootFlagUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "service restart does not accept positional arguments")
		return 2
	}
	if code, ok := commitServicePersistenceRoot(*persistenceRoot, stderr); !ok {
		return code
	}
	// The session guard and elevation for restart are applied inside
	// runServiceCommandAction, after confirming a matching installation exists,
	// so `restart --if-installed` can still no-op from inside a Kent session.
	return runServiceCommandAction(context.Background(), serviceActionRestart, serviceCommandOptions{IfInstalled: *ifInstalled}, stdout, stderr)
}

// serviceRunSubcommand is the internal entry the OS service manager invokes via
// the registered command (`<exe> service run --persistence-root <root>`). It
// resolves the spec for the baked root and hands off to the platform host, which
// supervises the background server. It is not a user-facing command.
func serviceRunSubcommand(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := newCommandFlagSet(config.Command+" service run", stderr, commandUsage{
		title: "Usage of " + config.Command + " service run:",
		lines: []string{"  " + config.Command + " service run  (internal: hosted by the OS service manager)"},
	})
	persistenceRoot := fs.String("persistence-root", "", servicePersistenceRootFlagUsage)
	if ok, exitCode := parseCommandFlags(fs, args); !ok {
		return exitCode
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(stderr, "service run does not accept positional arguments")
		return 2
	}
	if code, ok := commitServicePersistenceRoot(*persistenceRoot, stderr); !ok {
		return code
	}
	spec, err := loadServiceSpec()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return serviceHostRun(spec, stdout, stderr)
}

// legacyServerStopper is implemented by backends whose previous-generation
// registration may have a still-running server that would otherwise trip the
// unmanaged-server preflight during migration.
type legacyServerStopper interface {
	stopLegacyServer(ctx context.Context, spec serviceSpec)
}

// stopLegacyManagedServer best-effort stops a previous-generation server (e.g.
// the Windows scheduled-task launcher) before the install/restart preflight, so
// upgrading from an older Kent does not require manually killing the old server
// first. A no-op on backends without a legacy generation.
func stopLegacyManagedServer(ctx context.Context, backend serviceBackend, spec serviceSpec) {
	if stopper, ok := backend.(legacyServerStopper); ok {
		stopper.stopLegacyServer(ctx, spec)
	}
}

func runServiceCommandAction(ctx context.Context, action serviceAction, opts serviceCommandOptions, stdout io.Writer, stderr io.Writer) int {
	if err := ensureServiceLifecycleAllowed(action, opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	spec, err := loadServiceSpec()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	backend := serviceBackendFactory()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch action {
	case serviceActionStatus:
		status, err := readServiceStatus(ctx, backend, spec)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if opts.JSON {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(status); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		writeServiceStatus(stdout, status)
		return 0
	case serviceActionInstall:
		// Only end the legacy server on paths allowed to stop the service, so a
		// registration-only install (--no-start without --force) never halts a
		// session hosted by the old launcher.
		if serviceLifecycleGuardApplies(serviceActionInstall, opts) {
			stopLegacyManagedServer(ctx, backend, spec)
		}
		if err := ensureNoUnmanagedServerConflictForAction(ctx, backend, spec, serviceActionInstall); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := backend.Install(ctx, spec, opts.Force, !opts.NoStart); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Installed %s.\n", serviceDisplayName)
		if !opts.NoStart {
			fmt.Fprintln(stdout, "Started: yes")
		} else {
			fmt.Fprintln(stdout, "Started: no")
		}
	case serviceActionUninstall:
		if err := ensureServiceRootMatch(ctx, backend, spec); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := backend.Uninstall(ctx, spec, !opts.KeepRunning); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Uninstalled %s.\n", serviceDisplayName)
	case serviceActionStart:
		if err := ensureNoUnmanagedServerConflictForAction(ctx, backend, spec, serviceActionStart); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := backend.Start(ctx, spec); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Started %s.\n", serviceDisplayName)
	case serviceActionStop:
		if err := ensureServiceRootMatch(ctx, backend, spec); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := backend.Stop(ctx, spec); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Stopped %s.\n", serviceDisplayName)
	case serviceActionRestart:
		if !opts.IfInstalled {
			if err := ensureNoUnmanagedServerConflictForAction(ctx, backend, spec, action); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		} else {
			status, err := backend.Status(ctx, spec)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if !status.Installed {
				return 0
			}
			// A registration for a different root is "not installed" from this
			// invocation's perspective, so --if-installed exits as a quiet no-op.
			if rootMismatchError(status, spec) != nil {
				return 0
			}
			// Reinstall rewrites the registration (admin-only on Windows). Elevate
			// only now that a matching installation is confirmed, so a not-installed
			// or root-mismatch no-op above never triggers an unnecessary UAC prompt.
			if code, handled := elevateServiceAction(serviceActionRestart); handled {
				return code
			}
			stopLegacyManagedServer(ctx, backend, spec)
			if err := ensureNoUnmanagedServerConflictForAction(ctx, backend, spec, action); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(stdout, "%s is installed. Restarting it after update; sessions may fail briefly.\n", serviceDisplayName)
			if err := backend.Install(ctx, spec, true, true); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(stdout, "Restarted %s.\n", serviceDisplayName)
			return 0
		}
		if err := backend.Restart(ctx, spec); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Restarted %s.\n", serviceDisplayName)
	}
	return 0
}

// ensureServiceRootMatch reads the current registration and returns an error
// when the installed service serves a different config+data root than the
// resolved spec (or when the status read itself fails). It returns nil when
// there is no conflict (or the installed root cannot be determined). Used by
// stop/uninstall, which otherwise do not read status; start/restart reuse the
// status fetched by ensureNoUnmanagedServerConflictForAction instead of paying a
// second read.
func ensureServiceRootMatch(ctx context.Context, backend serviceBackend, spec serviceSpec) error {
	status, err := backend.Status(ctx, spec)
	if err != nil {
		return err
	}
	return rootMismatchError(status, spec)
}

// rootMismatchError compares the persistence root baked into an installed
// registration's command against the resolved spec root. Lifecycle actions
// target the single global OS registration, so acting on a registration that
// serves a different root is a cross-root footgun. It returns nil when they
// match, when nothing is installed, or when the requested root is the default
// and the installed registration's root cannot be confirmed (a legitimate
// default-root service). Every service this binary installs now bakes
// --persistence-root, and backends report the actual registration command (the
// Windows backend resolves it from the registered scheduled-task action or the
// Startup-folder launcher, never a path under the requested root), so the real
// cross-root footguns — a default/other-root registration targeted with a
// different --persistence-root, or an unpinned/unreadable legacy/manual
// registration targeted with an explicit non-default root — are caught.
func rootMismatchError(status serviceStatus, spec serviceSpec) error {
	if !status.Installed {
		return nil
	}
	installedRoot, ok := persistenceRootFromServiceCommand(status.Command)
	if !ok {
		// The registration's root cannot be confirmed: either the backend could
		// not read/parse its command (empty command — e.g. a malformed plist or
		// unit), or the command carries no --persistence-root (predates root
		// isolation or was installed by hand). Both are indeterminate and treated
		// as the default/global root. Acting on such a registration for an explicit
		// non-default root would target the wrong (single, global) registration, so
		// refuse unless the requested root is itself the default.
		requestedIsDefault, err := config.IsDefaultPersistenceRoot(spec.Config.PersistenceRoot)
		if err != nil {
			return err
		}
		if requestedIsDefault {
			return nil
		}
		return fmt.Errorf("no %s is installed for persistence root %s; the installed service's persistence root could not be confirmed (it predates root isolation, was installed manually, or its registration is unreadable) and is treated as the default root. Reinstall with `%s service install --persistence-root %s` or manage the default root instead", serviceDisplayName, spec.Config.PersistenceRoot, config.Command, spec.Config.PersistenceRoot)
	}
	if config.PersistenceRootHash(installedRoot) == config.PersistenceRootHash(spec.Config.PersistenceRoot) {
		return nil
	}
	return fmt.Errorf("no %s is installed for persistence root %s; the installed service targets %s. Reinstall with `%s service install --persistence-root %s` or manage the matching root instead", serviceDisplayName, spec.Config.PersistenceRoot, installedRoot, config.Command, spec.Config.PersistenceRoot)
}

// persistenceRootFromServiceCommand extracts the --persistence-root value baked
// into an installed service command. It scans structured argv tokens (both the
// "--persistence-root <root>" and "--persistence-root=<root>" forms) rather than
// matching substrings.
func persistenceRootFromServiceCommand(command []string) (string, bool) {
	const flag = "--persistence-root"
	for i, arg := range command {
		if arg == flag {
			if i+1 < len(command) {
				return command[i+1], true
			}
			return "", false
		}
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value, true
		}
	}
	return "", false
}

func ensureNoUnmanagedServerConflictForAction(ctx context.Context, backend serviceBackend, spec serviceSpec, action serviceAction) error {
	status, err := backend.Status(ctx, spec)
	if err != nil {
		return err
	}
	// start/restart must not act on a registration installed for a different
	// root. install (re)writes the registration, so it is exempt.
	if action == serviceActionStart || action == serviceActionRestart {
		if mismatch := rootMismatchError(status, spec); mismatch != nil {
			return mismatch
		}
	}
	healthStatus, healthPID := probeServiceHealth(ctx, spec)
	healthRunning := healthStatus == protocol.HealthStatusOK
	pidProof := status.PID > 0 && healthPID > 0 && status.PID == healthPID
	commandProof := len(status.Command) > 0 && commandArgsEqual(status.Command, serviceCommand(spec))
	backendOwnsHealthyServer := healthRunning && status.Running && status.Loaded && (pidProof || commandProof)
	if healthRunning && !backendOwnsHealthyServer {
		if action == serviceActionRestart && status.Installed && (!status.Loaded || !status.Running) {
			return nil
		}
		pidText := ""
		if healthPID > 0 {
			pidText = fmt.Sprintf(" (pid %d)", healthPID)
		}
		return fmt.Errorf(config.Product+" server is already running outside the background service on %s%s. Stop it before changing the service", spec.Endpoint, pidText)
	}
	if status.Running && status.Installed && !status.Loaded {
		return fmt.Errorf(config.Product+" server is already running on %s, but the background service is not loaded. Stop the manual server or run `"+config.Command+" service restart` after fixing service state", spec.Endpoint)
	}
	if !healthRunning {
		if status.Installed && status.Loaded && status.Running {
			return nil
		}
		dialer := net.Dialer{Timeout: 500 * time.Millisecond}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(spec.Config.Settings.ServerHost, strconv.Itoa(spec.Config.Settings.ServerPort)))
		if err == nil {
			_ = conn.Close()
			return fmt.Errorf("server port %s is already in use, but it is not responding as "+config.Product+". Stop the process using that port before installing the background service", net.JoinHostPort(spec.Config.Settings.ServerHost, strconv.Itoa(spec.Config.Settings.ServerPort)))
		}
	}
	return nil
}

const serviceLifecycleCurrentSessionError = "you may not manage the service now as this may kill your current session, halting your work. Ask the user to manage the service manually."

func serviceLifecycleGuardApplies(action serviceAction, opts serviceCommandOptions) bool {
	switch action {
	case serviceActionRestart:
		return true
	case serviceActionInstall:
		// --force rewrites (stops, deletes, recreates) an existing service, so it
		// can halt the session even with --no-start; guard it regardless.
		return !opts.NoStart || opts.Force
	case serviceActionUninstall:
		return !opts.KeepRunning
	case serviceActionStart:
		return true
	case serviceActionStop:
		return true
	default:
		return false
	}
}

func ensureServiceLifecycleAllowed(action serviceAction, opts serviceCommandOptions) error {
	if !serviceLifecycleGuardApplies(action, opts) {
		return nil
	}
	if _, ok := sessionenv.LookupSessionID(os.LookupEnv); !ok {
		return nil
	}
	return errors.New(serviceLifecycleCurrentSessionError)
}

func readServiceStatus(ctx context.Context, backend serviceBackend, spec serviceSpec) (serviceStatus, error) {
	status, err := backend.Status(ctx, spec)
	if err != nil {
		return serviceStatus{}, err
	}
	// Evaluate the root match against the raw registration command before the
	// substitution below replaces an empty command with the requested-root one,
	// which would otherwise mask a registration that serves a different root.
	mismatched := rootMismatchError(status, spec) != nil
	status.Backend = backend.Name()
	status.Endpoint = spec.Endpoint
	status.Logs = []string{spec.StdoutLogPath, spec.StderrLogPath}
	if len(status.Command) == 0 {
		status.Command = serviceCommand(spec)
	}
	if mismatched {
		// A registration for a different (or unconfirmable) root is "not
		// installed" from the requested root's perspective, so status never
		// reports another root's service as installed/running for this one.
		status.Installed = false
		status.Loaded = false
		status.Running = false
		status.PID = 0
	}
	return applyHealthProbe(ctx, status, spec), nil
}

func writeServiceStatus(stdout io.Writer, status serviceStatus) {
	state := "not installed"
	if status.Installed && status.Running {
		state = "running"
	} else if status.Installed {
		state = "stopped"
	} else if status.HealthStatus == protocol.HealthStatusOK {
		state = "not installed (server running manually)"
	}
	fmt.Fprintf(stdout, config.ServiceDisplayName+": %s\n", state)
	fmt.Fprintf(stdout, "Backend: %s\n", status.Backend)
	if status.PID > 0 {
		fmt.Fprintf(stdout, "PID: %d\n", status.PID)
	} else if status.HealthPID > 0 {
		fmt.Fprintf(stdout, "PID: %d\n", status.HealthPID)
	}
	if len(status.Command) > 0 {
		fmt.Fprintf(stdout, "Command: %s\n", commandString(status.Command))
	}
	fmt.Fprintf(stdout, "Endpoint: %s\n", status.Endpoint)
	if len(status.Logs) > 0 {
		fmt.Fprintf(stdout, "Logs: %s\n", strings.Join(status.Logs, ", "))
	}
	for _, hint := range status.Hints {
		fmt.Fprintf(stdout, "Hint: %s\n", hint)
	}
	if strings.TrimSpace(status.Detail) != "" {
		fmt.Fprintf(stdout, "Detail: %s\n", status.Detail)
	}
}
