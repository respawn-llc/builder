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
	"strings"
	"sync"
	"time"

	"core/internal/testharness/pty/analyzer"
	"core/shared/client"
	"core/shared/serverapi"
)

const fixedWait = 500 * time.Millisecond
const readinessWait = 2 * time.Second
const cleanupWait = 10 * time.Second
const preflightWait = cleanupWait

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
	cmd     *exec.Cmd
	done    chan struct{}
	failure chan struct{}
	stdout  *boundedLog
	stderr  *boundedLog
	mu      sync.Mutex
	waitErr error
}

func NewIsolatedEnvironment(serverBinary string, operations []RequiredOperation) (*IsolatedEnvironment, error) {
	if err := requirePTYPlatform(); err != nil {
		return nil, err
	}
	if serverBinary == "" {
		return nil, errors.New("server binary is required")
	}
	root, err := os.MkdirTemp("", "kent-pty-blackbox-")
	if err != nil {
		return nil, fmt.Errorf("create isolated root: %w", err)
	}
	environment := &IsolatedEnvironment{Root: root}
	fail := func(cause error) (*IsolatedEnvironment, error) {
		return environment, fmt.Errorf("%w; run_root=%s", cause, root)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fail(fmt.Errorf("create isolated workspace: %w", err))
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), harnessConfigTemplate, 0o600); err != nil {
		return fail(fmt.Errorf("copy harness config template: %w", err))
	}
	stub, err := StartResponsesStub(operations)
	if err != nil {
		return fail(err)
	}
	environment.Workspace = workspace
	environment.Stub = stub
	host, port, err := reserveLoopbackPort()
	if err != nil {
		return fail(err)
	}
	server, err := startServer(serverBinary, root, host, port, stub.URL())
	if err != nil {
		return fail(err)
	}
	environment.Host = host
	environment.Port = port
	environment.Server = server
	return environment, nil
}

func (e *IsolatedEnvironment) ClientEnvironment() ([]string, error) {
	if e == nil {
		return nil, errors.New("isolated environment is required")
	}
	return clientEnvironment(filepath.Join(e.Root, "client-home"), e.Root, e.Host, e.Port)
}

