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
	"core/internal/testharness/pty/driver"
	"core/internal/testharness/testsetup"
	"core/shared/lifecyclecontract"

	"github.com/google/uuid"
)

type lifecycleServerProcessReady struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"session_id"`
	ServerPort int    `json:"server_port"`
}

type lifecycleHookProcessReady struct {
	PID       int `json:"pid"`
	ParentPID int `json:"parent_pid"`
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
	result := runLifecyclePTYCommandAsync(
		t,
		ctx,
		bin,
		lifecyclePTYProcessEnv(t, root, processConfig),
	)
	waitForLifecycleHookRecordCount(t, recordPath, 2)
	capture := result.stopAndWait(t)
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

func TestLifecycleHooksHangingRecorderIsCanceledOnPTYShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	root := t.TempDir()
	scriptPath := filepath.Join(root, "script.json")
	recordPath := filepath.Join(root, "hooks.jsonl")
	readyPath := filepath.Join(root, "hook-ready.json")
	if err := os.WriteFile(scriptPath, []byte(`{"final":"unused final"}`), 0o600); err != nil {
		t.Fatalf("write hanging lifecycle script: %v", err)
	}
	processConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             filepath.Join(root, "workspace"),
		PersistenceRoot:           filepath.Join(root, "persistence"),
		ServerMode:                appfixture.LifecycleServerModeLocal,
		OpeningKind:               appfixture.LifecycleOpeningKindNew,
		LocalScriptPath:           &scriptPath,
		TargetFinalAssistantCount: 1,
		HookRecordPath:            recordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorHang,
		HookReadyPath:             &readyPath,
	}
	startedAt := time.Now()
	result := runLifecyclePTYCommandAsync(
		t,
		ctx,
		bin,
		lifecyclePTYProcessEnv(t, root, processConfig),
	)
	ready := waitForLifecycleHookProcessReady(t, readyPath)
	result.stopAndWait(t)
	if elapsed := time.Since(startedAt); elapsed >= 5*time.Second {
		t.Fatalf("hanging lifecycle PTY shutdown took %s, want less than hook deadline", elapsed)
	}
	testsetup.RequireProcessGone(t, time.Now().Add(3*time.Second), ready.PID)
	records := readLifecycleHookRecords(t, recordPath)
	if len(records) != 1 {
		t.Fatalf("hanging lifecycle hook record count = %d, want session start only: %+v", len(records), records)
	}
	var envelope struct {
		Category lifecyclecontract.Category `json:"category"`
	}
	if err := json.Unmarshal(records[0].Payload, &envelope); err != nil {
		t.Fatalf("decode hanging lifecycle hook payload: %v", err)
	}
	if envelope.Category != lifecyclecontract.CategorySessionStart {
		t.Fatalf("hanging lifecycle category = %q, want session start", envelope.Category)
	}
}

func TestLifecycleHooksSimultaneousRemoteTUIsUseIndependentSnapshots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	bin := buildPTYFixtureBinary(t, ctx)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	persistenceRoot := filepath.Join(root, "persistence")
	scriptPath := filepath.Join(root, "script.json")
	firstRecordPath := filepath.Join(root, "first-hooks.jsonl")
	secondRecordPath := filepath.Join(root, "second-hooks.jsonl")
	readyPath := filepath.Join(root, "server-ready.json")
	if err := os.WriteFile(scriptPath, []byte(`{"final":"unused simultaneous final"}`), 0o600); err != nil {
		t.Fatalf("write simultaneous lifecycle script: %v", err)
	}
	serverConfigPath := filepath.Join(root, "server-process.json")
	if err := appfixture.WriteLifecycleServerProcessConfig(
		serverConfigPath,
		appfixture.LifecycleServerProcessConfig{
			WorkspaceRoot:   workspace,
			PersistenceRoot: persistenceRoot,
			ScriptPath:      scriptPath,
			ReadyPath:       readyPath,
			HookRecordPath:  firstRecordPath,
			HookBehavior:    appfixture.LifecycleHookBehaviorSuccess,
		},
	); err != nil {
		t.Fatalf("write simultaneous lifecycle server process config: %v", err)
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
		t.Fatalf("start simultaneous lifecycle server process: %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- serverCommand.Wait() }()
	t.Cleanup(func() {
		stopServer()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Errorf("simultaneous lifecycle server did not stop output=%q", serverOutput.String())
		}
	})
	serverReady := waitForLifecycleServerReady(t, readyPath, serverDone, &serverOutput)
	if serverReady.SessionID == "" || serverReady.ServerPort <= 0 {
		t.Fatalf("simultaneous lifecycle server readiness is incomplete: %+v", serverReady)
	}

	firstConfig := appfixture.LifecycleProcessConfig{
		WorkspaceRoot:             workspace,
		PersistenceRoot:           persistenceRoot,
		ServerMode:                appfixture.LifecycleServerModeRemote,
		OpeningKind:               appfixture.LifecycleOpeningKindResumed,
		SessionID:                 &serverReady.SessionID,
		TargetFinalAssistantCount: 1,
		HookRecordPath:            firstRecordPath,
		HookBehavior:              appfixture.LifecycleHookBehaviorSuccess,
	}
	firstResult := runLifecyclePTYCommandAsync(
		t,
		ctx,
		bin,
		lifecyclePTYProcessEnv(t, filepath.Join(root, "first"), firstConfig),
	)
	waitForLifecycleHookRecordCount(t, firstRecordPath, 1)

	serverPort := serverReady.ServerPort
	if err := appfixture.WriteConfigWithOptions(
		ctx,
		persistenceRoot,
		appfixture.ConfigOptions{
			ServerPort:           &serverPort,
			LifecycleHookCommand: lifecycleRecorderCommandForTestBinary(bin, secondRecordPath),
		},
	); err != nil {
		t.Fatalf("rewrite simultaneous lifecycle client config: %v", err)
	}
	secondConfig := firstConfig
	secondConfig.HookRecordPath = secondRecordPath
	secondResult := runLifecyclePTYCommandAsync(
		t,
		ctx,
		bin,
		lifecyclePTYProcessEnv(t, filepath.Join(root, "second"), secondConfig),
	)
	waitForLifecycleHookRecordCount(t, secondRecordPath, 1)

	firstCapture := firstResult.stopAndWait(t)
	secondCapture := secondResult.stopAndWait(t)
	firstRecords := readLifecycleHookRecords(t, firstRecordPath)
	secondRecords := readLifecycleHookRecords(t, secondRecordPath)
	if len(firstRecords) != 1 || len(secondRecords) != 1 {
		t.Fatalf(
			"simultaneous lifecycle record counts = %d/%d, want one each; first=%q second=%q",
			len(firstRecords),
			len(secondRecords),
			string(firstCapture.Raw),
			string(secondCapture.Raw),
		)
	}
	if firstRecords[0].ParentPID == secondRecords[0].ParentPID ||
		firstRecords[0].ParentPID == serverReady.PID ||
		secondRecords[0].ParentPID == serverReady.PID {
		t.Fatalf(
			"simultaneous lifecycle parent PIDs = %d/%d server=%d, want two independent TUIs",
			firstRecords[0].ParentPID,
			secondRecords[0].ParentPID,
			serverReady.PID,
		)
	}
	for index, record := range []lifecycleHookRecord{firstRecords[0], secondRecords[0]} {
		var envelope struct {
			Category lifecyclecontract.Category `json:"category"`
		}
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			t.Fatalf("decode simultaneous lifecycle record %d: %v", index, err)
		}
		if envelope.Category != lifecyclecontract.CategorySessionStart {
			t.Fatalf("simultaneous lifecycle category %d = %q, want session start", index, envelope.Category)
		}
	}
}

type lifecyclePTYCommandResult struct {
	session *driver.Session
}

func runLifecyclePTYCommandAsync(
	t *testing.T,
	ctx context.Context,
	bin string,
	processEnv string,
) lifecyclePTYCommandResult {
	t.Helper()
	session, err := driver.StartSession(driver.SessionSpec{
		Path:       bin,
		Env:        append(os.Environ(), processEnv),
		Dimensions: pty.MustDimensions(24, 80),
	})
	if err != nil {
		t.Fatalf("start lifecycle PTY fixture: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-session.Done():
			return
		default:
		}
		_ = session.ForceKill()
		select {
		case <-session.Done():
		case <-time.After(time.Second):
			t.Error("lifecycle PTY fixture did not exit during cleanup")
		}
	})
	go func() {
		for range session.Events() {
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = session.ForceKill()
		case <-session.Done():
		}
	}()
	return lifecyclePTYCommandResult{session: session}
}

func (result lifecyclePTYCommandResult) stopAndWait(t *testing.T) pty.Capture {
	t.Helper()
	if err := result.session.Enqueue(driver.SessionCommand{
		ID:    uuid.New(),
		Kind:  driver.SessionCommandRuntimeControlByte,
		Bytes: []byte{0x03, 0x03},
	}); err != nil {
		t.Fatalf("stop lifecycle PTY fixture: %v", err)
	}
	select {
	case <-result.session.Done():
	case <-time.After(10 * time.Second):
		_ = result.session.ForceKill()
		t.Fatal("lifecycle PTY fixture did not exit")
	}
	capture, err := result.session.Capture()
	if err != nil {
		t.Fatalf("capture lifecycle PTY fixture: %v", err)
	}
	if capture.ProcessExit == nil || capture.ProcessExit.Code != 0 {
		t.Fatalf("lifecycle PTY fixture exit = %+v raw=%q", capture.ProcessExit, string(capture.Raw))
	}
	return capture
}

func lifecycleRecorderCommandForTestBinary(bin string, recordPath string) []string {
	return []string{
		bin,
		appfixture.LifecycleHookProductRecorderRunArg,
		"--",
		string(appfixture.LifecycleHookBehaviorSuccess),
		recordPath,
	}
}

func waitForLifecycleHookRecordCount(t *testing.T, path string, count int) {
	t.Helper()
	testsetup.RequireUntil(
		t,
		time.Now().Add(10*time.Second),
		10*time.Millisecond,
		func() bool {
			file, err := os.Open(path)
			if errors.Is(err, os.ErrNotExist) {
				return false
			}
			if err != nil {
				t.Fatalf("open lifecycle hook records while waiting: %v", err)
			}
			defer file.Close()
			decoder := json.NewDecoder(file)
			seen := 0
			for {
				var record lifecycleHookRecord
				if err := decoder.Decode(&record); err != nil {
					if err == io.EOF {
						return seen >= count
					}
					t.Fatalf("decode lifecycle hook records while waiting: %v", err)
				}
				seen++
			}
		},
		"lifecycle hook record count did not reach %d",
		count,
	)
}

func lifecyclePTYProcessEnv(t *testing.T, root string, config appfixture.LifecycleProcessConfig) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create lifecycle PTY process config root: %v", err)
	}
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

func waitForLifecycleHookProcessReady(t *testing.T, path string) lifecycleHookProcessReady {
	t.Helper()
	var ready lifecycleHookProcessReady
	testsetup.RequireUntil(
		t,
		time.Now().Add(10*time.Second),
		10*time.Millisecond,
		func() bool {
			encoded, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				return false
			}
			if err != nil {
				t.Fatalf("read lifecycle hook process readiness while waiting: %v", err)
			}
			if err := json.Unmarshal(encoded, &ready); err != nil {
				t.Fatalf("decode lifecycle hook process readiness while waiting: %v", err)
			}
			return ready.PID > 0 && ready.ParentPID > 0
		},
		"lifecycle hook process did not publish readiness",
	)
	return ready
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
