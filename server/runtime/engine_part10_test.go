package runtime

import (
	"context"
	"errors"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	brand "core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInjectsGlobalAndWorkspaceAgentsBeforeFirstUserMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, brand.ConfigDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalPath, []byte("global instructions"), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}

	workspace := t.TempDir()
	workspacePath := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(workspacePath, []byte("workspace instructions"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}

	storeRoot := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok-1"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok-2"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("second submit: %v", err)
	}

	if len(client.calls) < 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}

	firstReq := client.calls[0]
	if len(requestMessages(firstReq)) < 4 {
		t.Fatalf("expected at least 4 messages in first request, got %d", len(requestMessages(firstReq)))
	}
	envMsg := requestMessages(firstReq)[0]
	if envMsg.Role != llm.RoleDeveloper || !strings.Contains(envMsg.Content, environmentInjectedHeader) {
		t.Fatalf("expected first message to be environment developer injection, got %+v", envMsg)
	}
	if envMsg.MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment message type, got %+v", envMsg)
	}
	if requestMessages(firstReq)[1].Role != llm.RoleDeveloper || !strings.Contains(requestMessages(firstReq)[1].Content, "source: "+globalPath) {
		t.Fatalf("expected second message to be global developer AGENTS injection, got %+v", requestMessages(firstReq)[1])
	}
	if requestMessages(firstReq)[1].MessageType != llm.MessageTypeAgentsMD {
		t.Fatalf("expected global AGENTS message type, got %+v", requestMessages(firstReq)[1])
	}
	if requestMessages(firstReq)[2].Role != llm.RoleDeveloper || !strings.Contains(requestMessages(firstReq)[2].Content, "source: "+workspacePath) {
		t.Fatalf("expected third message to be workspace developer AGENTS injection, got %+v", requestMessages(firstReq)[2])
	}
	if requestMessages(firstReq)[2].MessageType != llm.MessageTypeAgentsMD {
		t.Fatalf("expected workspace AGENTS message type, got %+v", requestMessages(firstReq)[2])
	}
	for _, required := range []string{
		"\nYour model: gpt-5\n",
		"OS: ",
		"Current TZ: ",
		"Date/time: ",
		"Shell: ",
		"CWD: ",
		"CPU arch: ",
	} {
		if !strings.Contains(envMsg.Content, required) {
			t.Fatalf("expected environment message to contain %q, got %q", required, envMsg.Content)
		}
	}
	if requestMessages(firstReq)[3].Role != llm.RoleUser || requestMessages(firstReq)[3].Content != "first" {
		t.Fatalf("expected user message after injections, got %+v", requestMessages(firstReq)[3])
	}

	secondReq := client.calls[1]
	injectedCount := 0
	envInjectedCount := 0
	for _, msg := range requestMessages(secondReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeAgentsMD {
			injectedCount++
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeEnvironment {
			envInjectedCount++
		}
	}
	if injectedCount != 2 {
		t.Fatalf("expected exactly two injected AGENTS developer messages to persist, got %d", injectedCount)
	}
	if envInjectedCount != 1 {
		t.Fatalf("expected exactly one injected environment developer message to persist, got %d", envInjectedCount)
	}
}

func TestFreshChildSessionReinjectsDeveloperContextEvenWhenParentAlreadyInjected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, brand.ConfigDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalPath, []byte("global instructions"), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}

	workspace := t.TempDir()
	workspacePath := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(workspacePath, []byte("workspace instructions"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	storeRoot := t.TempDir()
	parent := mustCreateNamedTestSessionAt(t, storeRoot, "parent", workspace)
	child, err := session.NewLazy(storeRoot, "child", workspace, runtimeTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := session.InitializeChildFromParent(child, parent); err != nil {
		t.Fatalf("initialize child: %v", err)
	}

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, child, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if _, err := eng.SubmitUserMessage(context.Background(), "first child turn"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("expected one model call, got %d", len(client.calls))
	}
	messages := requestMessages(client.calls[0])
	if len(messages) < 5 {
		t.Fatalf("expected environment, AGENTS, and user messages, got %+v", messages)
	}
	if messages[0].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment reinjected first, got %+v", messages[0])
	}
	if messages[1].MessageType != llm.MessageTypeSkills || !strings.Contains(messages[1].Content, "workspace-skill") {
		t.Fatalf("expected skills reinjected after environment, got %+v", messages[1])
	}
	if messages[2].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(messages[2].Content, "source: "+globalPath) {
		t.Fatalf("expected global AGENTS reinjected, got %+v", messages[2])
	}
	if messages[3].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(messages[3].Content, "source: "+workspacePath) {
		t.Fatalf("expected workspace AGENTS reinjected, got %+v", messages[3])
	}
	if messages[4].Role != llm.RoleUser || messages[4].Content != "first child turn" {
		t.Fatalf("expected user message after reinjected context, got %+v", messages[4])
	}
}

func TestInjectsEnvironmentInfoWithoutAnyAgentsFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	storeRoot := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("expected one model call, got %d", len(client.calls))
	}
	req := client.calls[0]
	if len(requestMessages(req)) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(requestMessages(req)))
	}
	if requestMessages(req)[0].Role != llm.RoleDeveloper || !strings.Contains(requestMessages(req)[0].Content, environmentInjectedHeader) {
		t.Fatalf("expected first message to be environment injection, got %+v", requestMessages(req)[0])
	}
	if !strings.Contains(requestMessages(req)[0].Content, "\nYour model: gpt-5\n") {
		t.Fatalf("expected environment injection to include labeled model identifier, got %+v", requestMessages(req)[0])
	}
	if requestMessages(req)[1].Role != llm.RoleUser || requestMessages(req)[1].Content != "first" {
		t.Fatalf("expected user message after environment injection, got %+v", requestMessages(req)[1])
	}
}

