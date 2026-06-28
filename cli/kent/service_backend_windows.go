//go:build windows

package main

import (
	"context"
	brand "core/shared/config"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// scmServiceBackend registers the background server as a Windows Service Control
// Manager service. The service runs as LocalSystem (no stored password) and acts
// as a supervisor that launches `kent serve` in the logged-in user's session via
// the user's primary token (see service_supervisor_windows.go), so the server
// keeps the user's full identity while no console window is ever shown.
type scmServiceBackend struct{}

func currentServiceBackend() serviceBackend {
	return scmServiceBackend{}
}

func (scmServiceBackend) Name() string {
	return "scm"
}

var serviceRecoveryResetPeriod = uint32((24 * time.Hour).Seconds())

func (scmServiceBackend) Install(ctx context.Context, spec serviceSpec, force bool, start bool) error {
	if err := ensureServiceLogDir(spec); err != nil {
		return err
	}
	removeLegacyWindowsRegistration(ctx, spec)

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (installing requires Administrator): %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	if existing, openErr := m.OpenService(serviceWindowsServiceName); openErr == nil {
		if !force {
			_ = existing.Close()
			return errors.New(brand.ServiceDisplayName + " is already installed; use --force to rewrite it")
		}
		_, _ = existing.Control(svc.Stop)
		waitForServiceState(existing, svc.Stopped, 5*time.Second)
		deleteErr := existing.Delete()
		_ = existing.Close()
		if deleteErr != nil {
			return fmt.Errorf("remove existing service before reinstall: %w", deleteErr)
		}
		if err := waitForServiceAbsent(m, 5*time.Second); err != nil {
			return err
		}
	}

	config := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        windows.SERVICE_AUTO_START,
		ErrorControl:     windows.SERVICE_ERROR_NORMAL,
		DisplayName:      brand.ServiceDisplayName,
		Description:      brand.Product + " background server. Runs as the logged-in user with no console window.",
		ServiceStartName: "LocalSystem",
	}
	service, err := m.CreateService(serviceWindowsServiceName, spec.Executable, config, windowsServiceRunArguments(spec.Config.PersistenceRoot)...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer func() { _ = service.Close() }()

	if err := service.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
	}, serviceRecoveryResetPeriod); err != nil {
		return fmt.Errorf("configure restart-on-failure: %w", err)
	}

	// Best-effort: grant the installing user start/stop/query so lifecycle
	// commands do not need elevation. A failure here only means those commands
	// will require Administrator, so it must not fail the install.
	_ = grantUserServiceAccess(service)

	if start {
		if err := service.Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
	}
	return nil
}

