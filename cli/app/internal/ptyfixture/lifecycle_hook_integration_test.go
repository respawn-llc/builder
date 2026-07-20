package ptyfixture

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/appfixture"
	"core/shared/lifecyclecontract"
)

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
