package ptyfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/appfixture"
	"core/shared/lifecyclecontract"
)

type lifecycleServerProcessReady struct {
	PID int `json:"pid"`
}

type lifecycleHookRecord struct {
	ParentPID int             `json:"parent_pid"`
	Cwd       string          `json:"cwd"`
	Payload   json.RawMessage `json:"payload"`
}

func TestLifecycleHooksLocalConfiguredPTYRecordsAcceptedOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	recordPath := filepath.Join(t.TempDir(), "hooks.jsonl")
	script := []byte(`{
		"input_token_count": 34000,
		"context_window_tokens": 40000,
		"compactions": [{"summary": "compacted context", "trimmed_items_count": 2, "input_tokens_after": 100}],
		"steps": [
			{"tool_calls": [
				{"id": "call-1", "name": "exec_command", "input": {"cmd": "printf one"}},
				{"id": "call-2", "name": "exec_command", "input": {"cmd": "printf two"}}
			]},
			{"expected_tool_results": [
				{"CallID": "call-1", "Name": "exec_command"},
				{"CallID": "call-2", "Name": "exec_command"}
			], "tool_calls": [
				{"id": "ask-1", "name": "ask_question", "input": {
					"question": "Choose the next action",
					"suggestions": ["continue", "stop"],
					"recommended_option_index": 1
				}}
			]},
			{"expected_tool_results": [{"CallID": "ask-1", "Name": "ask_question"}], "final": "lifecycle scenario complete"}
		]
	}`)
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatalf("write lifecycle script: %v", err)
	}
	prompt := "run lifecycle scenario"
	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             workspace,
		PersistenceRoot:           persistenceRoot,
		ServerMode:                appfixture.LifecycleServerModeLocal,
		OpeningKind:               appfixture.LifecycleOpeningKindResumed,
		LocalScriptPath:           &scriptPath,
		InitialPrompt:             &prompt,
		TargetFinalAssistantCount: 1,
		HookRecordPath:            recordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorSuccess,
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:       bin,
		Env:        []string{lifecyclePTYProcessEnv(t, filepath.Dir(scriptPath), processConfig)},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{
			{Phase: pty.PhaseScenarioStart, After: 1500 * time.Millisecond, Bytes: []byte("\r")},
			{Phase: pty.PhaseScenarioFinalApplied, After: 500 * time.Millisecond, Bytes: []byte{0x03, 0x03}},
		},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run lifecycle PTY fixture: %v raw=%q", err, string(capture.Raw))
	}

	records := readLifecycleHookRecords(t, recordPath)
	if len(records) != 4 {
		t.Fatalf("lifecycle hook record count = %d, want four: %+v", len(records), records)
	}
	wantCategories := []lifecyclecontract.Category{
		lifecyclecontract.CategorySessionStart,
		lifecyclecontract.CategoryResourceLimit,
		lifecyclecontract.CategoryInputRequired,
		lifecyclecontract.CategoryTaskComplete,
	}
	for index, record := range records {
		var envelope struct {
			Category lifecyclecontract.Category `json:"category"`
			Focused  bool                       `json:"focused"`
			Context  struct {
				SessionID string `json:"session_id"`
			} `json:"context"`
			Details json.RawMessage `json:"details"`
		}
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			t.Fatalf("decode lifecycle hook record %d: %v", index, err)
		}
		if envelope.Category != wantCategories[index] {
			t.Fatalf("lifecycle category %d = %q, want %q", index, envelope.Category, wantCategories[index])
		}
		if envelope.Focused {
			t.Fatalf("lifecycle event %d reported focused before PTY focus authority was known", index)
		}
		if envelope.Context.SessionID == "" {
			t.Fatalf("lifecycle event %d omitted materialized session ID", index)
		}
		if index == 0 {
			var details struct {
				Kind lifecyclecontract.OpeningKind `json:"kind"`
			}
			if err := json.Unmarshal(envelope.Details, &details); err != nil {
				t.Fatalf("decode session-start details: %v", err)
			}
			if details.Kind != lifecyclecontract.OpeningKindResumed {
				t.Fatalf("session start kind = %q, want resumed", details.Kind)
			}
		}
		if index == 2 {
			var details struct {
				Kind lifecyclecontract.InputKind `json:"kind"`
			}
			if err := json.Unmarshal(envelope.Details, &details); err != nil {
				t.Fatalf("decode input-required details: %v", err)
			}
			if details.Kind != lifecyclecontract.InputKindQuestion {
				t.Fatalf("input required kind = %q, want question", details.Kind)
			}
		}
		if index == 3 {
			var details struct {
				WorkPerformed bool `json:"work_performed"`
			}
			if err := json.Unmarshal(envelope.Details, &details); err != nil {
				t.Fatalf("decode task-complete details: %v", err)
			}
			if !details.WorkPerformed {
				t.Fatal("task completion omitted two-tool work metadata")
			}
		}
	}
}

