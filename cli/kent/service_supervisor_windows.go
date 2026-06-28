//go:build windows

package main

import (
	"context"
	brand "core/shared/config"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	supervisorInitialBackoff = 1 * time.Second
	supervisorMaxBackoff     = 30 * time.Second
	stillActiveExitCode      = 259 // STILL_ACTIVE
	// supervisorStopGracePeriod is how long a graceful stop waits for the server
	// to exit after its shutdown event is signalled before falling back to a hard
	// terminate. Kept well under the service stop WaitHint so the SCM never kills
	// the supervisor (which would orphan the server) mid-shutdown.
	supervisorStopGracePeriod = 15 * time.Second
	// shutdownEventSDDL grants the supervisor (LocalSystem) and Administrators full
	// control of the graceful-stop event and the interactive user wait access, so
	// the server launched under the user token can open and wait on it.
	shutdownEventSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x00100000;;;IU)"
)

// serverSupervisor runs inside the LocalSystem service process and keeps a
// `kent serve` child alive in the interactive user's session. The child is
// launched with the logged-in user's primary token (no stored password), so it
// has the user's full identity (profile, DPAPI, Credential Manager, git/ssh),
// and with CREATE_NO_WINDOW so no console window appears.
type serverSupervisor struct {
	spec   serviceSpec
	wanted atomic.Uint32 // target interactive session id; 0 = no user session
	wake   chan struct{}
	mu     sync.Mutex
	child  *managedChild
}

func newServerSupervisor(spec serviceSpec) *serverSupervisor {
	return &serverSupervisor{spec: spec, wake: make(chan struct{}, 1)}
}

