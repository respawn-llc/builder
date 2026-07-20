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
	readyPath  *string
}

type lifecycleHookProductRecord struct {
	ParentPID int             `json:"parent_pid"`
	Cwd       string          `json:"cwd"`
	Payload   json.RawMessage `json:"payload"`
}

func isLifecycleHookProductRecorderInvocation(args []string) bool {
	_, ok := parseLifecycleHookProductRecorderInvocation(args)
	return ok
}

func parseLifecycleHookProductRecorderInvocation(args []string) (lifecycleHookProductRecorderInvocation, bool) {
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
	case appfixture.LifecycleHookBehaviorSuccess, appfixture.LifecycleHookBehaviorNonzero:
		if len(args) != 5 {
			return lifecycleHookProductRecorderInvocation{}, false
		}
	case appfixture.LifecycleHookBehaviorHang:
		if len(args) != 6 {
			return lifecycleHookProductRecorderInvocation{}, false
		}
		readyPath := args[5]
		invocation.readyPath = &readyPath
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
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("read lifecycle hook cwd: %v", err)
	}
	record := lifecycleHookProductRecord{
		ParentPID: os.Getppid(),
		Cwd:       cwd,
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
	case appfixture.LifecycleHookBehaviorHang:
		ready := struct {
			PID       int `json:"pid"`
			ParentPID int `json:"parent_pid"`
		}{
			PID:       os.Getpid(),
			ParentPID: os.Getppid(),
		}
		encodedReady, err := json.Marshal(ready)
		if err != nil {
			t.Fatalf("marshal lifecycle hook readiness: %v", err)
		}
		if invocation.readyPath == nil {
			t.Fatal("hanging lifecycle hook recorder requires a ready path")
		}
		if err := os.WriteFile(*invocation.readyPath, encodedReady, 0o600); err != nil {
			t.Fatalf("publish lifecycle hook readiness: %v", err)
		}
		select {}
	default:
		t.Fatal(fmt.Sprintf("unsupported lifecycle hook recorder behavior %q", invocation.behavior))
	}
}
