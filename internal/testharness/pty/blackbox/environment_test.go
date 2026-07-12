//go:build !windows

package blackbox

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/driver"

	"github.com/google/uuid"
)

func TestProcessEnvironmentsAreSeparateAndFallible(t *testing.T) {
	t.Setenv("KENT_OPENAI_BASE_URL", "http://ambient.invalid")
	t.Setenv("OPENAI_API_KEY", "ambient")
	t.Setenv("HTTPS_PROXY", "http://ambient.invalid")
	t.Setenv("COLORTERM", "truecolor")
	root := t.TempDir()
	client, err := clientEnvironment(filepath.Join(root, "client"), root, "127.0.0.1", 7777)
	if err != nil {
		t.Fatalf("clientEnvironment: %v", err)
	}
	server, err := serverEnvironment(filepath.Join(root, "server"), root, "127.0.0.1", 7777, "http://127.0.0.1:9999/v1")
	if err != nil {
		t.Fatalf("serverEnvironment: %v", err)
	}
	clientValues := environmentValues(t, client)
	serverValues := environmentValues(t, server)
	if clientValues["TERM"] != "xterm-256color" || clientValues["LANG"] != "C.UTF-8" || clientValues["LC_ALL"] != "C.UTF-8" {
		t.Fatalf("client terminal environment = %#v", clientValues)
	}
	if _, exists := serverValues["TERM"]; exists {
		t.Fatalf("server inherited client terminal environment: %#v", serverValues)
	}
	if _, exists := clientValues["KENT_OPENAI_BASE_URL"]; exists {
		t.Fatalf("client received server-only model endpoint: %#v", clientValues)
	}
	if serverValues["KENT_OPENAI_BASE_URL"] != "http://127.0.0.1:9999/v1" {
		t.Fatalf("server model endpoint = %q", serverValues["KENT_OPENAI_BASE_URL"])
	}
	if serverValues["GOMAXPROCS"] != "1" {
		t.Fatalf("server scheduler limit = %q, want 1", serverValues["GOMAXPROCS"])
	}
	if _, err := os.Stat(clientValues["HOME"]); err != nil {
		t.Fatalf("client HOME was not created: %v", err)
	}
	for _, values := range []map[string]string{clientValues, serverValues} {
		for _, forbidden := range []string{"OPENAI_API_KEY", "HTTPS_PROXY", "COLORTERM"} {
			if _, exists := values[forbidden]; exists {
				t.Fatalf("controlled process inherited %s: %#v", forbidden, values)
			}
		}
	}
	if _, exists := clientValues["COLORTERM"]; exists {
		t.Fatalf("client contains forbidden COLORTERM: %#v", clientValues)
	}
}

func TestEnvironmentBuilderRejectsDuplicateKeysAndUncreatableHome(t *testing.T) {
	if _, err := appendEnvironment([]string{"A=1"}, "A=2"); err == nil {
		t.Fatal("appendEnvironment accepted duplicate key")
	}
	root := t.TempDir()
	home := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(home, []byte("file"), 0o600); err != nil {
		t.Fatalf("create home file: %v", err)
	}
	if _, err := clientEnvironment(home, root, "127.0.0.1", 7777); err == nil {
		t.Fatal("clientEnvironment accepted a file as HOME")
	}
}

func TestWaitReadyRejectsForeignPIDAndExitedServer(t *testing.T) {
	foreign := httptestReadyServer(t, func(writer http.ResponseWriter) {
		_, _ = writer.Write([]byte(`{"ready":true,"pid":1}`))
	})
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "sleep 5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	environment := &IsolatedEnvironment{
		Host: foreign.host, Port: foreign.port,
		Server: &ServerHandle{cmd: cmd, done: make(chan struct{}), stderr: newBoundedLog(1024)},
	}
	if err := environment.WaitReady(); err == nil {
		t.Fatal("WaitReady accepted readiness from a foreign PID")
	}

	exit := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	if err := exit.Start(); err != nil {
		t.Fatalf("start exiting process: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = exit.Wait()
		close(done)
	}()
	environment = &IsolatedEnvironment{
		Host: "127.0.0.1", Port: 1,
		Server: &ServerHandle{cmd: exit, done: done, stderr: newBoundedLog(1024)},
	}
	started := time.Now()
	if err := environment.WaitReady(); err == nil {
		t.Fatal("WaitReady accepted an exited server")
	}
	if time.Since(started) > readinessWait {
		t.Fatal("WaitReady did not fail within its readiness deadline")
	}
}

func TestServerLogOverflowIsTypedAndRetainsDiagnosticExcerpts(t *testing.T) {
	stream := newBoundedLog(16)
	if _, err := stream.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatalf("fill bounded log: %v", err)
	}
	if _, err := stream.Write([]byte("overflow")); err == nil {
		t.Fatal("bounded log accepted evidence overflow")
	}
	var overflow *analyzer.EvidenceLimitExceeded
	if !errors.As(stream.Error(), &overflow) {
		t.Fatalf("log error = %T %v, want EvidenceLimitExceeded", stream.Error(), stream.Error())
	}
	if overflow.Limit != 16 || overflow.Observed != 24 || len(overflow.Prefix) == 0 || len(overflow.Tail) == 0 {
		t.Fatalf("overflow diagnostic = %+v", overflow)
	}
}

func TestServerFailureSignalWakesAQuietActionLoopOnLogOverflow(t *testing.T) {
	stub, err := StartResponsesStub([]RequiredOperation{{
		ID: uuid.New(), Route: RouteResponses, Outcome: OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)
	failure := make(chan struct{})
	var notifyOnce sync.Once
	notify := func() {
		notifyOnce.Do(func() {
			close(failure)
		})
	}
	environment := &IsolatedEnvironment{
		Stub: stub,
		Server: &ServerHandle{
			done:    make(chan struct{}),
			failure: failure,
			stdout:  newBoundedLog(16, notify),
			stderr:  newBoundedLog(16, notify),
		},
	}
	session, err := driver.StartSession(driver.SessionSpec{
		Path:       "/bin/sh",
		Args:       []string{"-c", "sleep 10"},
		Env:        []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Dimensions: analyzer.MustDimensions(2, 8),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = session.Terminate()
		_ = session.ForceKill()
	})
	result := make(chan error, 1)
	go func() {
		_, runErr := runActions(session, environment, []Action{{
			ID: uuid.New(), Kind: ActionWait, Predicate: &Predicate{Kind: PredicateModelConsumed},
		}})
		result <- runErr
	}()
	if _, err := environment.Server.stderr.Write([]byte("0123456789abcdefx")); err == nil {
		t.Fatal("server log overflow did not fail")
	}
	select {
	case runErr := <-result:
		var overflow *analyzer.EvidenceLimitExceeded
		if !errors.As(runErr, &overflow) {
			t.Fatalf("runActions error = %T %v, want EvidenceLimitExceeded", runErr, runErr)
		}
	case <-time.After(cleanupRetryWait):
		t.Fatal("quiet action loop did not wake for server log overflow")
	}
}

type readyServer struct {
	host string
	port int
}

func httptestReadyServer(t *testing.T, serve func(http.ResponseWriter)) readyServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen readiness server: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { serve(writer) })}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	address := listener.Addr().(*net.TCPAddr)
	return readyServer{host: address.IP.String(), port: address.Port}
}

func environmentValues(t *testing.T, environment []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("malformed environment entry %q", entry)
		}
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate environment key %q", key)
		}
		values[key] = value
	}
	return values
}
