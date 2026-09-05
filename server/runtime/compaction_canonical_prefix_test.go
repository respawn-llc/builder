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
		Checkpoint: llm.ResponseItem{
			Type:             llm.ResponseItemTypeCompaction,
			ID:               textutil.Value(checkpointID),
			EncryptedContent: textutil.Value("encrypted"),
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
	if len(items) < 8 {
		t.Fatalf("canonical replacement items = %+v, want remote output and canonical context", items)
	}
	assertCompactionReplacementOrder(t, items, false)
	items = items[1:]
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
	if items[4].Type != llm.ResponseItemTypeCompaction ||
		items[4].ID == nil ||
		*items[4].ID != checkpointID {
		t.Fatalf("canonical replacement checkpoint = %+v, want identity %q", items[4], checkpointID)
	}
	if items[5].Type != llm.ResponseItemTypeMessage ||
		items[5].MessageType == nil ||
		*items[5].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("canonical replacement environment = %+v, want volatile suffix", items[5])
	}
	if items[6].Type != llm.ResponseItemTypeMessage ||
		items[6].MessageType == nil ||
		*items[6].MessageType != llm.MessageTypeCompactionPreservedUserMessage {
		t.Fatalf("canonical replacement carryover = %+v, want preserved user message", items[6])
	}
}

func assertCompactionReplacementOrder(t *testing.T, items []llm.ResponseItem, wantFutureAgentMessage bool) {
	t.Helper()
	var compactedIndex *int
	var environmentIndex *int
	var carryoverIndex *int
	var futureIndex *int
	var nativeReminderIndex *int
	for index, item := range items {
		if item.Type == llm.ResponseItemTypeCompaction ||
			(item.Type == llm.ResponseItemTypeMessage &&
				item.MessageType != nil &&
				*item.MessageType == llm.MessageTypeCompactionSummary) {
			if compactedIndex != nil {
				t.Fatalf("replacement contains multiple compacted outputs: %+v", items)
			}
			compactedIndex = textutil.Value(index)
		}
		if item.Type == llm.ResponseItemTypeMessage && item.MessageType == nil &&
			item.Role != nil && *item.Role == llm.RoleDeveloper {
			if nativeReminderIndex != nil {
				t.Fatalf("replacement contains duplicate native reminders: %+v", items)
			}
			nativeReminderIndex = textutil.Value(index)
			if item.Content == nil {
				t.Fatalf("native reminder must contain continuation guidance: %+v", item)
			}
		}
		if item.Type != llm.ResponseItemTypeMessage || item.MessageType == nil {
			continue
		}
		switch *item.MessageType {
		case llm.MessageTypeEnvironment:
			if environmentIndex != nil {
				t.Fatalf("replacement contains multiple Environment messages: %+v", items)
			}
			environmentIndex = textutil.Value(index)
		case llm.MessageTypeCompactionPreservedUserMessage:
			if carryoverIndex != nil {
				t.Fatalf("replacement contains duplicate carryover messages: %+v", items)
			}
			carryoverIndex = textutil.Value(index)
		case llm.MessageTypeHandoffFutureMessage:
			if futureIndex != nil {
				t.Fatalf("replacement contains duplicate future-agent messages: %+v", items)
			}
			futureIndex = textutil.Value(index)
		}
	}
	if compactedIndex == nil || environmentIndex == nil || carryoverIndex == nil {
		t.Fatalf(
			"replacement must contain stable context, one compacted output, Environment, and one carryover: %+v",
			items,
		)
	}
	wantCarryoverIndex := *environmentIndex + 1
	if items[*compactedIndex].Type == llm.ResponseItemTypeCompaction {
		if nativeReminderIndex == nil || *nativeReminderIndex != 0 {
			t.Fatalf("native reminder must be the first ordinary developer message: %+v", items)
		}
	} else if nativeReminderIndex != nil {
		t.Fatalf("local compaction must not contain a native reminder: %+v", items)
	}
	if *environmentIndex != *compactedIndex+1 || *carryoverIndex != wantCarryoverIndex {
		t.Fatalf(
			"replacement must retain compacted output -> Environment -> carryover ordering: %+v",
			items,
		)
	}
	for index := 0; index < *compactedIndex; index++ {
		item := items[index]
		if item.Type != llm.ResponseItemTypeMessage || item.MessageType == nil {
			continue
		}
		switch *item.MessageType {
		case llm.MessageTypeEnvironment,
			llm.MessageTypeCompactionPreservedUserMessage,
			llm.MessageTypeHandoffFutureMessage:
			t.Fatalf("post-compaction context appeared before compacted output: %+v", items)
		}
	}
	if wantFutureAgentMessage {
		if futureIndex == nil || *futureIndex != *carryoverIndex+1 || *futureIndex != len(items)-1 {
			t.Fatalf(
				"replacement order must end with carryover -> future-agent message with no interleaving: %+v",
				items,
			)
		}
	} else if futureIndex != nil {
		t.Fatalf("unexpected future-agent message in replacement: %+v", items)
	} else if *carryoverIndex != len(items)-1 {
		t.Fatalf("replacement must end with carryover when no future-agent message is requested: %+v", items)
	}
}
