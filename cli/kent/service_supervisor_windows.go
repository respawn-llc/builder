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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	supervisorInitialBackoff = 1 * time.Second
	supervisorMaxBackoff     = 30 * time.Second
	stillActiveExitCode      = 259

	supervisorStopGracePeriod = 15 * time.Second

	shutdownEventSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x00100000;;;IU)"
)

type serverSupervisor struct {
	spec      serviceSpec
	installer string
	wanted    atomic.Uint32
	wake      chan struct{}
	mu        sync.Mutex
	child     *managedChild
}

func newServerSupervisor(spec serviceSpec) *serverSupervisor {
	return &serverSupervisor{spec: spec, installer: readInstallUserSID(spec), wake: make(chan struct{}, 1)}
}

func (s *serverSupervisor) targetSession() uint32 {
	return activeUserSessionForSID(s.installer)
}

func (s *serverSupervisor) setWanted(session uint32) {
	s.wanted.Store(session)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

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

func activeUserSessionForSID(wantSID string) uint32 {
	var infos *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &infos, &count); err != nil {
		return 0
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(infos)))
	sessions := unsafe.Slice(infos, count)
	console := windows.WTSGetActiveConsoleSessionId()
	var consoleFallback uint32
	for i := range sessions {
		id := sessions[i].SessionID
		state := sessions[i].State
		if id == 0 {
			continue
		}
		if state != windows.WTSActive && state != windows.WTSDisconnected {
			continue
		}
		sid, err := sessionUserSID(id)
		if err != nil {
			continue
		}
		if wantSID != "" {
			if sid == wantSID {
				return id
			}
			continue
		}
		if state != windows.WTSActive {
			continue
		}
		if id == console {
			return id
		}
		if consoleFallback == 0 {
			consoleFallback = id
		}
	}
	if wantSID == "" {
		return consoleFallback
	}
	return 0
}

func sessionUserSID(session uint32) (string, error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

type managedChild struct {
	session  uint32
	pid      uint32
	process  windows.Handle
	token    windows.Token
	profile  windows.Handle
	shutdown windows.Handle
	job      windows.Handle
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

	if err := ensureServiceLogDir(spec); err != nil {
		return nil, err
	}

	env, err := userEnvironment(token, map[string]string{
		shutdownEventEnvVar: shutdownName,
		stdoutLogEnvVar:     spec.StdoutLogPath,
		stderrLogEnvVar:     spec.StderrLogPath,
	})
	if err != nil {
		return nil, err
	}

	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return nil, err
	}
	si := windows.StartupInfo{Desktop: desktop}
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
	err = windows.CreateProcessAsUser(token, appName, cmdLine, nil, nil, false, flags, &env[0], cwd, &si, &pi)
	runtime.KeepAlive(env)
	if err != nil {
		return nil, fmt.Errorf("launch server as user in session %d: %w", session, err)
	}
	_ = windows.CloseHandle(pi.Thread)

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

func userEnvironment(token windows.Token, extra map[string]string) ([]uint16, error) {
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err != nil {
		return nil, fmt.Errorf("create environment block: %w", err)
	}
	defer func() { _ = windows.DestroyEnvironmentBlock(block) }()
	entries := upsertEnvironmentEntries(splitEnvironmentBlock(block), extra)
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i]) < strings.ToLower(entries[j])
	})
	return buildEnvironmentBlock(entries)
}

func upsertEnvironmentEntries(entries []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return entries
	}
	replaced := make(map[string]struct{}, len(extra))
	for key := range extra {
		replaced[strings.ToLower(key)] = struct{}{}
	}
	result := make([]string, 0, len(entries)+len(extra))
	for _, entry := range entries {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		if _, dup := replaced[strings.ToLower(name)]; dup {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range extra {
		result = append(result, key+"="+value)
	}
	return result
}

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
