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
	Payload   json.RawMessage `json:"payload"`
}

func TestLifecycleHooksLocalConfiguredPTYRunsRepresentativeFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	root := t.TempDir()
	scriptPath := filepath.Join(root, "script.json")
	recordPath := filepath.Join(root, "hooks.jsonl")
	script := []byte(`{"steps":[
		{"tool_calls":[
			{"id":"call-1","name":"exec_command","input":{"cmd":"printf one"}},
			{"id":"call-2","name":"exec_command","input":{"cmd":"printf two"}}
		]},
		{"expected_tool_results":[
			{"CallID":"call-1","Name":"exec_command"},
			{"CallID":"call-2","Name":"exec_command"}
		],"tool_calls":[{"id":"ask-1","name":"ask_question","input":{
			"question":"Choose the next action",
			"suggestions":["continue","stop"],
			"recommended_option_index":1
		}}]},
		{"expected_tool_results":[{"CallID":"ask-1","Name":"ask_question"}],"final":"done"}
	]}`)
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatalf("write lifecycle script: %v", err)
	}
	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             filepath.Join(root, "workspace"),
		PersistenceRoot:           filepath.Join(root, "persistence"),
		ServerMode:                appfixture.LifecycleServerModeLocal,
		LocalScriptPath:           &scriptPath,
		InitialPrompt:             "run lifecycle scenario",
		TargetFinalAssistantCount: 1,
		HookRecordPath:            recordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorSuccess,
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: buildPTYFixtureBinary(t, ctx),
		Env: []string{
			"TERM=xterm-256color",
			"COLORTERM=truecolor",
			"NO_COLOR=",
			"CLICOLOR=1",
			"CLICOLOR_FORCE=1",
			"FORCE_COLOR=1",
			lifecyclePTYProcessEnv(t, root, processConfig),
		},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{
			{Phase: pty.PhaseScenarioStart, After: time.Second, Bytes: []byte("\r")},
			{Phase: pty.PhaseScenarioFinalApplied, After: 300 * time.Millisecond, Bytes: []byte{0x03, 0x03}},
		},
		Timeout: 25 * time.Second,
	})
	if err != nil {
		t.Fatalf("run local lifecycle PTY fixture: %v raw=%q", err, string(capture.Raw))
	}
	records := waitForLifecycleHookRecords(t, recordPath, 3)
	events := lifecycleEventsByCategory(t, records)
	for _, category := range []lifecyclecontract.Category{
		lifecyclecontract.CategorySessionStart,
		lifecyclecontract.CategoryInputRequired,
		lifecyclecontract.CategoryTaskComplete,
	} {
		if _, ok := events[category]; !ok {
			t.Fatalf("missing lifecycle category %q in %+v", category, events)
		}
	}
	var complete struct {
		WorkPerformed bool `json:"work_performed"`
	}
	if err := json.Unmarshal(events[lifecyclecontract.CategoryTaskComplete].Details, &complete); err != nil {
		t.Fatalf("decode task completion details: %v", err)
	}
	if !complete.WorkPerformed {
		t.Fatal("task completion omitted server-authored work metadata")
	}
}

func TestLifecycleHooksRemotePTYRunsInControllingClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	persistenceRoot := filepath.Join(root, "persistence")
	scriptPath := filepath.Join(root, "script.json")
	recordPath := filepath.Join(root, "hooks.jsonl")
	readyPath := filepath.Join(root, "server-ready.json")
	if err := os.WriteFile(scriptPath, []byte(`{"final":"remote done"}`), 0o600); err != nil {
		t.Fatalf("write remote lifecycle script: %v", err)
	}
	serverConfigPath := filepath.Join(root, "server-process.json")
	if err := appfixture.WriteLifecycleServerProcessConfig(serverConfigPath, appfixture.LifecycleServerProcessConfig{
		WorkspaceRoot:   workspace,
		PersistenceRoot: persistenceRoot,
		ScriptPath:      scriptPath,
		ReadyPath:       readyPath,
		HookRecordPath:  recordPath,
	}); err != nil {
		t.Fatalf("write remote lifecycle server config: %v", err)
	}
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	var serverOutput bytes.Buffer
	serverCommand := exec.CommandContext(serverCtx, bin)
	serverCommand.Env = append(os.Environ(), appfixture.LifecycleServerProcessConfigEnvName+"="+serverConfigPath)
	serverCommand.Stdout = &serverOutput
	serverCommand.Stderr = &serverOutput
	if err := serverCommand.Start(); err != nil {
		t.Fatalf("start remote lifecycle server: %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- serverCommand.Wait() }()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(serverCtx.Err(), context.Canceled) {
				t.Errorf("remote lifecycle server: %v output=%q", err, serverOutput.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("remote lifecycle server did not stop output=%q", serverOutput.String())
		}
	})
	serverReady := waitForLifecycleServerReady(t, readyPath, serverDone, &serverOutput)

	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             workspace,
		PersistenceRoot:           persistenceRoot,
		ServerMode:                appfixture.LifecycleServerModeRemote,
		InitialPrompt:             "run remote lifecycle scenario",
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
			After: 300 * time.Millisecond,
			Bytes: []byte{0x03, 0x03},
		}},
		Timeout: 25 * time.Second,
	})
	if err != nil {
		t.Fatalf("run remote lifecycle PTY fixture: %v raw=%q server=%q", err, string(capture.Raw), serverOutput.String())
	}
	records := waitForLifecycleHookRecords(t, recordPath, 2)
	events := lifecycleEventsByCategory(t, records)
	for _, category := range []lifecyclecontract.Category{
		lifecyclecontract.CategorySessionStart,
		lifecyclecontract.CategoryTaskComplete,
	} {
		if _, ok := events[category]; !ok {
			t.Fatalf("missing remote lifecycle category %q in %+v", category, events)
		}
	}
	for index, record := range records {
		if record.ParentPID == serverReady.PID {
			t.Fatalf("remote server process executed lifecycle hook %d", index)
		}
	}
}

