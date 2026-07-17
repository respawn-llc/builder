package runtime

import (
	"context"
	"errors"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	brand "core/shared/config"
	"core/shared/sessioncontract"
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

	firstMessages := requestMessages(client.calls[0])
	if len(firstMessages) < 4 {
		t.Fatalf("expected environment, AGENTS, and user messages, got %+v", firstMessages)
	}
	for index, want := range []llm.Message{
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeAgentsMD, SourcePath: globalPath},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeAgentsMD, SourcePath: workspacePath},
		{Role: llm.RoleUser, Content: "first"},
	} {
		got := firstMessages[index]
		if got.Role != want.Role || got.MessageType != want.MessageType || got.SourcePath != want.SourcePath || (want.Content != "" && got.Content != want.Content) {
			t.Fatalf("message %d = %+v, want role=%q type=%q source=%q content=%q", index, got, want.Role, want.MessageType, want.SourcePath, want.Content)
		}
	}

	injectedCount := 0
	envInjectedCount := 0
	for _, msg := range requestMessages(client.calls[1]) {
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
	child, err := session.NewLazy(storeRoot, "child", workspace, sessioncontract.SessionCategorySubagent, runtimeTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourceParentAgent, session.ChildContextOptions{
		InheritLockedContract: true,
		InheritContinuation:   true,
	}); err != nil {
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
	if messages[2].MessageType != llm.MessageTypeAgentsMD || messages[2].SourcePath != globalPath {
		t.Fatalf("expected global AGENTS reinjected, got %+v", messages[2])
	}
	if messages[3].MessageType != llm.MessageTypeAgentsMD || messages[3].SourcePath != workspacePath {
		t.Fatalf("expected workspace AGENTS reinjected, got %+v", messages[3])
	}
	if messages[4].Role != llm.RoleUser || messages[4].Content != "first child turn" {
		t.Fatalf("expected user message after reinjected context, got %+v", messages[4])
	}
}

func TestInjectsSkillsContextAfterEnvironmentAndPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(home, brand.ConfigDirName, "skills", "home-skill"), "home-skill", "from home")
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

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

	firstMessages := requestMessages(client.calls[0])
	skillsIdx := -1
	envIdx := -1
	userIdx := -1
	for i, msg := range firstMessages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeSkills {
			skillsIdx = i
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeEnvironment {
			envIdx = i
		}
		if msg.Role == llm.RoleUser && msg.Content == "first" {
			userIdx = i
		}
	}
	if skillsIdx < 0 {
		t.Fatalf("expected injected skills developer message in first request, messages=%+v", firstMessages)
	}
	if envIdx < 0 {
		t.Fatalf("expected injected environment developer message in first request, messages=%+v", firstMessages)
	}
	if userIdx < 0 {
		t.Fatalf("expected first user message in first request, messages=%+v", firstMessages)
	}
	if !(envIdx < skillsIdx && skillsIdx < userIdx) {
		t.Fatalf("expected environment -> skills -> user ordering, got env=%d skills=%d user=%d", envIdx, skillsIdx, userIdx)
	}

	skillsInjectedCount := 0
	for _, msg := range requestMessages(client.calls[1]) {
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
		Model:       "gpt-5",
		SkillPolicy: skillPolicyWithDisabled("workspace skill"),
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
	brokenLinkPath := filepath.Join(workspace, brand.ConfigDirName, "skills", "broken-skill")
	if err := os.MkdirAll(filepath.Dir(brokenLinkPath), 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-skill-dir"), brokenLinkPath); err != nil {
		t.Fatalf("symlink broken skill dir: %v", err)
	}

	store := mustCreateNamedTestSessionAt(t, t.TempDir(), "ws", workspace)
	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	for _, msg := range requestMessages(client.calls[0]) {
		if msg.MessageType == llm.MessageTypeSkills {
			t.Fatalf("broken skill produced model-visible skills context: %+v", requestMessages(client.calls[0]))
		}
	}

	warnings := 0
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role != string(transcript.EntryRoleWarning) {
			continue
		}
		warnings++
		if entry.Visibility != transcript.EntryVisibilityOngoing {
			t.Fatalf("warning visibility = %q, want ongoing", entry.Visibility)
		}
	}
	if warnings != 1 {
		t.Fatalf("warning entries = %d, want 1; entries=%+v", warnings, eng.ChatSnapshot().Entries)
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
	store := mustCreateNamedTestSessionAt(t, t.TempDir(), "ws", workspace)
	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		Model:                 "gpt-5.3-codex",
		ThinkingLevel:         "high",
		AutoCompactTokenLimit: 1_000_000_000,
		CompactionMode:        "local",
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	assertModelCallCount(t, client, 1)
	messages := requestMessages(client.calls[0])
	if len(messages) < 2 {
		t.Fatalf("expected environment and user messages, got %d", len(messages))
	}
	envMsg := messages[0]
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

func TestManualCompactionReinjectsOnlyActiveHeadlessState(t *testing.T) {
	tests := []struct {
		name              string
		active            bool
		persistedMessages []llm.Message
		wantHeadless      int
	}{
		{name: "active", active: true, wantHeadless: 1},
		{
			name: "exited",
			persistedMessages: []llm.Message{
				{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless mode instructions"},
				{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessModeExit, Content: "interactive mode instructions"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			client := &fakeClient{responses: []llm.Response{{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
				Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
			}}}
			eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
			if test.active {
				if err := store.SetHeadlessActive(true); err != nil {
					t.Fatalf("mark headless active: %v", err)
				}
			}
			for _, message := range append(test.persistedMessages, llm.Message{Role: llm.RoleUser, Content: "continue"}) {
				if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{message})); err != nil {
					t.Fatalf("append message: %v", err)
				}
			}
			if err := eng.CompactContext(context.Background(), ""); err != nil {
				t.Fatalf("compact: %v", err)
			}

			headlessCount := 0
			exitCount := 0
			messages := eng.transcriptRuntimeState().SnapshotMessages()
			for _, message := range messages {
				switch message.MessageType {
				case llm.MessageTypeHeadlessMode:
					headlessCount++
				case llm.MessageTypeHeadlessModeExit:
					exitCount++
				}
			}
			if headlessCount != test.wantHeadless || exitCount != 0 {
				t.Fatalf("headless/exit counts = %d/%d, want %d/0; messages=%+v", headlessCount, exitCount, test.wantHeadless, messages)
			}
		})
	}
}

func TestSubmitUserMessagePersistsHeadlessModeTransitions(t *testing.T) {
	prevHeadlessPrompt := prompts.HeadlessModePrompt
	prevExitPrompt := prompts.HeadlessModeExitPrompt
	prompts.HeadlessModePrompt = "headless mode instructions"
	prompts.HeadlessModeExitPrompt = "interactive mode instructions"
	defer func() {
		prompts.HeadlessModePrompt = prevHeadlessPrompt
		prompts.HeadlessModeExitPrompt = prevExitPrompt
	}()

	tests := []struct {
		name           string
		seedHeadless   bool
		targetHeadless bool
		messageTypes   []llm.MessageType
	}{
		{
			name:           "enter headless",
			targetHeadless: true,
			messageTypes:   []llm.MessageType{llm.MessageTypeHeadlessMode},
		},
		{
			name:         "exit headless",
			seedHeadless: true,
			messageTypes: []llm.MessageType{llm.MessageTypeHeadlessMode, llm.MessageTypeHeadlessModeExit},
		},
		{
			name: "remain interactive",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			seedClient := &fakeClient{responses: []llm.Response{finalOutputItemResponse("seeded")}}
			seedEngine := mustNewExecTestEngine(t, store, seedClient, Config{Model: "gpt-5", HeadlessMode: test.seedHeadless})
			if _, err := seedEngine.SubmitUserMessage(context.Background(), "seed"); err != nil {
				t.Fatalf("seed submit: %v", err)
			}

			client := &fakeClient{responses: []llm.Response{
				finalOutputItemResponse("transitioned"),
				finalOutputItemResponse("continued"),
			}}
			eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5", HeadlessMode: test.targetHeadless})
			if _, err := eng.SubmitUserMessage(context.Background(), "transition"); err != nil {
				t.Fatalf("transition submit: %v", err)
			}
			if _, err := eng.SubmitUserMessage(context.Background(), "again"); err != nil {
				t.Fatalf("second submit: %v", err)
			}
			assertModelCallCount(t, client, 2)

			firstMessages := requestMessages(client.calls[0])
			secondMessages := requestMessages(client.calls[1])
			if len(test.messageTypes) == 0 {
				for _, messages := range [][]llm.Message{firstMessages, secondMessages} {
					for _, message := range messages {
						if message.MessageType == llm.MessageTypeHeadlessMode || message.MessageType == llm.MessageTypeHeadlessModeExit {
							t.Fatalf("unchanged interactive session gained headless transition: %+v", messages)
						}
					}
				}
				return
			}

			assertMessageTypesInOrder(t, firstMessages, test.messageTypes...)
			assertMessageTypesInOrder(t, secondMessages, test.messageTypes...)
			transitionIndex := -1
			userIndex := -1
			for index, message := range firstMessages {
				if message.Role == llm.RoleDeveloper && message.MessageType == test.messageTypes[len(test.messageTypes)-1] {
					transitionIndex = index
				}
				if message.Role == llm.RoleUser && message.Content == "transition" {
					userIndex = index
				}
			}
			if transitionIndex < 0 || userIndex < 0 || transitionIndex >= userIndex {
				t.Fatalf("transition/user indexes = %d/%d, want transition before current user; messages=%+v", transitionIndex, userIndex, firstMessages)
			}
		})
	}
}