// setWanted records the session the server should run in and nudges the run loop.
func (s *serverSupervisor) setWanted(session uint32) {
	s.wanted.Store(session)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// run is the supervision loop. It launches the child in the wanted session,
// restarts it with backoff if it exits unexpectedly, relaunches it when the
// interactive session changes, and tears it down when the context is cancelled.
func (s *serverSupervisor) run(ctx context.Context) {
	backoff := supervisorInitialBackoff
	for {
		if ctx.Err() != nil {
			s.stopChild()
			return
		}
		session := s.wanted.Load()
		if session == 0 {
			s.stopChild()
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		s.mu.Lock()
		needLaunch := s.child == nil || s.child.session != session || !s.child.alive()
		s.mu.Unlock()
		if needLaunch {
			s.stopChild()
			child, err := launchServerAsUser(s.spec, session)
			if err != nil {
				s.logf("launch failed for session %d: %v", session, err)
				if !s.sleep(ctx, backoff) {
					return
				}
				backoff = growBackoff(backoff)
				continue
			}
			s.mu.Lock()
			s.child = child
			s.mu.Unlock()
			s.writePID(child.pid)
			backoff = supervisorInitialBackoff
		}
		s.mu.Lock()
		child := s.child
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.stopChild()
			return
		case <-s.wake:
			continue
		case <-child.exited:
			child.release()
			s.mu.Lock()
			if s.child == child {
				s.child = nil
			}
			s.mu.Unlock()
			s.clearPID()
			if ctx.Err() != nil {
				return
			}
			if !s.sleep(ctx, backoff) {
				return
			}
			backoff = growBackoff(backoff)
		}
	}
}

// sleep waits for the duration, an early wake, or context cancellation. It
// returns false when the context was cancelled (caller should exit).
func (s *serverSupervisor) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (s *serverSupervisor) stopChild() {
	s.mu.Lock()
	child := s.child
	s.child = nil
	s.mu.Unlock()
	if child != nil {
		child.terminate()
	}
	s.clearPID()
}

func (s *serverSupervisor) writePID(pid uint32) {
	_ = os.MkdirAll(windowsServiceDir(s.spec), 0o755)
	_ = os.WriteFile(windowsServerPIDPath(s.spec), []byte(fmt.Sprintf("%d", pid)), 0o644)
}

func (s *serverSupervisor) clearPID() {
	_ = os.Remove(windowsServerPIDPath(s.spec))
}

// logf appends a supervisor diagnostic to the service error log so launch
// failures are observable even though the supervisor has no console.
func (s *serverSupervisor) logf(format string, args ...any) {
	appendServiceLog(s.spec, format, args...)
}

func appendServiceLog(spec serviceSpec, format string, args ...any) {
	if err := ensureServiceLogDir(spec); err != nil {
		return
	}
	f, err := os.OpenFile(spec.StderrLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "[%s supervisor] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func growBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > supervisorMaxBackoff {
		return supervisorMaxBackoff
	}
	return next
}

// activeUserSession returns the interactive console session id when a user is
// logged in and a primary token can be obtained, else 0 (session 0 is the
// non-interactive services session and is never a target).
func activeUserSession() uint32 {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF || session == 0 {
		return 0
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return 0
	}
	_ = token.Close()
	return session
}

// managedChild is a launched server process plus the user resources that must be
// released when it exits (logon token, loaded profile, environment block, log
// file handles).
type managedChild struct {
	session  uint32
	pid      uint32
	process  windows.Handle
	token    windows.Token
	profile  windows.Handle
	shutdown windows.Handle
	job      windows.Handle
	logs     []windows.Handle
	exited   chan struct{}
	released sync.Once
}

func (c *managedChild) watch() {
	_, _ = windows.WaitForSingleObject(c.process, windows.INFINITE)
	close(c.exited)
}

func (c *managedChild) alive() bool {
	var code uint32
	if err := windows.GetExitCodeProcess(c.process, &code); err != nil {
		return false
	}
	return code == stillActiveExitCode
}

// terminate stops the server, preferring a graceful shutdown: it signals the
// server's shutdown event (equivalent to Ctrl+C) and waits up to the grace
// period for a clean exit, falling back to a hard TerminateProcess only if the
// server does not exit in time or no shutdown event is available.
func (c *managedChild) terminate() {
	if c.shutdown != 0 && windows.SetEvent(c.shutdown) == nil {
		select {
		case <-c.exited:
			c.release()
			return
		case <-time.After(supervisorStopGracePeriod):
		}
	}
	_ = windows.TerminateProcess(c.process, 1)
	select {
	case <-c.exited:
	case <-time.After(5 * time.Second):
	}
	c.release()
}

func (c *managedChild) release() {
	c.released.Do(func() {
		for _, h := range c.logs {
			_ = windows.CloseHandle(h)
		}
		if c.shutdown != 0 {
			_ = windows.CloseHandle(c.shutdown)
		}
		if c.job != 0 {
			_ = windows.CloseHandle(c.job)
		}
		if c.profile != 0 {
			_ = unloadUserProfile(c.token, c.profile)
		}
		if c.token != 0 {
			_ = c.token.Close()
		}
		if c.process != 0 {
			_ = windows.CloseHandle(c.process)
		}
	})
}

// launchServerAsUser starts `kent serve` in the given interactive session as the
// logged-in user, with the user's profile/environment loaded and stdout/stderr
// redirected to the service log files. No console window is created.
func launchServerAsUser(spec serviceSpec, session uint32) (child *managedChild, err error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return nil, fmt.Errorf("query user token for session %d: %w", session, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = token.Close()
		}
	}()

	username, err := tokenUserName(token)
	if err != nil {
		return nil, err
	}
	profile, err := loadUserProfile(token, username)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !committed {
			_ = unloadUserProfile(token, profile)
		}
	}()

	shutdown, shutdownName, err := createShutdownEvent()
	if err != nil {
		return nil, err
	}
	defer func() {
		if !committed {
			_ = windows.CloseHandle(shutdown)
		}
	}()

	env, err := userEnvironment(token, shutdownEventEnvVar, shutdownName)
	if err != nil {
		return nil, err
	}

	if err := ensureServiceLogDir(spec); err != nil {
		return nil, err
	}
	stdout, err := openInheritableAppendFile(spec.StdoutLogPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !committed {
			_ = windows.CloseHandle(stdout)
		}
	}()
	stderr, err := openInheritableAppendFile(spec.StderrLogPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !committed {
			_ = windows.CloseHandle(stderr)
		}
	}()

	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return nil, err
	}
	si := windows.StartupInfo{
		Desktop:   desktop,
		Flags:     windows.STARTF_USESTDHANDLES,
		StdOutput: stdout,
		StdErr:    stderr,
	}
	si.Cb = uint32(unsafe.Sizeof(si))

	appName, err := windows.UTF16PtrFromString(spec.Executable)
	if err != nil {
		return nil, err
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(serviceCommand(spec)))
	if err != nil {
		return nil, err
	}
	var cwd *uint16
	if root := spec.Config.PersistenceRoot; root != "" {
		if cwd, err = windows.UTF16PtrFromString(root); err != nil {
			return nil, err
		}
	}

	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	err = windows.CreateProcessAsUser(token, appName, cmdLine, nil, nil, true, flags, &env[0], cwd, &si, &pi)
	runtime.KeepAlive(env)
	if err != nil {
		return nil, fmt.Errorf("launch server as user in session %d: %w", session, err)
	}
	_ = windows.CloseHandle(pi.Thread)

	// Best-effort: jobbing the server so a supervisor crash cannot leave it
	// orphaned (which the SCM restart would then duplicate). A failure here only
	// loses that safety net; graceful stop and normal teardown still work.
	job, jobErr := assignProcessToKillJob(pi.Process)
	if jobErr != nil {
		appendServiceLog(spec, "kill-on-crash job unavailable for session %d: %v", session, jobErr)
	}

	c := &managedChild{
		session:  session,
		pid:      pi.ProcessId,
		process:  pi.Process,
		token:    token,
		profile:  profile,
		shutdown: shutdown,
		job:      job,
		logs:     []windows.Handle{stdout, stderr},
		exited:   make(chan struct{}),
	}
	go c.watch()
	committed = true
	return c, nil
}