func (scmServiceBackend) Uninstall(ctx context.Context, spec serviceSpec, stop bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (uninstalling requires Administrator): %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	service, err := m.OpenService(serviceWindowsServiceName)
	if err != nil {
		// Not installed: clean up any leftover legacy registration and pid file.
		removeLegacyWindowsRegistration(ctx, spec)
		_ = os.Remove(windowsServerPIDPath(spec))
		return nil
	}
	defer func() { _ = service.Close() }()

	if stop {
		_, _ = service.Control(svc.Stop)
		waitForServiceState(service, svc.Stopped, 5*time.Second)
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	removeLegacyWindowsRegistration(ctx, spec)
	_ = os.Remove(windowsServerPIDPath(spec))
	return nil
}

func (scmServiceBackend) Start(ctx context.Context, spec serviceSpec) error {
	service, cleanup, err := openServiceWithAccess(windows.SERVICE_START | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := service.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func (scmServiceBackend) Stop(ctx context.Context, spec serviceSpec) error {
	service, cleanup, err := openServiceWithAccess(windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := service.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	waitForServiceState(service, svc.Stopped, 5*time.Second)
	return nil
}

func (scmServiceBackend) Restart(ctx context.Context, spec serviceSpec) error {
	service, cleanup, err := openServiceWithAccess(windows.SERVICE_START | windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := service.Control(svc.Stop); err == nil {
		waitForServiceState(service, svc.Stopped, 5*time.Second)
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func (scmServiceBackend) Status(ctx context.Context, spec serviceSpec) (serviceStatus, error) {
	status := serviceStatus{
		Backend:     "scm",
		Endpoint:    spec.Endpoint,
		Logs:        []string{spec.StdoutLogPath, spec.StderrLogPath},
		InstallPath: spec.Executable,
	}
	service, cleanup, err := openServiceWithAccess(windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return status, nil
		}
		return serviceStatus{}, err
	}
	defer cleanup()
	status.Installed = true
	status.Loaded = true
	if query, err := service.Query(); err == nil {
		status.Running = query.State == svc.Running || query.State == svc.StartPending
	}
	if cfg, err := service.Config(); err == nil {
		if command, err := windows.DecomposeCommandLine(cfg.BinaryPathName); err == nil {
			status.Command = command
		}
	}
	status.PID = readWindowsServerPID(spec)
	return status, nil
}

// windowsServiceDir is the per-root directory holding service runtime state.
func windowsServiceDir(spec serviceSpec) string {
	return filepath.Join(spec.Config.PersistenceRoot, "service")
}

// windowsServerPIDPath is where the supervisor records the launched server's
// PID. Status reads it so ownership checks compare against the actual server
// process (the SCM-reported PID is the supervisor, not the server).
func windowsServerPIDPath(spec serviceSpec) string {
	return filepath.Join(windowsServiceDir(spec), "server.pid")
}

func readWindowsServerPID(spec serviceSpec) int {
	data, err := os.ReadFile(windowsServerPIDPath(spec))
	if err != nil {
		return 0
	}
	return parsePositiveInt(string(data))
}

// windowsServiceRunArguments are the arguments baked into the registered service
// command: `service run --persistence-root <root>`. The supervisor re-derives the
// server command (`serve --persistence-root <root>`) from the same root.
func windowsServiceRunArguments(persistenceRoot string) []string {
	args := []string{string(serviceActionRun)}
	args = append([]string{"service"}, args...)
	if trimmed := strings.TrimSpace(persistenceRoot); trimmed != "" {
		args = append(args, "--persistence-root", trimmed)
	}
	return args
}

// openServiceWithAccess opens the service through a low-privilege SCM connection
// (SC_MANAGER_CONNECT, available to all users) requesting only the given service
// rights. Combined with the install-time DACL grant, this lets start/stop/status
// run without elevation.
func openServiceWithAccess(access uint32) (*mgr.Service, func(), error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to the service manager: %w", err)
	}
	namePtr, err := windows.UTF16PtrFromString(serviceWindowsServiceName)
	if err != nil {
		_ = windows.CloseServiceHandle(scm)
		return nil, nil, err
	}
	handle, err := windows.OpenService(scm, namePtr, access)
	if err != nil {
		_ = windows.CloseServiceHandle(scm)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, nil, windows.ERROR_SERVICE_DOES_NOT_EXIST
		}
		return nil, nil, fmt.Errorf("open service (is it installed? run `%s service install`): %w", brand.Command, err)
	}
	service := &mgr.Service{Name: serviceWindowsServiceName, Handle: handle}
	cleanup := func() {
		_ = windows.CloseServiceHandle(handle)
		_ = windows.CloseServiceHandle(scm)
	}
	return service, cleanup, nil
}

// grantUserServiceAccess adds an ACE granting the installing user start/stop/query
// on the service so lifecycle commands work without elevation.
func grantUserServiceAccess(service *mgr.Service) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.GetSecurityInfo(service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	existing, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.SERVICE_START | windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS | windows.READ_CONTROL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, existing)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
}

func waitForServiceState(service *mgr.Service, state svc.State, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil || status.State == state {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitForServiceAbsent(m *mgr.Mgr, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		service, err := m.OpenService(serviceWindowsServiceName)
		if err != nil {
			return nil
		}
		_ = service.Close()
		if time.Now().After(deadline) {
			return errors.New("existing service still registered after delete; reboot or stop dependent processes and retry")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// removeLegacyWindowsRegistration tears down the pre-SCM scheduled task and
// Startup-folder launcher so upgraders stop getting the old console windows.
func removeLegacyWindowsRegistration(ctx context.Context, spec serviceSpec) {
	_, _ = runServiceCommand(ctx, "schtasks", "/Delete", "/F", "/TN", serviceWindowsTaskName)
	_ = os.Remove(legacyWindowsStartupItemPath())
	_ = os.Remove(filepath.Join(windowsServiceDir(spec), "server.cmd"))
}

func legacyWindowsStartupItemPath() string {
	base := strings.TrimSpace(os.Getenv("APPDATA"))
	if base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", serviceWindowsTaskName+".cmd")
}