func TestInjectsSkillsContextBeforeEnvironmentAndPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	homeSkillPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "home-skill", "from home")
	workspaceSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	storeRoot := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok-1"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok-2"}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("second submit: %v", err)
	}

	if len(client.calls) != 2 {
		t.Fatalf("expected two model calls, got %d", len(client.calls))
	}

	firstReq := client.calls[0]
	skillsIdx := -1
	envIdx := -1
	userIdx := -1
	for i, msg := range requestMessages(firstReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeSkills {
			skillsIdx = i
			if !strings.Contains(msg.Content, "- home-skill: "+filepath.ToSlash(homeSkillPath)+" . from home") {
				t.Fatalf("expected injected skills context to include home skill entry, got %q", msg.Content)
			}
			if !strings.Contains(msg.Content, "- workspace-skill: "+filepath.ToSlash(workspaceSkillPath)+" . from workspace") {
				t.Fatalf("expected injected skills context to include workspace skill entry, got %q", msg.Content)
			}
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeEnvironment {
			envIdx = i
		}
		if msg.Role == llm.RoleUser && msg.Content == "first" {
			userIdx = i
		}
	}
	if skillsIdx < 0 {
		t.Fatalf("expected injected skills developer message in first request, messages=%+v", requestMessages(firstReq))
	}
	if envIdx < 0 {
		t.Fatalf("expected injected environment developer message in first request, messages=%+v", requestMessages(firstReq))
	}
	if userIdx < 0 {
		t.Fatalf("expected first user message in first request, messages=%+v", requestMessages(firstReq))
	}
	if !(envIdx < skillsIdx && skillsIdx < userIdx) {
		t.Fatalf("expected environment -> skills -> user ordering, got env=%d skills=%d user=%d", envIdx, skillsIdx, userIdx)
	}

	secondReq := client.calls[1]
	skillsInjectedCount := 0
	for _, msg := range requestMessages(secondReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeSkills {
			skillsInjectedCount++
		}
	}
	if skillsInjectedCount != 1 {
		t.Fatalf("expected exactly one injected skills message to persist, got %d", skillsInjectedCount)
	}
}

func TestDisabledSkillsAreNotInjectedIntoNewSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	homeSkillPath := writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "home-skill", "from home")
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "Workspace Skill", "from workspace")

	storeRoot := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: llm.Usage{WindowTokens: 200000}}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:          "gpt-5",
		DisabledSkills: map[string]bool{"workspace skill": true},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one model call, got %d", len(client.calls))
	}

	for _, msg := range requestMessages(client.calls[0]) {
		if msg.Role != llm.RoleDeveloper || msg.MessageType != llm.MessageTypeSkills {
			continue
		}
		if strings.Contains(msg.Content, "Workspace Skill") {
			t.Fatalf("did not expect disabled workspace skill in injected skills context, got %q", msg.Content)
		}
		if !strings.Contains(msg.Content, "- home-skill: "+filepath.ToSlash(homeSkillPath)+" . from home") {
			t.Fatalf("expected enabled home skill to remain, got %q", msg.Content)
		}
		return
	}
	t.Fatalf("expected skills developer message in first request, messages=%+v", requestMessages(client.calls[0]))
}