func TestLifecycleHooksRemoteConfiguredPTYRecordsNewOpening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	persistenceRoot := filepath.Join(root, "persistence")
	scriptPath := filepath.Join(root, "script.json")
	recordPath := filepath.Join(root, "hooks.jsonl")
	readyPath := filepath.Join(root, "server-ready.json")
	script := []byte(`{"final":"remote lifecycle scenario complete"}`)
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatalf("write remote lifecycle script: %v", err)
	}
	serverConfigPath := filepath.Join(root, "server-process.json")
	if err := appfixture.WriteLifecycleServerProcessConfig(
		serverConfigPath,
		appfixture.LifecycleServerProcessConfig{
			WorkspaceRoot:   workspace,
			PersistenceRoot: persistenceRoot,
			ScriptPath:      scriptPath,
			ReadyPath:       readyPath,
			HookRecordPath:  recordPath,
			HookBehavior:    appfixture.LifecycleHookBehaviorSuccess,
		},
	); err != nil {
		t.Fatalf("write remote lifecycle server process config: %v", err)
	}
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	var serverOutput bytes.Buffer
	serverCommand := exec.CommandContext(serverCtx, bin)
	serverCommand.Env = append(
		os.Environ(),
		appfixture.LifecycleServerProcessConfigEnvName+"="+serverConfigPath,
	)
	serverCommand.Stdout = &serverOutput
	serverCommand.Stderr = &serverOutput
	if err := serverCommand.Start(); err != nil {
		t.Fatalf("start remote lifecycle server process: %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- serverCommand.Wait() }()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(serverCtx.Err(), context.Canceled) {
				t.Errorf("remote lifecycle server process: %v output=%q", err, serverOutput.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("remote lifecycle server process did not stop output=%q", serverOutput.String())
		}
	})
	serverReady := waitForLifecycleServerReady(t, readyPath, serverDone, &serverOutput)

	prompt := "run remote lifecycle scenario"
	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             workspace,
		PersistenceRoot:           persistenceRoot,
		ServerMode:                appfixture.LifecycleServerModeRemote,
		OpeningKind:               appfixture.LifecycleOpeningKindNew,
		InitialPrompt:             &prompt,
		TargetFinalAssistantCount: 1,
		HookRecordPath:            recordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorSuccess,
	}
	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:       bin,
		Env:        []string{lifecyclePTYProcessEnv(t, root, processConfig)},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{{
			Phase: pty.PhaseScenarioFinalApplied,
			After: 500 * time.Millisecond,
			Bytes: []byte{0x03, 0x03},
		}},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run remote lifecycle PTY fixture: %v raw=%q server=%q", err, string(capture.Raw), serverOutput.String())
	}
	records := readLifecycleHookRecords(t, recordPath)
	if len(records) != 2 {
		t.Fatalf("remote lifecycle hook record count = %d, want two: %+v", len(records), records)
	}
	wantCategories := []lifecyclecontract.Category{
		lifecyclecontract.CategorySessionStart,
		lifecyclecontract.CategoryTaskComplete,
	}
	for index, record := range records {
		if record.ParentPID == serverReady.PID {
			t.Fatalf("remote server process executed lifecycle hook %d", index)
		}
		var envelope struct {
			Category lifecyclecontract.Category `json:"category"`
			Details  json.RawMessage            `json:"details"`
		}
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			t.Fatalf("decode remote lifecycle hook record %d: %v", index, err)
		}
		if envelope.Category != wantCategories[index] {
			t.Fatalf("remote lifecycle category %d = %q, want %q", index, envelope.Category, wantCategories[index])
		}
		if index == 0 {
			var details struct {
				Kind lifecyclecontract.OpeningKind `json:"kind"`
			}
			if err := json.Unmarshal(envelope.Details, &details); err != nil {
				t.Fatalf("decode remote session-start details: %v", err)
			}
			if details.Kind != lifecyclecontract.OpeningKindNew {
				t.Fatalf("remote session start kind = %q, want new", details.Kind)
			}
		}
	}
}

