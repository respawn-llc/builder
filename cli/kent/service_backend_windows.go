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
		if _, err := existing.Control(svc.Stop); err != nil {
			if !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
				_ = existing.Close()
				return fmt.Errorf("stop existing service before reinstall: %w", err)
			}
		} else if werr := waitForServiceState(existing, svc.Stopped, serviceStopWindow); werr != nil {
			_ = existing.Close()
			return fmt.Errorf("stop existing service before reinstall: %w", werr)
		}
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
		Description:      brand.Product + " background server.",
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

	if err := grantUserServiceAccess(service); err != nil {
		return fmt.Errorf("grant service control to the installing user: %w", err)
	}

	persistInstallUser(spec)

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
		if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("open service: %w", err)
		}
		if stop {
			endLegacyWindowsTask(ctx)
		}
		removeLegacyWindowsRegistration(ctx, spec)
		_ = os.Remove(windowsServerPIDPath(spec))
		return nil
	}
	defer func() { _ = service.Close() }()

	if stop {
		if _, err := service.Control(svc.Stop); err != nil {
			if !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
				return fmt.Errorf("stop service before uninstall: %w", err)
			}
		} else if werr := waitForServiceState(service, svc.Stopped, serviceStopWindow); werr != nil {
			return fmt.Errorf("stop service before uninstall: %w", werr)
		}
		endLegacyWindowsTask(ctx)
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	removeLegacyWindowsRegistration(ctx, spec)
	_ = os.Remove(windowsServerPIDPath(spec))
	_ = os.Remove(installUserSIDPath(spec))
	return nil
}

func (scmServiceBackend) Start(ctx context.Context, spec serviceSpec) error {
	service, cleanup, err := openServiceWithAccess(windows.SERVICE_START | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := service.Start(); err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return nil
		}
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
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
		return fmt.Errorf("stop service: %w", err)
	}
	if err := waitForServiceState(service, svc.Stopped, serviceStopWindow); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	return nil
}

func (scmServiceBackend) Restart(ctx context.Context, spec serviceSpec) error {
	service, cleanup, err := openServiceWithAccess(windows.SERVICE_START | windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := service.Control(svc.Stop); err == nil {
		if err := waitForServiceState(service, svc.Stopped, serviceStopWindow); err != nil {
			return fmt.Errorf("wait for service to stop before restart: %w", err)
		}
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

func windowsServiceDir(spec serviceSpec) string {
	return filepath.Join(spec.Config.PersistenceRoot, "service")
}

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

func installUserSIDPath(spec serviceSpec) string {
	return filepath.Join(windowsServiceDir(spec), "install_user.sid")
}

func persistInstallUser(spec serviceSpec) {
	sid, err := interactiveUserSID()
	if err != nil {
		return
	}
	_ = os.MkdirAll(windowsServiceDir(spec), 0o755)
	_ = os.WriteFile(installUserSIDPath(spec), []byte(sid.String()), 0o644)
}

func readInstallUserSID(spec serviceSpec) string {
	data, err := os.ReadFile(installUserSIDPath(spec))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func windowsServiceRunArguments(persistenceRoot string) []string {
	args := []string{string(serviceActionRun)}
	args = append([]string{"service"}, args...)
	if trimmed := strings.TrimSpace(persistenceRoot); trimmed != "" {
		args = append(args, "--persistence-root", trimmed)
	}
	return args
}

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

func grantUserServiceAccess(service *mgr.Service) error {
	descriptor, err := windows.GetSecurityInfo(service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	existing, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	sid, err := interactiveUserSID()
	if err != nil {

		token, terr := windows.OpenCurrentProcessToken()
		if terr != nil {
			return terr
		}
		defer func() { _ = token.Close() }()
		user, uerr := token.GetTokenUser()
		if uerr != nil {
			return uerr
		}
		sid = user.User.Sid
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.SERVICE_START | windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG | windows.READ_CONTROL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, existing)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(service.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
}

func interactiveUserSID() (*windows.SID, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return nil, fmt.Errorf("resolve install session: %w", err)
	}
	if session == 0 {
		return nil, errors.New("install is not running in an interactive session")
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return nil, fmt.Errorf("query install session user token: %w", err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get install session user: %w", err)
	}
	return user.User.Sid, nil
}

func waitForServiceState(service *mgr.Service, state svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("query service state: %w", err)
		}
		if status.State == state {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for service to reach state %d", state)
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

func (scmServiceBackend) stopLegacyServer(ctx context.Context, spec serviceSpec) {
	endLegacyWindowsTask(ctx)
}

func endLegacyWindowsTask(ctx context.Context) {
	_, _ = runServiceCommand(ctx, "schtasks", "/End", "/TN", serviceWindowsTaskName)
}

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
