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
		tools.NewRegistry(tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{Model: "gpt-5", GlobalConfigDir: globalConfigDir},
	)
	if err := store.SetHeadlessActive(true); err != nil {
		t.Fatalf("enable headless context: %v", err)
	}
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	_, receipt, err := engine.compactNow(
		context.Background(),
		"compact",
		compactionModeManual,
		"",
		false,
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("compact remote context: receipt=%+v error=%v", receipt, err)
	}

	items := engine.transcriptRuntimeState().SnapshotItems()
	if len(items) < 7 {
		t.Fatalf("canonical replacement items = %+v, want remote output and canonical context", items)
	}
	if items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].MessageType == nil ||
		*items[0].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("canonical replacement first item = %+v, want compaction summary", items[0])
	}
	if items[1].Type != llm.ResponseItemTypeCompaction ||
		items[1].ID == nil ||
		*items[1].ID != checkpointID {
		t.Fatalf("canonical replacement checkpoint = %+v, want identity %q", items[1], checkpointID)
	}
	want := []llm.MessageType{
		llm.MessageTypeEnvironment,
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeHeadlessMode,
	}
	for index, messageType := range want {
		item := items[index+2]
		if item.Type != llm.ResponseItemTypeMessage ||
			item.MessageType == nil ||
			*item.MessageType != messageType {
			t.Fatalf(
				"canonical replacement item[%d] = %+v, want message type %q",
				index+2,
				item,
				messageType,
			)
		}
	}
}