func TestLifecycleHooksRuntimeFailureAfterNonzeroDoesNotRecurse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	root := t.TempDir()
	scriptPath := filepath.Join(root, "script.json")
	recordPath := filepath.Join(root, "hooks.jsonl")
	nonzeroStatePath := filepath.Join(root, "nonzero-once")
	script := []byte(`{"steps":[{"error":"scripted terminal runtime failure"}]}`)
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatalf("write failing lifecycle script: %v", err)
	}
	prompt := "run failing lifecycle scenario"
	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             filepath.Join(root, "workspace"),
		PersistenceRoot:           filepath.Join(root, "persistence"),
		ServerMode:                appfixture.LifecycleServerModeLocal,
		OpeningKind:               appfixture.LifecycleOpeningKindNew,
		LocalScriptPath:           &scriptPath,
		InitialPrompt:             &prompt,
		TargetFinalAssistantCount: 0,
		HookRecordPath:            recordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorNonzeroOnce,
		HookStatePath:             &nonzeroStatePath,
	}
	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:       bin,
		Env:        []string{lifecyclePTYProcessEnv(t, root, processConfig)},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{{
			Phase: pty.PhaseScenarioStart,
			After: 3 * time.Second,
			Bytes: []byte{0x03, 0x03},
		}},
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("run failing lifecycle PTY fixture: %v raw=%q", err, string(capture.Raw))
	}
	if _, err := os.Stat(nonzeroStatePath); err != nil {
		t.Fatalf("non-zero-once recorder did not publish its failure state: %v", err)
	}
	records := readLifecycleHookRecords(t, recordPath)
	if len(records) != 2 {
		t.Fatalf(
			"failing lifecycle hook record count = %d, want start and task error only: %+v raw=%q",
			len(records),
			records,
			string(capture.Raw),
		)
	}
	wantCategories := []lifecyclecontract.Category{
		lifecyclecontract.CategorySessionStart,
		lifecyclecontract.CategoryTaskError,
	}
	for index, record := range records {
		var envelope struct {
			Category lifecyclecontract.Category `json:"category"`
		}
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			t.Fatalf("decode failing lifecycle hook record %d: %v", index, err)
		}
		if envelope.Category != wantCategories[index] {
			t.Fatalf("failing lifecycle category %d = %q, want %q", index, envelope.Category, wantCategories[index])
		}
	}
}

func lifecyclePTYProcessEnv(t *testing.T, root string, config appfixture.LifecycleProcessConfig) string {
	t.Helper()
	path := filepath.Join(root, "lifecycle-process.json")
	if err := appfixture.WriteLifecycleProcessConfig(path, config); err != nil {
		t.Fatalf("write lifecycle PTY process config: %v", err)
	}
	return appfixture.LifecycleProcessConfigEnvName + "=" + path
}

func readLifecycleHookRecords(t *testing.T, path string) []lifecycleHookRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open lifecycle hook records: %v", err)
	}
	defer file.Close()
	var records []lifecycleHookRecord
	decoder := json.NewDecoder(file)
	for {
		var record lifecycleHookRecord
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				return records
			}
			t.Fatalf("decode lifecycle hook records: %v", err)
		}
		records = append(records, record)
	}
}

func waitForLifecycleServerReady(
	t *testing.T,
	path string,
	done <-chan error,
	output *bytes.Buffer,
) lifecycleServerProcessReady {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			var ready lifecycleServerProcessReady
			if err := json.Unmarshal(encoded, &ready); err != nil {
				t.Fatalf("decode lifecycle server readiness: %v", err)
			}
			if ready.PID <= 0 {
				t.Fatalf("lifecycle server readiness has invalid PID: %+v", ready)
			}
			return ready
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read lifecycle server readiness: %v", err)
		}
		select {
		case err := <-done:
			t.Fatalf("lifecycle server exited before readiness: %v output=%q", err, output.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("lifecycle server did not become ready output=%q", output.String())
	return lifecycleServerProcessReady{}
}