func TestBrokenSymlinkedSkillsAreSkippedAndWarnedInTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	validSkillPath := writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "valid-skill"), "valid-skill", "from workspace")
	brokenLinkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "broken-skill")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-skill-dir"), brokenLinkPath); err != nil {
		t.Fatalf("symlink broken skill dir: %v", err)
	}

	storeRoot := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: llm.Usage{WindowTokens: 200000}}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one model call, got %d", len(client.calls))
	}

	foundSkills := false
	for _, msg := range requestMessages(client.calls[0]) {
		if msg.Role != llm.RoleDeveloper || msg.MessageType != llm.MessageTypeSkills {
			continue
		}
		foundSkills = true
		if !strings.Contains(msg.Content, "- valid-skill: "+filepath.ToSlash(validSkillPath)+" . from workspace") {
			t.Fatalf("expected valid skill to remain injected, got %q", msg.Content)
		}
		if strings.Contains(msg.Content, "broken-skill") {
			t.Fatalf("did not expect broken symlinked skill in injected context, got %q", msg.Content)
		}
	}
	if !foundSkills {
		t.Fatalf("expected skills developer message in first request, messages=%+v", requestMessages(client.calls[0]))
	}
	for _, msg := range requestMessages(client.calls[0]) {
		if strings.Contains(msg.Content, "Skipped skill \"broken-skill\"") {
			t.Fatalf("expected broken skill warning to stay out of model request, got %+v", requestMessages(client.calls[0]))
		}
	}

	snapshot := eng.ChatSnapshot()
	foundWarning := false
	for _, entry := range snapshot.Entries {
		if entry.Role != "warning" || entry.Visibility != transcript.EntryVisibilityOngoing {
			continue
		}
		if strings.Contains(entry.Text, "Skipped skill \"broken-skill\"") && strings.Contains(entry.Text, filepath.ToSlash(brokenLinkPath)) {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected broken skill warning in transcript, entries=%+v", snapshot.Entries)
	}
}

func TestEnvironmentContextMessageIncludesLabeledModelIdentifier(t *testing.T) {
	workspace := t.TempDir()
	msg, err := environmentContextMessage(workspace, "gpt-5.3-codex", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("environmentContextMessage: %v", err)
	}
	if !strings.Contains(msg, "\nYour model: gpt-5.3-codex\n") {
		t.Fatalf("expected environment message to include labeled model identifier, got %q", msg)
	}
	if strings.Contains(msg, "Your model: gpt-5.3-codex high") {
		t.Fatalf("expected environment message to exclude thinking level from model identifier, got %q", msg)
	}
}

func TestEnvironmentContextMessageUsesWorkspaceRootForCWD(t *testing.T) {
	workspace := t.TempDir()
	msg, err := environmentContextMessage(workspace, "gpt-5.3-codex", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("environmentContextMessage: %v", err)
	}
	if !strings.Contains(msg, "\nCWD: "+workspace+"\n") {
		t.Fatalf("expected environment message cwd to use workspace root %q, got %q", workspace, msg)
	}
}

func TestEnvironmentContextMessageFallsBackToProcessCWDWhenWorkspaceRootMissing(t *testing.T) {
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	msg, err := environmentContextMessage("", "gpt-5.3-codex", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("environmentContextMessage: %v", err)
	}
	if !strings.Contains(msg, "\nCWD: "+processCWD+"\n") {
		t.Fatalf("expected environment message cwd to fall back to process cwd %q, got %q", processCWD, msg)
	}
}

func TestEnvironmentContextMessageRejectsEmptyModel(t *testing.T) {
	workspace := t.TempDir()
	if _, err := environmentContextMessage(workspace, "", time.Unix(0, 0).UTC()); !errors.Is(err, errEnvironmentContextModelRequired) {
		t.Fatalf("expected errEnvironmentContextModelRequired, got %v", err)
	}
}

func TestNewRejectsEmptyModel(t *testing.T) {
	storeRoot := t.TempDir()
	workspace := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	_, err := New(store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})
	if !errors.Is(err, ErrModelRequired) {
		t.Fatalf("expected ErrModelRequired, got %v", err)
	}
}

