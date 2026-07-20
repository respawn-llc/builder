package app

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/shared/invariant"
	"core/shared/lifecyclecontract"
)

const (
	lifecycleHookHelperEnvironmentName = "KENT_LIFECYCLE_HOOK_HELPER"
	lifecycleHookHelperMarkerName      = "KENT_LIFECYCLE_HOOK_MARKER"
	lifecycleHookHelperReadyPathName   = "KENT_LIFECYCLE_HOOK_READY_PATH"
	lifecycleHookHelperReleasePathName = "KENT_LIFECYCLE_HOOK_RELEASE_PATH"
)

type lifecycleHookHelperRecord struct {
	Args        []string        `json:"args"`
	Cwd         string          `json:"cwd"`
	Environment string          `json:"environment"`
	Payload     json.RawMessage `json:"payload"`
}

func TestLifecycleHookDispatcherInvokesCopiedArgvInFIFOOrder(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "records.jsonl")
	marker := "dynamic-inherited-environment"
	t.Setenv(lifecycleHookHelperEnvironmentName, "1")
	t.Setenv(lifecycleHookHelperMarkerName, marker)
	t.Setenv(lifecycleHookHelperReadyPathName, "")
	t.Setenv(lifecycleHookHelperReleasePathName, "")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get dispatcher test working directory: %v", err)
	}
	argv := []string{
		os.Args[0],
		"-test.run=^TestLifecycleHookDispatcherHelper$",
		"--",
		recordPath,
		"fixed-original",
	}
	dispatcher, err := newLifecycleHookDispatcher(
		argv,
		lifecyclecontract.NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))),
	)
	if err != nil {
		t.Fatalf("construct lifecycle hook dispatcher: %v", err)
	}
	t.Cleanup(func() {
		if err := dispatcher.Close(); err != nil {
			t.Errorf("close lifecycle hook dispatcher: %v", err)
		}
	})
	argv[len(argv)-1] = "fixed-mutated-after-construction"

	for index := 1; index <= 3; index++ {
		if accepted := dispatcher.EnqueueLifecycleEnvelope(dispatcherTestEnvelope(t, index)); !accepted {
			t.Fatalf("enqueue lifecycle envelope %d was rejected", index)
		}
	}

	records := waitForLifecycleHookHelperRecords(t, recordPath, 3)
	for index, record := range records {
		if !slices.Equal(record.Args, []string{recordPath, "fixed-original"}) {
			t.Fatalf("helper args %d = %q, want copied fixed argv", index, record.Args)
		}
		if record.Cwd != workingDirectory {
			t.Fatalf("helper cwd %d = %q, want inherited %q", index, record.Cwd, workingDirectory)
		}
		if record.Environment != marker {
			t.Fatalf("helper environment %d = %q, want inherited marker %q", index, record.Environment, marker)
		}
		var payload struct {
			Details struct {
				FinalAnswer string `json:"final_answer"`
			} `json:"details"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode helper payload %d: %v", index, err)
		}
		want := dispatcherTestAnswer(index + 1)
		if payload.Details.FinalAnswer != want {
			t.Fatalf("helper payload %d answer = %q, want FIFO answer %q", index, payload.Details.FinalAnswer, want)
		}
	}
}

func TestLifecycleHookDispatcherEnqueueDoesNotWaitForActiveProcess(t *testing.T) {
	tempDir := t.TempDir()
	recordPath := filepath.Join(tempDir, "records.jsonl")
	readyPath := filepath.Join(tempDir, "ready")
	releasePath := filepath.Join(tempDir, "release")
	t.Setenv(lifecycleHookHelperEnvironmentName, "1")
	t.Setenv(lifecycleHookHelperReadyPathName, readyPath)
	t.Setenv(lifecycleHookHelperReleasePathName, releasePath)
	dispatcher, err := newLifecycleHookDispatcher(
		[]string{
			os.Args[0],
			"-test.run=^TestLifecycleHookDispatcherHelper$",
			"--",
			recordPath,
			"fixed-blocking",
		},
		lifecyclecontract.NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))),
	)
	if err != nil {
		t.Fatalf("construct blocking lifecycle hook dispatcher: %v", err)
	}
	t.Cleanup(func() {
		if err := dispatcher.Close(); err != nil {
			t.Errorf("close blocking lifecycle hook dispatcher: %v", err)
		}
	})

	if accepted := dispatcher.EnqueueLifecycleEnvelope(dispatcherTestEnvelope(t, 1)); !accepted {
		t.Fatal("first blocking lifecycle envelope was rejected")
	}
	testsetup.RequireUntil(
		t,
		time.Now().Add(2*time.Second),
		10*time.Millisecond,
		func() bool {
			_, err := os.Stat(readyPath)
			return err == nil
		},
		"lifecycle helper did not report active process",
	)

	secondEnvelope := dispatcherTestEnvelope(t, 2)
	returned := make(chan bool, 1)
	go func() {
		returned <- dispatcher.EnqueueLifecycleEnvelope(secondEnvelope)
	}()
	select {
	case accepted := <-returned:
		if !accepted {
			t.Fatal("second lifecycle envelope was rejected while worker was active")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("lifecycle enqueue waited for the active process")
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release lifecycle helper: %v", err)
	}
	waitForLifecycleHookHelperRecords(t, recordPath, 2)
}

func TestLifecycleHookInvocationHasNoWorkingDirectoryAuthority(t *testing.T) {
	invocationType := reflect.TypeOf(lifecycleHookInvocation{})
	for index := 0; index < invocationType.NumField(); index++ {
		switch invocationType.Field(index).Name {
		case "cwd", "Cwd", "root", "Root", "workdir", "Workdir":
			t.Fatalf("lifecycle hook invocation exposes working-directory authority through %q", invocationType.Field(index).Name)
		}
	}
}

func TestLifecycleHookDispatcherHelper(t *testing.T) {
	if os.Getenv(lifecycleHookHelperEnvironmentName) != "1" {
		return
	}
	args := flag.Args()
	if len(args) != 2 {
		t.Fatalf("lifecycle hook helper args = %q, want record path and fixed marker", args)
	}
	decoder := json.NewDecoder(os.Stdin)
	var payload json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode lifecycle hook payload: %v", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("lifecycle hook stdin contains more than one JSON object: %v", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get lifecycle hook helper cwd: %v", err)
	}
	if readyPath := os.Getenv(lifecycleHookHelperReadyPathName); readyPath != "" {
		if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
			t.Fatalf("write lifecycle hook helper ready marker: %v", err)
		}
		releasePath := os.Getenv(lifecycleHookHelperReleasePathName)
		testsetup.RequireUntil(
			t,
			time.Now().Add(4*time.Second),
			10*time.Millisecond,
			func() bool {
				_, err := os.Stat(releasePath)
				return err == nil
			},
			"lifecycle hook helper was not released",
		)
	}
	stdout := os.Stdout
	stderr := os.Stderr
	_, _ = stdout.Write([]byte(strings.Repeat("ignored stdout", 1024)))
	_, _ = stderr.Write([]byte(strings.Repeat("bounded stderr", 4096)))
	file, err := os.OpenFile(args[0], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open lifecycle hook record file: %v", err)
	}
	record := lifecycleHookHelperRecord{
		Args:        append([]string(nil), args...),
		Cwd:         workingDirectory,
		Environment: os.Getenv(lifecycleHookHelperMarkerName),
		Payload:     append(json.RawMessage(nil), payload...),
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		_ = file.Close()
		t.Fatalf("append lifecycle hook record: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close lifecycle hook record file: %v", err)
	}
}

func dispatcherTestEnvelope(t *testing.T, index int) lifecyclecontract.Envelope {
	t.Helper()
	envelope, err := lifecyclecontract.NewEnvelope(lifecyclecontract.EnvelopeInput{
		Scope:      lifecyclecontract.ScopeClient,
		Category:   lifecyclecontract.CategoryTaskComplete,
		OccurredAt: time.Unix(int64(index), 0).UTC(),
		Details: lifecyclecontract.NewTaskCompleteDetails(
			dispatcherTestAnswer(index),
			index%2 == 0,
		),
	})
	if err != nil {
		t.Fatalf("construct dispatcher test envelope %d: %v", index, err)
	}
	return envelope
}

func dispatcherTestAnswer(index int) string {
	return "dynamic dispatcher answer " + string(rune('0'+index))
}

func waitForLifecycleHookHelperRecords(
	t *testing.T,
	path string,
	count int,
) []lifecycleHookHelperRecord {
	t.Helper()
	var records []lifecycleHookHelperRecord
	testsetup.RequireUntil(
		t,
		time.Now().Add(5*time.Second),
		10*time.Millisecond,
		func() bool {
			file, err := os.Open(path)
			if err != nil {
				return false
			}
			defer file.Close()
			decoded := make([]lifecycleHookHelperRecord, 0, count)
			decoder := json.NewDecoder(file)
			for {
				var record lifecycleHookHelperRecord
				if err := decoder.Decode(&record); err != nil {
					if err == io.EOF {
						break
					}
					return false
				}
				decoded = append(decoded, record)
			}
			if len(decoded) < count {
				return false
			}
			records = decoded
			return true
		},
		"lifecycle hook records did not reach %d entries",
		count,
	)
	return records
}
