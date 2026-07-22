package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"core/internal/testharness/pty/appfixture"
)

const lifecycleHookProductRecorderRunArg = "-test.run=^TestLifecycleHookProductRecorder$"

type lifecycleHookProductRecorderInvocation struct {
	behavior   appfixture.LifecycleHookBehavior
	recordPath string
	statePath  *string
}

func isLifecycleHookProductRecorderInvocation(args []string) bool {
	_, ok := parseLifecycleHookProductRecorderInvocation(args)
	return ok
}

func parseLifecycleHookProductRecorderInvocation(
	args []string,
) (lifecycleHookProductRecorderInvocation, bool) {
	if len(args) != 5 && len(args) != 6 {
		return lifecycleHookProductRecorderInvocation{}, false
	}
	if args[1] != lifecycleHookProductRecorderRunArg || args[2] != "--" {
		return lifecycleHookProductRecorderInvocation{}, false
	}
	invocation := lifecycleHookProductRecorderInvocation{
		behavior:   appfixture.LifecycleHookBehavior(args[3]),
		recordPath: args[4],
	}
	switch invocation.behavior {
	case appfixture.LifecycleHookBehaviorSuccess:
		if len(args) != 5 {
			return lifecycleHookProductRecorderInvocation{}, false
		}
	case appfixture.LifecycleHookBehaviorNonzero:
		if len(args) != 5 {
			return lifecycleHookProductRecorderInvocation{}, false
		}
	case appfixture.LifecycleHookBehaviorNonzeroOnce:
		if len(args) != 6 {
			return lifecycleHookProductRecorderInvocation{}, false
		}
		statePath := args[5]
		invocation.statePath = &statePath
	default:
		return lifecycleHookProductRecorderInvocation{}, false
	}
	return invocation, true
}

func TestLifecycleHookProductRecorder(t *testing.T) {
	invocation, ok := parseLifecycleHookProductRecorderInvocation(os.Args)
	if !ok {
		t.Skip("lifecycle hook product recorder runs only through its exact subprocess invocation")
	}
	var payload json.RawMessage
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode lifecycle hook payload: %v", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("lifecycle hook stdin must contain exactly one JSON object: %v", err)
	}
	record := struct {
		ParentPID int             `json:"parent_pid"`
		Payload   json.RawMessage `json:"payload"`
	}{
		ParentPID: os.Getppid(),
		Payload:   append(json.RawMessage(nil), payload...),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal lifecycle hook record: %v", err)
	}
	file, err := os.OpenFile(invocation.recordPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open lifecycle hook record file: %v", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		t.Fatalf("append lifecycle hook record: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close lifecycle hook record file: %v", err)
	}
	switch invocation.behavior {
	case appfixture.LifecycleHookBehaviorSuccess:
		return
	case appfixture.LifecycleHookBehaviorNonzero:
		os.Exit(7)
	case appfixture.LifecycleHookBehaviorNonzeroOnce:
		if invocation.statePath == nil {
			t.Fatal("non-zero-once lifecycle hook recorder requires a state path")
		}
		stateFile, err := os.OpenFile(*invocation.statePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if closeErr := stateFile.Close(); closeErr != nil {
				t.Fatalf("close non-zero-once lifecycle hook state: %v", closeErr)
			}
			os.Exit(7)
		}
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("open non-zero-once lifecycle hook state: %v", err)
		}
	default:
		t.Fatal(fmt.Sprintf("unsupported lifecycle hook recorder behavior %q", invocation.behavior))
	}
}

func lifecycleHookProductRecorderCommand(
	recordPath string,
	behavior appfixture.LifecycleHookBehavior,
	statePath *string,
) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve lifecycle recorder executable: %w", err)
	}
	command := []string{
		executable,
		lifecycleHookProductRecorderRunArg,
		"--",
		string(behavior),
		recordPath,
	}
	if behavior == appfixture.LifecycleHookBehaviorNonzeroOnce {
		if statePath == nil {
			return nil, errors.New("non-zero-once lifecycle recorder requires a state path")
		}
		command = append(command, *statePath)
	}
	return command, nil
}