func TestSubmitInjectsEnvironmentLineWithLabeledModelIdentifier(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	storeRoot := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, storeRoot, "ws", workspace)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok"},
		OutputItems: []llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseFinal,
			Content: "ok",
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5.3-codex",
		ThinkingLevel:         "high",
		AutoCompactTokenLimit: 1_000_000_000,
		CompactionMode:        "local",
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("expected one model call, got %d", len(client.calls))
	}
	req := client.calls[0]
	if len(requestMessages(req)) < 2 {
		t.Fatalf("expected environment and user messages, got %d", len(requestMessages(req)))
	}
	envMsg := requestMessages(req)[0]
	if envMsg.Role != llm.RoleDeveloper || envMsg.MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected first request message to be environment context, got %+v", envMsg)
	}
	if !strings.Contains(envMsg.Content, "\nYour model: gpt-5.3-codex\n") {
		t.Fatalf("expected environment context to contain labeled model identifier, got %q", envMsg.Content)
	}
	if !strings.Contains(envMsg.Content, "\nCWD: "+workspace+"\n") {
		t.Fatalf("expected environment context cwd to use session workspace root %q, got %q", workspace, envMsg.Content)
	}
	if strings.Contains(envMsg.Content, "Your model: gpt-5.3-codex high") {
		t.Fatalf("expected environment context to exclude thinking level from model identifier, got %q", envMsg.Content)
	}
}

