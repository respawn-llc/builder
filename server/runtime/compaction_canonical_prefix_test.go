package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestManualRemoteCompactionRebuildsCanonicalPrefixOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	globalConfigDir := filepath.Join(root, "global")
	workspace := filepath.Join(root, "workspace")
	skillDir := filepath.Join(workspace, config.ConfigDirName, "skills", "workspace-skill")
	for _, dir := range []string{globalConfigDir, workspace, skillDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture directory %q: %v", dir, err)
		}
	}
	writeTestFile(t, filepath.Join(globalConfigDir, agentsFileName), "global instructions")
	writeTestFile(t, filepath.Join(workspace, agentsFileName), "workspace instructions")
	writeTestFile(t, filepath.Join(skillDir, "SKILL.md"), skillFixtureMarkdown("workspace-skill", "workspace skill"))

	const checkpointID = "remote-compaction-checkpoint"
	store := mustCreateNamedTestSession(t, "ws", workspace)
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{{
		OutputItems: []llm.ResponseItem{
			{
				Type:        llm.ResponseItemTypeMessage,
				Role:        textutil.Value(llm.RoleUser),
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			},
			{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value(checkpointID),
				EncryptedContent: textutil.Value("encrypted"),
			},
		},
		Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
	}}}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{Model: "gpt-5", GlobalConfigDir: globalConfigDir},
	)
	if err := store.SetHeadlessActive(true); err != nil {
		t.Fatalf("enable headless context: %v", err)
	}
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	stepID := runtimeTestStepID("compact")
	restoreStep := setTestActiveStep(engine, stepID)
	_, receipt, err := engine.compactNow(
		context.Background(),
		stepID,
		compactionModeManual,
		compactionInstructionsInput{},
		false,
	)
	restoreStep()
	if err != nil || !receipt.Committed {
		t.Fatalf("compact remote context: receipt=%+v error=%v", receipt, err)
	}

	items := engine.transcriptRuntimeState().SnapshotItems()
	if len(items) < 7 {
		t.Fatalf("canonical replacement items = %+v, want remote output and canonical context", items)
	}
	if items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].MessageType == nil ||
		*items[0].MessageType != llm.MessageTypeHeadlessMode {
		t.Fatalf("canonical replacement first item = %+v, want headless stable context", items[0])
	}
	want := []llm.MessageType{
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeAgentsMD,
	}
	for index, messageType := range want {
		item := items[index+1]
		if item.Type != llm.ResponseItemTypeMessage ||
			item.MessageType == nil ||
			*item.MessageType != messageType {
			t.Fatalf(
				"canonical replacement item[%d] = %+v, want message type %q",
				index+1,
				item,
				messageType,
			)
		}
	}
	if items[4].Type != llm.ResponseItemTypeMessage ||
		items[4].MessageType == nil ||
		*items[4].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("canonical replacement summary = %+v, want provider output after stable context", items[4])
	}
	if items[5].Type != llm.ResponseItemTypeCompaction ||
		items[5].ID == nil ||
		*items[5].ID != checkpointID {
		t.Fatalf("canonical replacement checkpoint = %+v, want identity %q", items[5], checkpointID)
	}
	if items[6].Type != llm.ResponseItemTypeMessage ||
		items[6].MessageType == nil ||
		*items[6].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("canonical replacement environment = %+v, want volatile suffix", items[6])
	}
}
