package lifecyclehook_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/lifecyclehook"
	"core/shared/lifecyclecontract"
)

const dispatcherProcessModeEnv = "KENT_TEST_LIFECYCLE_DISPATCHER_MODE"

func TestDispatcherInheritsProcessContextAndForwardsPayload(t *testing.T) {
	root := t.TempDir()
	recordPath := filepath.Join(root, "record.json")
	t.Chdir(root)
	t.Setenv(dispatcherProcessModeEnv, "inspect")
	t.Setenv("KENT_TEST_LIFECYCLE_INHERITED", "present")
	t.Setenv("KENT_TEST_LIFECYCLE_RECORD", recordPath)
	dispatcher := lifecyclehook.New(context.Background(), dispatcherProcessCommand())
	t.Cleanup(dispatcher.Close)

	dispatcher.Submit(testLifecycleEvent())
	record := waitForDispatcherRecord(t, recordPath)
	if record.Workdir != root || record.Inherited != "present" {
		t.Fatalf("process context = %+v, want cwd %q and inherited env", record, root)
	}
	if record.Event.Category != lifecyclecontract.CategorySessionStart {
		t.Fatalf("payload category = %q", record.Event.Category)
	}
}

func TestDispatcherSurfacesLaunchAndBoundedStderrFailures(t *testing.T) {
	t.Run("launch failure", func(t *testing.T) {
		dispatcher := lifecyclehook.New(t.Context(), []string{filepath.Join(t.TempDir(), "missing-hook")})
		t.Cleanup(dispatcher.Close)
		dispatcher.Submit(testLifecycleEvent())
		issue := waitForDispatcherIssue(t, dispatcher.Issues(), 3*time.Second)
		if issue.Err == nil || issue.Category != lifecyclecontract.CategorySessionStart {
			t.Fatalf("issue = %+v", issue)
		}
	})

	t.Run("nonzero stderr is bounded", func(t *testing.T) {
		t.Setenv(dispatcherProcessModeEnv, "fail")
		dispatcher := lifecyclehook.New(t.Context(), dispatcherProcessCommand())
		t.Cleanup(dispatcher.Close)
		dispatcher.Submit(testLifecycleEvent())
		issue := waitForDispatcherIssue(t, dispatcher.Issues(), 3*time.Second)
		if issue.Err == nil {
			t.Fatal("missing nonzero exit error")
		}
		if len(issue.Stderr) > 4*1024 {
			t.Fatalf("stderr length = %d, want at most 4096", len(issue.Stderr))
		}
		if issue.Stderr == "" {
			t.Fatal("bounded stderr omitted hook diagnostics")
		}
	})
}

func TestDispatcherOverlapsHooksAndSilentlyCapsActiveProcesses(t *testing.T) {
	root := t.TempDir()
	startDir := filepath.Join(root, "started")
	releasePath := filepath.Join(root, "release")
	t.Setenv(dispatcherProcessModeEnv, "block")
	t.Setenv("KENT_TEST_LIFECYCLE_START_DIR", startDir)
	t.Setenv("KENT_TEST_LIFECYCLE_RELEASE", releasePath)
	dispatcher := lifecyclehook.New(t.Context(), dispatcherProcessCommand())
	t.Cleanup(dispatcher.Close)

	startedAt := time.Now()
	for range 256 {
		dispatcher.Submit(testLifecycleEvent())
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("burst submission blocked for %s", elapsed)
	}
	waitForDispatcherProcessCount(t, startDir, 64)
	time.Sleep(100 * time.Millisecond)
	if got := dispatcherProcessCount(t, startDir); got != 64 {
		t.Fatalf("active hook processes = %d, want cap 64", got)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release hooks: %v", err)
	}
}

func TestDispatcherFailurePresentationDoesNotHoldActiveProcessSlots(t *testing.T) {
	root := t.TempDir()
	startDir := filepath.Join(root, "started")
	t.Setenv(dispatcherProcessModeEnv, "fail")
	t.Setenv("KENT_TEST_LIFECYCLE_START_DIR", startDir)
	dispatcher := lifecyclehook.New(t.Context(), dispatcherProcessCommand())
	t.Cleanup(dispatcher.Close)

	for range 64 {
		dispatcher.Submit(testLifecycleEvent())
	}
	waitForDispatcherProcessCount(t, startDir, 64)
	for range 64 {
		dispatcher.Submit(testLifecycleEvent())
	}
	waitForDispatcherProcessCount(t, startDir, 128)
	waitForDispatcherProcessLaunchAfterSaturation(t, dispatcher, startDir, 129)

	totalFailures := 0
	deadline := time.Now().Add(5 * time.Second)
	for totalFailures < 129 && time.Now().Before(deadline) {
		issue := waitForDispatcherIssue(t, dispatcher.Issues(), 3*time.Second)
		if issue.Err == nil {
			t.Fatalf("issue omitted failure: %+v", issue)
		}
		totalFailures += issue.Count
	}
	if totalFailures != 129 {
		t.Fatalf("reported failure count = %d, want 129", totalFailures)
	}
}

