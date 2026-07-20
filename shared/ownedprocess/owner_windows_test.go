//go:build windows

package ownedprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"core/internal/testharness/testsetup"
)

const (
	windowsHelperModeEnvironment = "KENT_OWNED_PROCESS_WINDOWS_HELPER_MODE"
	windowsHelperIdentityPathEnv = "KENT_OWNED_PROCESS_WINDOWS_IDENTITY_PATH"
	windowsHelperMarkerEnv       = "KENT_OWNED_PROCESS_WINDOWS_MARKER"
	windowsHelperRootHolding     = "root_holding"
	windowsHelperRootNoInput     = "root_holding_without_input"
	windowsHelperRootExits       = "root_exits"
	windowsHelperDescendant      = "descendant"
)

type windowsProcessIdentity struct {
	RootPID       int    `json:"root_pid"`
	DescendantPID int    `json:"descendant_pid"`
	Cwd           string `json:"cwd"`
	Environment   string `json:"environment"`
	Stdin         string `json:"stdin"`
}

func TestWindowsTerminateRemovesRootAndDescendant(t *testing.T) {
	owner, identity, _, _ := launchWindowsProcessTreeHelper(t, windowsHelperRootHolding)

	if err := owner.Terminate(); err != nil {
		t.Fatalf("terminate owned process tree: %v", err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("wait after termination unexpectedly succeeded")
	}
	assertWindowsProcessGoneEventually(t, identity.RootPID)
	assertWindowsProcessGoneEventually(t, identity.DescendantPID)
	if err := owner.Close(); err != nil {
		t.Fatalf("close terminated process tree: %v", err)
	}
}

func TestWindowsCloseRemovesDescendantAfterRootExitAndReleasesHandles(t *testing.T) {
	owner, identity, _, _ := launchWindowsProcessTreeHelper(t, windowsHelperRootExits)

	if err := owner.Wait(); err != nil {
		t.Fatalf("wait for root exit: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owned process tree: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("second close owned process tree: %v", err)
	}
	assertWindowsProcessGoneEventually(t, identity.RootPID)
	assertWindowsProcessGoneEventually(t, identity.DescendantPID)

	tree, ok := owner.process.(*windowsProcessTree)
	if !ok {
		t.Fatalf("owner process = %T, want Windows process tree", owner.process)
	}
	tree.handleMu.Lock()
	defer tree.handleMu.Unlock()
	if tree.process != 0 || tree.thread != 0 || tree.job != 0 {
		t.Fatalf("owner retained Windows handles after Close: process=%#x thread=%#x job=%#x", tree.process, tree.thread, tree.job)
	}
}

func TestWindowsOwnerBridgesCallerStdio(t *testing.T) {
	owner, _, stdout, stderr := launchWindowsProcessTreeHelper(t, windowsHelperRootExits)
	if err := owner.Wait(); err != nil {
		t.Fatalf("wait for root exit: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owned process tree: %v", err)
	}
	if got := stdout.String(); got != "stdout marker\n" {
		t.Fatalf("stdout = %q, want marker", got)
	}
	if got := stderr.String(); got != "stderr marker\n" {
		t.Fatalf("stderr = %q, want marker", got)
	}
}

func TestWindowsCloseJoinsBlockingInputBridge(t *testing.T) {
	reader := newBlockingReader()
	owner, identity, _, _ := launchWindowsProcessTreeHelperWithStdio(t, windowsHelperRootNoInput, reader, new(bytes.Buffer), new(bytes.Buffer))
	<-reader.started

	if err := owner.Terminate(); err != nil {
		t.Fatalf("terminate owned process tree: %v", err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("wait after termination unexpectedly succeeded")
	}
	assertWindowsProcessGoneEventually(t, identity.RootPID)

	closed := make(chan error, 1)
	go func() {
		closed <- owner.Close()
	}()
	select {
	case err := <-closed:
		t.Fatalf("close returned before the blocking input bridge was released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	reader.release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close owned process tree: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not join the released input bridge")
	}
}

func TestWindowsCloseSurfacesFailingOutputBridge(t *testing.T) {
	writer := newBlockingFailingWriter(errors.New("writer failed"))
	owner, _, _, _ := launchWindowsProcessTreeHelperWithStdio(t, windowsHelperRootExits, strings.NewReader("caller stdin"), writer, new(bytes.Buffer))
	<-writer.started
	if err := owner.Wait(); err != nil {
		t.Fatalf("wait for root exit: %v", err)
	}

	closed := make(chan error, 1)
	go func() {
		closed <- owner.Close()
	}()
	select {
	case err := <-closed:
		t.Fatalf("close returned before the failing output bridge was released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	writer.release()
	select {
	case err := <-closed:
		if !errors.Is(err, writer.err) {
			t.Fatalf("close error = %v, want writer error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not join the failing output bridge")
	}
}

func TestWindowsEnvironmentBlockEncodesEmptyAndValidatesEntries(t *testing.T) {
	block, err := windowsEnvironmentBlock([]string{})
	if err != nil {
		t.Fatalf("encode empty environment: %v", err)
	}
	if want := []uint16{0, 0}; !reflect.DeepEqual(block, want) {
		t.Fatalf("empty environment block = %#v, want %#v", block, want)
	}
	block, err = windowsEnvironmentBlock([]string{"Path=first", "PATH=second", "", "Z=last"})
	if err != nil {
		t.Fatalf("encode environment: %v", err)
	}
	if got := string(utf16.Decode(block)); got != "PATH=second\x00Z=last\x00\x00" {
		t.Fatalf("environment block = %q", got)
	}
	if _, err := windowsEnvironmentBlock([]string{"A=1\x00B=2"}); err == nil {
		t.Fatal("environment containing NUL unexpectedly encoded")
	}
}

func TestWindowsLaunchSupportsExplicitEmptyEnvironment(t *testing.T) {
	var stdout bytes.Buffer
	owner, err := Launch(LaunchRequest{
		Argv:   []string{os.Args[0], "-test.run=TestWindowsEmptyEnvironmentHelper"},
		Env:    []string{},
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("launch with empty environment: %v", err)
	}
	if err := owner.Wait(); err != nil {
		t.Fatalf("wait for empty-environment helper: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close empty-environment helper: %v", err)
	}
	if got := stdout.String(); got != "empty environment\n" {
		t.Fatalf("empty-environment stdout = %q", got)
	}
}

func TestWindowsEmptyEnvironmentHelper(t *testing.T) {
	if len(os.Environ()) != 0 {
		return
	}
	if _, err := fmt.Fprintln(os.Stdout, "empty environment"); err != nil {
		t.Fatalf("write empty-environment marker: %v", err)
	}
	os.Exit(0)
}

func TestWindowsCloseAndTerminateAreRaceSafe(t *testing.T) {
	owner, identity, _, _ := launchWindowsProcessTreeHelper(t, windowsHelperRootHolding)
	var callers sync.WaitGroup
	errors := make(chan error, 24)
	for index := 0; index < 12; index++ {
		callers.Add(2)
		go func() {
			defer callers.Done()
			errors <- owner.Terminate()
		}()
		go func() {
			defer callers.Done()
			errors <- owner.Close()
		}()
	}
	callers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent owner shutdown: %v", err)
		}
	}
	assertWindowsProcessGoneEventually(t, identity.RootPID)
	assertWindowsProcessGoneEventually(t, identity.DescendantPID)
}

func launchWindowsProcessTreeHelper(t *testing.T, mode string) (*Owner, windowsProcessIdentity, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	return launchWindowsProcessTreeHelperWithStdio(t, mode, strings.NewReader("caller stdin"), stdout, stderr)
}

func launchWindowsProcessTreeHelperWithStdio(t *testing.T, mode string, stdin io.Reader, stdout, stderr io.Writer) (*Owner, windowsProcessIdentity, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	cwd := t.TempDir()
	owner, err := Launch(LaunchRequest{
		Argv: []string{os.Args[0], "-test.run=TestWindowsOwnedProcessHelper"},
		Cwd:  &cwd,
		Env: append(os.Environ(),
			windowsHelperModeEnvironment+"="+mode,
			windowsHelperIdentityPathEnv+"="+identityPath,
			windowsHelperMarkerEnv+"=marker",
		),
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		t.Fatalf("launch owned process tree: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close owned process tree: %v", err)
		}
	})
	identity := waitForWindowsProcessIdentity(t, identityPath)
	if identity.Cwd != cwd {
		t.Fatalf("helper cwd = %q, want %q", identity.Cwd, cwd)
	}
	if identity.Environment != "marker" {
		t.Fatalf("helper environment = %q, want marker", identity.Environment)
	}
	if mode != windowsHelperRootNoInput && identity.Stdin != "caller stdin" {
		t.Fatalf("helper stdin = %q, want caller stdin", identity.Stdin)
	}
	stdoutBuffer, ok := stdout.(*bytes.Buffer)
	if !ok {
		stdoutBuffer = new(bytes.Buffer)
	}
	stderrBuffer, ok := stderr.(*bytes.Buffer)
	if !ok {
		stderrBuffer = new(bytes.Buffer)
	}
	return owner, identity, stdoutBuffer, stderrBuffer
}

func TestWindowsOwnedProcessHelper(t *testing.T) {
	mode := os.Getenv(windowsHelperModeEnvironment)
	if mode == "" {
		return
	}
	if mode == windowsHelperDescendant {
		select {}
	}
	child, err := os.StartProcess(os.Args[0], []string{os.Args[0], "-test.run=TestWindowsOwnedProcessHelper"}, &os.ProcAttr{
		Env: []string{windowsHelperModeEnvironment + "=" + windowsHelperDescendant},
	})
	if err != nil {
		t.Fatalf("start descendant: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get helper cwd: %v", err)
	}
	var stdin []byte
	if mode != windowsHelperRootNoInput {
		stdin, err = io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatalf("read helper stdin: %v", err)
		}
	}
	identity := windowsProcessIdentity{
		RootPID:       os.Getpid(),
		DescendantPID: child.Pid,
		Cwd:           cwd,
		Environment:   os.Getenv(windowsHelperMarkerEnv),
		Stdin:         string(stdin),
	}
	body, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal helper identity: %v", err)
	}
	identityPath := os.Getenv(windowsHelperIdentityPathEnv)
	temporaryPath := identityPath + ".tmp"
	if err := os.WriteFile(temporaryPath, body, 0o600); err != nil {
		t.Fatalf("write helper identity temp: %v", err)
	}
	if err := os.Rename(temporaryPath, identityPath); err != nil {
		t.Fatalf("publish helper identity: %v", err)
	}
	if _, err := os.Stdout.WriteString("stdout marker\n"); err != nil {
		t.Fatalf("write helper stdout: %v", err)
	}
	if _, err := os.Stderr.WriteString("stderr marker\n"); err != nil {
		t.Fatalf("write helper stderr: %v", err)
	}
	if mode == windowsHelperRootExits {
		os.Exit(0)
	}
	select {}
}

type blockingReader struct {
	started     chan struct{}
	releaseOnce sync.Once
	released    chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{started: make(chan struct{}), released: make(chan struct{})}
}

func (reader *blockingReader) Read([]byte) (int, error) {
	close(reader.started)
	<-reader.released
	return 0, io.EOF
}

func (reader *blockingReader) release() {
	reader.releaseOnce.Do(func() {
		close(reader.released)
	})
}

type blockingFailingWriter struct {
	err         error
	started     chan struct{}
	releaseOnce sync.Once
	released    chan struct{}
}

func newBlockingFailingWriter(err error) *blockingFailingWriter {
	return &blockingFailingWriter{err: err, started: make(chan struct{}), released: make(chan struct{})}
}

func (writer *blockingFailingWriter) Write([]byte) (int, error) {
	close(writer.started)
	<-writer.released
	return 0, writer.err
}

func (writer *blockingFailingWriter) release() {
	writer.releaseOnce.Do(func() {
		close(writer.released)
	})
}

func waitForWindowsProcessIdentity(t *testing.T, path string) windowsProcessIdentity {
	t.Helper()
	var identity windowsProcessIdentity
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		body, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(body, &identity); err != nil {
				t.Fatalf("decode helper identity: %v", err)
			}
			return identity.RootPID > 0 && identity.DescendantPID > 0
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper identity: %v", err)
		}
		return false
	}, "timed out waiting for Windows process tree identity")
	return identity
}

func assertWindowsProcessGoneEventually(t *testing.T, pid int) {
	t.Helper()
	testsetup.RequireProcessGone(t, time.Now().Add(5*time.Second), pid)
}