func TestManualCompactionReinjectsHeadlessEnterOnlyWhileHeadlessRemainsActive(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := store.SetHeadlessActive(true); err != nil {
		t.Fatalf("mark headless active: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "continue"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	headlessCount := 0
	exitCount := 0
	for _, message := range messages {
		switch message.MessageType {
		case llm.MessageTypeHeadlessMode:
			headlessCount++
		case llm.MessageTypeHeadlessModeExit:
			exitCount++
		}
	}
	if headlessCount != 1 {
		t.Fatalf("expected exactly one reinjected headless enter after compaction, got %d messages=%+v", headlessCount, messages)
	}
	if exitCount != 0 {
		t.Fatalf("did not expect headless exit after compaction while still headless, got %d messages=%+v", exitCount, messages)
	}
}

func TestManualCompactionDoesNotReinjectHeadlessEnterAfterExit(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless mode instructions"}})); err != nil {
		t.Fatalf("append headless mode: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessModeExit, Content: "interactive mode instructions"}})); err != nil {
		t.Fatalf("append headless exit: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "continue"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	for _, message := range messages {
		if message.MessageType == llm.MessageTypeHeadlessMode {
			t.Fatalf("did not expect headless enter reinjection after exit, got messages=%+v", messages)
		}
		if message.MessageType == llm.MessageTypeHeadlessModeExit {
			t.Fatalf("did not expect historical headless exit in the new compaction list, got messages=%+v", messages)
		}
	}
}

func TestSubmitUserMessageInjectsHeadlessEnterPromptWhenContinuingRegularSessionInHeadlessMode(t *testing.T) {
	prevHeadlessPrompt := prompts.HeadlessModePrompt
	prompts.HeadlessModePrompt = "headless mode instructions"
	defer func() {
		prompts.HeadlessModePrompt = prevHeadlessPrompt
	}()

	store := mustCreateTestSession(t)

	interactiveClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "interactive-ok"},
		OutputItems: []llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseFinal,
			Content: "interactive-ok",
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	interactiveEngine := mustNewTestEngine(t, store, interactiveClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if _, err := interactiveEngine.SubmitUserMessage(context.Background(), "regular start"); err != nil {
		t.Fatalf("interactive submit: %v", err)
	}

	headlessClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "headless-ok-1"},
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "headless-ok-1",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "headless-ok-2"},
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "headless-ok-2",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	headlessEngine := mustNewTestEngine(t, store, headlessClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", HeadlessMode: true})

	if _, err := headlessEngine.SubmitUserMessage(context.Background(), "continue headlessly"); err != nil {
		t.Fatalf("headless submit 1: %v", err)
	}
	if _, err := headlessEngine.SubmitUserMessage(context.Background(), "continue headlessly again"); err != nil {
		t.Fatalf("headless submit 2: %v", err)
	}

	if len(headlessClient.calls) != 2 {
		t.Fatalf("expected two headless calls, got %d", len(headlessClient.calls))
	}
	firstReq := headlessClient.calls[0]
	headlessIdx := -1
	userIdx := -1
	for i, msg := range requestMessages(firstReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessMode {
			headlessIdx = i
		}
		if msg.Role == llm.RoleUser && msg.Content == "continue headlessly" {
			userIdx = i
		}
	}
	if headlessIdx < 0 {
		t.Fatalf("expected enter prompt when switching regular session into headless mode, messages=%+v", requestMessages(firstReq))
	}
	if userIdx < 0 || headlessIdx >= userIdx {
		t.Fatalf("expected headless enter prompt before user message, headless=%d user=%d messages=%+v", headlessIdx, userIdx, requestMessages(firstReq))
	}
	secondReq := headlessClient.calls[1]
	headlessCount := 0
	for _, msg := range requestMessages(secondReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessMode {
			headlessCount++
		}
	}
	if headlessCount != 1 {
		t.Fatalf("expected exactly one persisted headless enter marker, got %d messages=%+v", headlessCount, requestMessages(secondReq))
	}
}

func TestSubmitUserMessageInjectsHeadlessExitPromptOnFirstInteractiveTurn(t *testing.T) {
	prevHeadlessPrompt := prompts.HeadlessModePrompt
	prevExitPrompt := prompts.HeadlessModeExitPrompt
	prompts.HeadlessModePrompt = "headless mode instructions"
	prompts.HeadlessModeExitPrompt = "interactive mode instructions"
	defer func() {
		prompts.HeadlessModePrompt = prevHeadlessPrompt
		prompts.HeadlessModeExitPrompt = prevExitPrompt
	}()

	store := mustCreateTestSession(t)

	headlessClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "headless-ok"},
		OutputItems: []llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseFinal,
			Content: "headless-ok",
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	headlessEngine := mustNewTestEngine(t, store, headlessClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", HeadlessMode: true})
	if _, err := headlessEngine.SubmitUserMessage(context.Background(), "run headless"); err != nil {
		t.Fatalf("headless submit: %v", err)
	}

	interactiveClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "interactive-ok-1"},
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "interactive-ok-1",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "interactive-ok-2"},
			OutputItems: []llm.ResponseItem{{
				Type:    llm.ResponseItemTypeMessage,
				Role:    llm.RoleAssistant,
				Phase:   llm.MessagePhaseFinal,
				Content: "interactive-ok-2",
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	interactiveEngine := mustNewTestEngine(t, store, interactiveClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := interactiveEngine.SubmitUserMessage(context.Background(), "continue interactively"); err != nil {
		t.Fatalf("interactive submit 1: %v", err)
	}
	if _, err := interactiveEngine.SubmitUserMessage(context.Background(), "continue again"); err != nil {
		t.Fatalf("interactive submit 2: %v", err)
	}

	if len(interactiveClient.calls) != 2 {
		t.Fatalf("expected two interactive model calls, got %d", len(interactiveClient.calls))
	}

	firstReq := interactiveClient.calls[0]
	headlessIdx := -1
	exitIdx := -1
	userIdx := -1
	for i, msg := range requestMessages(firstReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessMode {
			headlessIdx = i
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessModeExit {
			exitIdx = i
		}
		if msg.Role == llm.RoleUser && msg.Content == "continue interactively" {
			userIdx = i
		}
	}
	if headlessIdx < 0 {
		t.Fatalf("expected prior headless prompt in first interactive request, messages=%+v", requestMessages(firstReq))
	}
	if exitIdx < 0 {
		t.Fatalf("expected exit prompt in first interactive request, messages=%+v", requestMessages(firstReq))
	}
	if userIdx < 0 {
		t.Fatalf("expected interactive user message in first request, messages=%+v", requestMessages(firstReq))
	}
	if !(headlessIdx < exitIdx && exitIdx < userIdx) {
		t.Fatalf("expected headless -> exit -> user ordering, got headless=%d exit=%d user=%d", headlessIdx, exitIdx, userIdx)
	}

	secondReq := interactiveClient.calls[1]
	exitCount := 0
	for _, msg := range requestMessages(secondReq) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessModeExit {
			exitCount++
		}
	}
	if exitCount != 1 {
		t.Fatalf("expected exactly one persisted exit prompt in later requests, got %d messages=%+v", exitCount, requestMessages(secondReq))
	}
}

func TestSubmitUserMessageDoesNotInjectHeadlessExitPromptForNormalSession(t *testing.T) {
	prevExitPrompt := prompts.HeadlessModeExitPrompt
	prompts.HeadlessModeExitPrompt = "interactive mode instructions"
	defer func() {
		prompts.HeadlessModeExitPrompt = prevExitPrompt
	}()

	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok"},
		OutputItems: []llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseFinal,
			Content: "ok",
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "plain user"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one model call, got %d", len(client.calls))
	}
	for _, msg := range requestMessages(client.calls[0]) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeHeadlessModeExit {
			t.Fatalf("did not expect headless exit prompt in normal session, messages=%+v", requestMessages(client.calls[0]))
		}
	}
}