func tokenUserName(token windows.Token) (string, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("get token user: %w", err)
	}
	account, _, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return "", fmt.Errorf("resolve token user name: %w", err)
	}
	return account, nil
}

// createShutdownEvent creates the per-server manual-reset event the supervisor
// signals for a graceful stop. It lives in the Global namespace (the supervisor
// has SeCreateGlobalPrivilege; the server, running as the user, only opens it)
// with a DACL granting the interactive user wait access. The unique random name
// is passed to the server so multiple installs never collide.
func createShutdownEvent() (windows.Handle, string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, "", fmt.Errorf("generate shutdown event name: %w", err)
	}
	name := fmt.Sprintf(`Global\%s-svc-shutdown-%s`, brand.Product, hex.EncodeToString(nonce[:]))
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, "", err
	}
	sd, err := windows.SecurityDescriptorFromString(shutdownEventSDDL)
	if err != nil {
		return 0, "", fmt.Errorf("build shutdown event security descriptor: %w", err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	event, err := windows.CreateEvent(&sa, 1, 0, namePtr)
	if err != nil {
		return 0, "", fmt.Errorf("create shutdown event: %w", err)
	}
	return event, name, nil
}

// userEnvironment builds the environment block for the server from the user's
// profile environment with one extra variable set, returning a Go-owned block
// the caller keeps alive across CreateProcessAsUser. The extra var carries the
// graceful-stop event name only to the supervised child.
func userEnvironment(token windows.Token, key string, value string) ([]uint16, error) {
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err != nil {
		return nil, fmt.Errorf("create environment block: %w", err)
	}
	defer func() { _ = windows.DestroyEnvironmentBlock(block) }()
	entries := append(splitEnvironmentBlock(block), key+"="+value)
	return buildEnvironmentBlock(entries)
}

// splitEnvironmentBlock decodes a double-null-terminated UTF-16 environment block
// into its "KEY=VALUE" entries.
func splitEnvironmentBlock(block *uint16) []string {
	if block == nil {
		return nil
	}
	var entries []string
	for p := block; ; {
		n := 0
		for *(*uint16)(unsafe.Add(unsafe.Pointer(p), uintptr(n)*2)) != 0 {
			n++
		}
		if n == 0 {
			break
		}
		entries = append(entries, windows.UTF16ToString(unsafe.Slice(p, n)))
		p = (*uint16)(unsafe.Add(unsafe.Pointer(p), uintptr(n+1)*2))
	}
	return entries
}

// buildEnvironmentBlock encodes "KEY=VALUE" entries into a double-null-terminated
// UTF-16 environment block.
func buildEnvironmentBlock(entries []string) ([]uint16, error) {
	var block []uint16
	for _, entry := range entries {
		encoded, err := windows.UTF16FromString(entry)
		if err != nil {
			return nil, fmt.Errorf("encode environment entry: %w", err)
		}
		block = append(block, encoded...)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

// assignProcessToKillJob puts the server in a job that kills it when the job
// handle closes, so an abnormal supervisor exit terminates the server with it
// instead of orphaning it.
func assignProcessToKillJob(process windows.Handle) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configure kill-on-close job: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("assign server to job: %w", err)
	}
	return job, nil
}

func openInheritableAppendFile(path string) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	sa := windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(sa))
	handle, err := windows.CreateFile(
		namePtr,
		windows.FILE_APPEND_DATA,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&sa,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("open log file %s: %w", path, err)
	}
	return handle, nil
}