func TestLifecycleHookFailureIsVisibleAndDoesNotBlockPTYRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("FORCE_COLOR", "1")

	root := t.TempDir()
	scriptPath := filepath.Join(root, "script.json")
	recordPath := filepath.Join(root, "hooks.jsonl")
	statePath := filepath.Join(root, "nonzero-once")
	if err := os.WriteFile(scriptPath, []byte(`{"final":"runtime continued"}`), 0o600); err != nil {
		t.Fatalf("write failing-hook script: %v", err)
	}
	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             filepath.Join(root, "workspace"),
		PersistenceRoot:           filepath.Join(root, "persistence"),
		ServerMode:                appfixture.LifecycleServerModeLocal,
		LocalScriptPath:           &scriptPath,
		InitialPrompt:             "run after hook failure",
		TargetFinalAssistantCount: 1,
		HookRecordPath:            recordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorNonzeroOnce,
		HookStatePath:             &statePath,
	}
	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:       buildPTYFixtureBinary(t, ctx),
		Env:        []string{lifecyclePTYProcessEnv(t, root, processConfig)},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{{
			Phase: pty.PhaseScenarioFinalApplied,
			After: 300 * time.Millisecond,
			Bytes: []byte{0x03, 0x03},
		}},
		Timeout: 25 * time.Second,
	})
	if err != nil {
		t.Fatalf("run failing-hook lifecycle PTY fixture: %v raw=%q", err, string(capture.Raw))
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("failing lifecycle hook did not publish failure state: %v", err)
	}
	events := lifecycleEventsByCategory(t, waitForLifecycleHookRecords(t, recordPath, 2))
	if _, ok := events[lifecyclecontract.CategoryTaskComplete]; !ok {
		t.Fatalf("runtime did not complete after hook failure: %+v", events)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze failing-hook PTY capture: %v", err)
	}
	var visibleError bool
	for _, row := range analysis.Screen.Cells[len(analysis.Screen.Cells)-2:] {
		for _, cell := range row {
			if cell.Content != "" && cell.Bold && cell.Foreground != "" {
				visibleError = true
			}
		}
	}
	if !visibleError {
		t.Fatalf("final PTY screen has no visible error-styled notice:\n%s", analysis.Screen.RenderText())
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

func waitForLifecycleHookRecords(t *testing.T, path string, count int) []lifecycleHookRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, err := os.Open(path)
		if err == nil {
			var records []lifecycleHookRecord
			decoder := json.NewDecoder(file)
			for {
				var record lifecycleHookRecord
				if err := decoder.Decode(&record); err != nil {
					if err != io.EOF {
						_ = file.Close()
						t.Fatalf("decode lifecycle hook records: %v", err)
					}
					break
				}
				records = append(records, record)
			}
			_ = file.Close()
			if len(records) >= count {
				return records
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lifecycle hook records", count)
	return nil
}

type lifecycleEventEnvelope struct {
	Category lifecyclecontract.Category `json:"category"`
	Details  json.RawMessage            `json:"details"`
}

func lifecycleEventsByCategory(
	t *testing.T,
	records []lifecycleHookRecord,
) map[lifecyclecontract.Category]lifecycleEventEnvelope {
	t.Helper()
	events := make(map[lifecyclecontract.Category]lifecycleEventEnvelope, len(records))
	for index, record := range records {
		var event lifecycleEventEnvelope
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode lifecycle hook record %d: %v", index, err)
		}
		events[event.Category] = event
	}
	return events
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
