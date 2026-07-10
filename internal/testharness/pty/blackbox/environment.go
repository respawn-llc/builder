package blackbox

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"core/shared/client"
	"core/shared/serverapi"
)

const fixedWait = 500 * time.Millisecond

var directHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: nil},
}

//go:embed testdata/config.toml
var harnessConfigTemplate []byte

type IsolatedEnvironment struct {
	Root      string
	Workspace string
	Host      string
	Port      int
	Stub      *ResponsesStub
	Server    *ServerHandle
}

type ServerHandle struct {
	cmd    *exec.Cmd
	done   chan struct{}
	stdout *boundedLog
	stderr *boundedLog
}

func NewIsolatedEnvironment(serverBinary string, operations []RequiredOperation) (*IsolatedEnvironment, error) {
	if serverBinary == "" {
		return nil, errors.New("server binary is required")
	}
	root, err := os.MkdirTemp("", "kent-pty-blackbox-")
	if err != nil {
		return nil, fmt.Errorf("create isolated root: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("create isolated workspace: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), harnessConfigTemplate, 0o600); err != nil {
		return nil, fmt.Errorf("copy harness config template: %w", err)
	}
	stub, err := StartResponsesStub(operations)
	if err != nil {
		return nil, err
	}
	host, port, err := reserveLoopbackPort()
	if err != nil {
		stub.Close()
		return nil, err
	}
	server, err := startServer(serverBinary, root, host, port, stub.URL())
	if err != nil {
		stub.Close()
		return nil, err
	}
	environment := &IsolatedEnvironment{
		Root: root, Workspace: workspace, Host: host, Port: port, Stub: stub, Server: server,
	}
	if err := environment.WaitReady(); err != nil {
		environment.Close()
		return nil, fmt.Errorf("%w; run_root=%s", err, root)
	}
	if err := environment.BindProject(); err != nil {
		environment.Close()
		return nil, fmt.Errorf("%w; run_root=%s", err, root)
	}
	cleanup = false
	return environment, nil
}

func (e *IsolatedEnvironment) ClientEnvironment() []string {
	if e == nil {
		return nil
	}
	return exactEnvironment(filepath.Join(e.Root, "client-home"), e.Root, e.Host, e.Port, "")
}

func (e *IsolatedEnvironment) WaitReady() error {
	if e == nil || e.Server == nil || e.Server.cmd == nil || e.Server.cmd.Process == nil {
		return errors.New("isolated server is required")
	}
	deadline := time.Now().Add(fixedWait)
	url := "http://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) + "/readyz"
	for time.Now().Before(deadline) {
		response, err := directHTTPClient.Get(url)
		if err == nil {
			var body struct {
				Ready bool `json:"ready"`
				PID   int  `json:"pid"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&body)
			_ = response.Body.Close()
			if decodeErr != nil {
				return fmt.Errorf("decode readiness: %w", decodeErr)
			}
			if body.PID != e.Server.cmd.Process.Pid {
				return fmt.Errorf("readiness PID mismatch: got=%d want=%d", body.PID, e.Server.cmd.Process.Pid)
			}
			if response.StatusCode == http.StatusOK && body.Ready {
				return nil
			}
		}
		select {
		case <-e.Server.done:
			return fmt.Errorf("standalone server exited before readiness: stderr=%s", e.Server.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return fmt.Errorf("standalone server readiness timed out: stderr=%s", e.Server.stderr.String())
}

func (e *IsolatedEnvironment) BindProject() error {
	ctx, cancel := context.WithTimeout(context.Background(), fixedWait)
	defer cancel()
	remote, err := client.DialRemoteURL(ctx, "ws://"+net.JoinHostPort(e.Host, strconv.Itoa(e.Port))+"/rpc")
	if err != nil {
		return fmt.Errorf("dial standalone server project API: %w", err)
	}
	defer remote.Close()
	if err := remote.EnableNoAuthBootstrapAcknowledgement(ctx); err != nil {
		return fmt.Errorf("acknowledge standalone no-auth setup: %w", err)
	}
	created, err := remote.CreateProject(ctx, serverapi.ProjectCreateRequest{
		DisplayName:   "PTY Harness",
		WorkspaceRoot: e.Workspace,
	})
	if err != nil {
		return fmt.Errorf("create isolated project: %w", err)
	}
	plan, err := remote.PlanWorkspaceBinding(ctx, serverapi.ProjectBindingPlanRequest{
		Path: e.Workspace,
		Mode: serverapi.ProjectBindingPlanModeInteractive,
	})
	if err != nil {
		return fmt.Errorf("verify isolated project binding: %w", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindBound || plan.Binding == nil || plan.Binding.WorkspaceID != created.Binding.WorkspaceID {
		return fmt.Errorf("isolated project binding is not bound: kind=%s", plan.Kind)
	}
	return nil
}

func (e *IsolatedEnvironment) Close() {
	if e == nil {
		return
	}
	if e.Stub != nil {
		e.Stub.Close()
	}
	if e.Server != nil {
		e.Server.Terminate()
	}
}

func (s *ServerHandle) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

func (s *ServerHandle) Terminate() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
}

func (s *ServerHandle) ForceKill() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
}

func startServer(binary string, root string, host string, port int, stubURL string) (*ServerHandle, error) {
	cmd := exec.Command(binary, "serve", "--persistence-root", root)
	cmd.Env = exactEnvironment(filepath.Join(root, "server-home"), root, host, port, stubURL)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := newBoundedLog(512 * 1024)
	stderr := newBoundedLog(512 * 1024)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start standalone server: %w", err)
	}
	handle := &ServerHandle{cmd: cmd, done: make(chan struct{}), stdout: stdout, stderr: stderr}
	go func() {
		_ = cmd.Wait()
		close(handle.done)
	}()
	return handle, nil
}

func reserveLoopbackPort() (string, int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, fmt.Errorf("reserve loopback port: %w", err)
	}
	address := listener.Addr().(*net.TCPAddr)
	err = listener.Close()
	if err != nil {
		return "", 0, err
	}
	return "127.0.0.1", address.Port, nil
}

func exactEnvironment(home string, root string, host string, port int, modelURL string) []string {
	_ = os.MkdirAll(home, 0o700)
	runtime := filepath.Join(home, "runtime")
	temporary := filepath.Join(home, "tmp")
	_ = os.MkdirAll(runtime, 0o700)
	_ = os.MkdirAll(temporary, 0o700)
	environment := []string{
		"HOME=" + home,
		"TMPDIR=" + temporary,
		"XDG_RUNTIME_DIR=" + runtime,
		"PATH=" + os.Getenv("PATH"),
		"SHELL=/bin/sh",
		"TZ=UTC",
		"KENT_PERSISTENCE_ROOT=" + root,
		"KENT_SERVER_HOST=" + host,
		"KENT_SERVER_PORT=" + strconv.Itoa(port),
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	if modelURL != "" {
		environment = append(environment, "KENT_OPENAI_BASE_URL="+modelURL)
	}
	return environment
}

type boundedLog struct {
	limit int
	mu    sync.Mutex
	data  []byte
}

func newBoundedLog(limit int) *boundedLog {
	return &boundedLog{limit: limit, data: make([]byte, 0, limit)}
}

func (b *boundedLog) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		b.data = append(b.data, payload[:min(remaining, len(payload))]...)
	}
	return len(payload), nil
}

func (b *boundedLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.data))
}