func TestDispatcherTimesOutHooks(t *testing.T) {
	t.Setenv(dispatcherProcessModeEnv, "block")
	t.Setenv("KENT_TEST_LIFECYCLE_START_DIR", filepath.Join(t.TempDir(), "started"))
	dispatcher := lifecyclehook.New(context.Background(), dispatcherProcessCommand())
	t.Cleanup(dispatcher.Close)
	dispatcher.Submit(testLifecycleEvent())

	issue := waitForDispatcherIssue(t, dispatcher.Issues(), 35*time.Second)
	if issue.Err == nil {
		t.Fatal("timeout omitted failure")
	}
}

func TestDispatcherCloseReturnsWithoutWaitingForHookProcess(t *testing.T) {
	root := t.TempDir()
	startDir := filepath.Join(root, "started")
	t.Setenv(dispatcherProcessModeEnv, "block")
	t.Setenv("KENT_TEST_LIFECYCLE_START_DIR", startDir)
	dispatcher := lifecyclehook.New(t.Context(), dispatcherProcessCommand())
	dispatcher.Submit(testLifecycleEvent())
	waitForDispatcherProcessCount(t, startDir, 1)

	startedAt := time.Now()
	dispatcher.Close()
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("Close waited %s", elapsed)
	}
}

func TestLifecycleHookDispatcherProcess(t *testing.T) {
	mode := os.Getenv(dispatcherProcessModeEnv)
	if mode == "" {
		return
	}
	var event lifecyclecontract.Event
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		os.Exit(2)
	}
	recordDispatcherProcessStart()
	switch mode {
	case "inspect":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("ignored stdout", 64*1024))
		record := dispatcherProcessRecord{
			Workdir:   mustDispatcherProcessWorkdir(),
			Inherited: os.Getenv("KENT_TEST_LIFECYCLE_INHERITED"),
			Event:     event,
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			os.Exit(3)
		}
		recordPath := os.Getenv("KENT_TEST_LIFECYCLE_RECORD")
		tempPath := recordPath + ".tmp"
		if err := os.WriteFile(tempPath, encoded, 0o600); err != nil {
			os.Exit(4)
		}
		if err := os.Rename(tempPath, recordPath); err != nil {
			os.Exit(5)
		}
	case "fail":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("diagnostic", 1024))
		os.Exit(7)
	case "block":
		releasePath := os.Getenv("KENT_TEST_LIFECYCLE_RELEASE")
		for {
			if releasePath != "" {
				if _, err := os.Stat(releasePath); err == nil {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(8)
	}
}

type dispatcherProcessRecord struct {
	Workdir   string                  `json:"workdir"`
	Inherited string                  `json:"inherited"`
	Event     lifecyclecontract.Event `json:"event"`
}

func dispatcherProcessCommand() []string {
	return []string{os.Args[0], "-test.run=^TestLifecycleHookDispatcherProcess$"}
}

func testLifecycleEvent() lifecyclecontract.Event {
	return lifecyclecontract.NewSessionStart(time.Now().UTC(), false, lifecyclecontract.Context{}, lifecyclecontract.OpeningKindNew)
}

func waitForDispatcherIssue(t *testing.T, issues <-chan lifecyclehook.Issue, timeout time.Duration) lifecyclehook.Issue {
	t.Helper()
	select {
	case issue := <-issues:
		return issue
	case <-time.After(timeout):
		t.Fatal("timed out waiting for lifecycle hook issue")
		return lifecyclehook.Issue{}
	}
}

func waitForDispatcherRecord(t *testing.T, path string) dispatcherProcessRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			var record dispatcherProcessRecord
			if err := json.Unmarshal(encoded, &record); err != nil {
				t.Fatalf("decode process record: %v", err)
			}
			return record
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read process record: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for process record")
	return dispatcherProcessRecord{}
}

func recordDispatcherProcessStart() {
	startDir := os.Getenv("KENT_TEST_LIFECYCLE_START_DIR")
	if startDir == "" {
		return
	}
	if err := os.MkdirAll(startDir, 0o700); err != nil {
		os.Exit(9)
	}
	path := filepath.Join(startDir, fmt.Sprintf("%d", os.Getpid()))
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		os.Exit(10)
	}
}

func waitForDispatcherProcessCount(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dispatcherProcessCount(t, dir) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d hook processes; got %d", want, dispatcherProcessCount(t, dir))
}

func dispatcherProcessCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read process start directory: %v", err)
	}
	return len(entries)
}

func waitForDispatcherProcessLaunchAfterSaturation(
	t *testing.T,
	dispatcher *lifecyclehook.Dispatcher,
	dir string,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dispatcher.Submit(testLifecycleEvent())
		if dispatcherProcessCount(t, dir) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("later hook did not launch after issue presentation saturated; process count = %d", dispatcherProcessCount(t, dir))
}

func mustDispatcherProcessWorkdir() string {
	workdir, err := os.Getwd()
	if err != nil {
		os.Exit(11)
	}
	return workdir
}