func (e *IsolatedEnvironment) WaitReady() error {
	if e == nil || e.Server == nil || e.Server.cmd == nil || e.Server.cmd.Process == nil {
		return errors.New("isolated server is required")
	}
	deadline := time.Now().Add(readinessWait)
	url := "http://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) + "/readyz"
	for time.Now().Before(deadline) {
		probeContext, cancel := context.WithDeadline(context.Background(), deadline)
		request, requestErr := http.NewRequestWithContext(probeContext, http.MethodGet, url, nil)
		if requestErr != nil {
			cancel()
			return fmt.Errorf("create readiness request: %w", requestErr)
		}
		response, err := directHTTPClient.Do(request)
		if err == nil {
			var body struct {
				Ready bool `json:"ready"`
				PID   int  `json:"pid"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&body)
			closeErr := response.Body.Close()
			cancel()
			if decodeErr != nil {
				return fmt.Errorf("decode readiness: %w", decodeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close readiness response: %w", closeErr)
			}
			if body.PID != e.Server.cmd.Process.Pid {
				return fmt.Errorf("readiness PID mismatch: got=%d want=%d", body.PID, e.Server.cmd.Process.Pid)
			}
			if response.StatusCode == http.StatusOK && body.Ready {
				return nil
			}
		} else {
			cancel()
		}
		select {
		case <-e.Server.done:
			if err := e.Server.Error(); err != nil {
				return fmt.Errorf("standalone server exited before readiness: %w", err)
			}
			return fmt.Errorf("standalone server exited before readiness: stderr=%s", e.Server.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return fmt.Errorf("standalone server readiness timed out: stderr=%s", e.Server.stderr.String())
}

func (e *IsolatedEnvironment) BindProject() (returnErr error) {
	dialContext, cancelDial := context.WithTimeout(context.Background(), fixedWait)
	remote, err := client.DialRemoteURL(dialContext, "ws://"+net.JoinHostPort(e.Host, strconv.Itoa(e.Port))+"/rpc")
	cancelDial()
	if err != nil {
		return fmt.Errorf("dial standalone server project API: %w", err)
	}
	defer func() {
		if err := remote.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close standalone project API: %w", err)
		}
	}()
	acknowledgeContext, cancelAcknowledge := context.WithTimeout(context.Background(), fixedWait)
	err = remote.EnableNoAuthBootstrapAcknowledgement(acknowledgeContext)
	cancelAcknowledge()
	if err != nil {
		return fmt.Errorf("acknowledge standalone no-auth setup: %w", err)
	}
	createContext, cancelCreate := context.WithTimeout(context.Background(), fixedWait)
	created, err := remote.CreateProject(createContext, serverapi.ProjectCreateRequest{
		DisplayName:   "PTY Harness",
		WorkspaceRoot: e.Workspace,
	})
	cancelCreate()
	if err != nil {
		return fmt.Errorf("create isolated project: %w", err)
	}
	planContext, cancelPlan := context.WithTimeout(context.Background(), fixedWait)
	plan, err := remote.PlanWorkspaceBinding(planContext, serverapi.ProjectBindingPlanRequest{
		Path: e.Workspace,
		Mode: serverapi.ProjectBindingPlanModeInteractive,
	})
	cancelPlan()
	if err != nil {
		return fmt.Errorf("verify isolated project binding: %w", err)
	}
	if plan.Kind != serverapi.ProjectBindingPlanKindBound || plan.Binding == nil || plan.Binding.WorkspaceID != created.Binding.WorkspaceID {
		return fmt.Errorf("isolated project binding is not bound: kind=%s", plan.Kind)
	}
	return nil
}

func (s *ServerHandle) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Failure becomes ready exactly once when bounded server evidence reaches a
// terminal error. It remains ready so a quiet action loop cannot miss it.
func (s *ServerHandle) Failure() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.failure
}

func (s *ServerHandle) Error() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waitErr != nil {
		return s.waitErr
	}
	if s.stdout != nil && s.stdout.Error() != nil {
		return s.stdout.Error()
	}
	if s.stderr != nil && s.stderr.Error() != nil {
		return s.stderr.Error()
	}
	return nil
}

func (s *ServerHandle) Terminate() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return terminateServerProcessGroup(s.cmd)
}

func (s *ServerHandle) ForceKill() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return killServerProcessGroup(s.cmd)
}

func startServer(binary string, root string, host string, port int, stubURL string) (*ServerHandle, error) {
	preflightContext, cancelPreflight := context.WithTimeout(context.Background(), preflightWait)
	preflightOutput, preflightErr := exec.CommandContext(preflightContext, binary, "--version").CombinedOutput()
	cancelPreflight()
	if preflightErr != nil {
		return nil, fmt.Errorf("preflight standalone server binary: %w: %s", preflightErr, string(preflightOutput))
	}
	cmd := exec.Command(binary, "serve", "--persistence-root", root)
	environment, err := serverEnvironment(filepath.Join(root, "server-home"), root, host, port, stubURL)
	if err != nil {
		return nil, err
	}
	cmd.Env = environment
	if err := configureServerProcessGroup(cmd); err != nil {
		return nil, err
	}
	failure := make(chan struct{})
	var signalFailure sync.Once
	notifyFailure := func() {
		signalFailure.Do(func() {
			close(failure)
		})
	}
	stdout := newBoundedLog(512*1024, notifyFailure)
	stderr := newBoundedLog(512*1024, notifyFailure)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start standalone server: %w", err)
	}
	handle := &ServerHandle{cmd: cmd, done: make(chan struct{}), failure: failure, stdout: stdout, stderr: stderr}
	go func() {
		waitErr := cmd.Wait()
		handle.mu.Lock()
		handle.waitErr = waitErr
		handle.mu.Unlock()
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

func clientEnvironment(home string, root string, host string, port int) ([]string, error) {
	environment, err := baseEnvironment(home, root, host, port)
	if err != nil {
		return nil, err
	}
	return appendEnvironment(environment,
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	)
}

func serverEnvironment(home string, root string, host string, port int, modelURL string) ([]string, error) {
	environment, err := baseEnvironment(home, root, host, port)
	if err != nil {
		return nil, err
	}
	return appendEnvironment(environment,
		"KENT_OPENAI_BASE_URL="+modelURL,
		"GOMAXPROCS=1",
	)
}

func baseEnvironment(home string, root string, host string, port int) ([]string, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("create process home: %w", err)
	}
	runtime := filepath.Join(home, "runtime")
	temporary := filepath.Join(home, "tmp")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		return nil, fmt.Errorf("create process runtime directory: %w", err)
	}
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return nil, fmt.Errorf("create process temporary directory: %w", err)
	}
	return appendEnvironment(nil,
		"HOME="+home,
		"TMPDIR="+temporary,
		"XDG_RUNTIME_DIR="+runtime,
		"PATH="+os.Getenv("PATH"),
		"SHELL=/bin/sh",
		"TZ=UTC",
		"KENT_PERSISTENCE_ROOT="+root,
		"KENT_SERVER_HOST="+host,
		"KENT_SERVER_PORT="+strconv.Itoa(port),
	)
}

func appendEnvironment(environment []string, entries ...string) ([]string, error) {
	keys := make(map[string]struct{}, len(environment)+len(entries))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("invalid environment entry %q", entry)
		}
		keys[key] = struct{}{}
	}
	for _, entry := range entries {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("invalid environment entry %q", entry)
		}
		if _, exists := keys[key]; exists {
			return nil, fmt.Errorf("duplicate environment key %q", key)
		}
		keys[key] = struct{}{}
		environment = append(environment, entry)
	}
	return environment, nil
}

type boundedLog struct {
	limit      int
	mu         sync.Mutex
	data       []byte
	tail       []byte
	err        error
	onOverflow func()
}

func newBoundedLog(limit int, onOverflow ...func()) *boundedLog {
	var signal func()
	if len(onOverflow) > 0 {
		signal = onOverflow[0]
	}
	return &boundedLog{limit: limit, data: make([]byte, 0, limit), onOverflow: signal}
}

func (b *boundedLog) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		b.data = append(b.data, payload[:min(remaining, len(payload))]...)
	}
	if len(payload) <= remaining {
		return len(payload), nil
	}
	if b.err == nil {
		b.tail = appendTail(b.tail, payload, 32*1024)
		b.err = &analyzer.EvidenceLimitExceeded{
			Source:   analyzer.EvidenceSourceArtifacts,
			Detail:   "server log",
			Limit:    b.limit,
			Observed: len(b.data) + len(payload) - remaining,
			Prefix:   bytes.Clone(b.data[:min(len(b.data), 32*1024)]),
			Tail:     bytes.Clone(b.tail),
		}
		if b.onOverflow != nil {
			b.onOverflow()
		}
	}
	return 0, b.err
}

func (b *boundedLog) String() string {
	return string(b.Bytes())
}

func (b *boundedLog) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data)
}

func (b *boundedLog) Error() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func appendTail(tail []byte, payload []byte, limit int) []byte {
	tail = append(tail, payload...)
	if len(tail) > limit {
		tail = append([]byte(nil), tail[len(tail)-limit:]...)
	}
	return tail
}
