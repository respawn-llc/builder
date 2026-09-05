package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestSkillToggleChangesOnlySkillContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeTestSkill(t, filepath.Join(workspace, config.ConfigDirName, "skills", "prompting"), "prompting", "agent assignments")

	requests := make([]llm.Request, 0, 2)
	contexts := make([][]llm.Message, 0, 2)
	projections := make([][]llm.Message, 0, 2)
	for _, enabled := range []bool{true, false} {
		policy := config.ResolveSkillPolicy(config.Settings{
			SkillToggles: map[string]bool{"prompting": enabled},
		})
		catalog := config.Settings{MaxSubagentDepth: 2}
		client := &fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		}}}
		engine := mustNewTestEngine(t, mustCreateNamedTestSession(t, "ws", workspace), client,
			newTestToolRegistry(t, tools.HandlerRegistration{
				ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
			}), Config{
				Model:                   "gpt-5",
				EnabledTools:            []toolspec.ID{toolspec.ToolExecCommand},
				SkillPolicy:             policy,
				SubagentCatalogSettings: catalog,
			})
		if _, err := engine.SubmitUserMessage(context.Background(), "work"); err != nil {
			t.Fatalf("submit with skill enabled=%t: %v", enabled, err)
		}
		if len(client.calls) != 1 {
			t.Fatalf("model calls = %d, want one", len(client.calls))
		}
		request := client.calls[0]
		requests = append(requests, request)
		var otherContext []llm.Message
		skillsPresent := false
		rolesPresent := false
		for _, message := range requestMessages(request) {
			if message.MessageType != nil {
				switch *message.MessageType {
				case llm.MessageTypeSkills:
					skillsPresent = true
					continue
				case llm.MessageTypeSubagents:
					rolesPresent = true
				case llm.MessageTypeEnvironment:
					// Live environment timestamps differ between Sessions.
					// Compare the entire context with a fixed time below.
					continue
				}
			}
			otherContext = append(otherContext, message)
		}
		if skillsPresent != enabled {
			t.Fatalf("skill context present=%t with enabled=%t", skillsPresent, enabled)
		}
		if !rolesPresent {
			t.Fatal("callable role context is missing")
		}
		contexts = append(contexts, otherContext)

		builder := newMetaContextBuilder(workspace, "gpt-5", "", policy, time.Unix(0, 0)).
			withSubagents(catalog, []toolspec.ID{toolspec.ToolExecCommand})
		projection, err := builder.Build(baseMetaContextBuildOptions(false))
		if err != nil {
			t.Fatalf("build fixed-time context: %v", err)
		}
		var fixedContext []llm.Message
		for _, message := range projection.Projection().Messages() {
			if message.MessageType != nil && *message.MessageType == llm.MessageTypeSkills {
				continue
			}
			fixedContext = append(fixedContext, message)
		}
		projections = append(projections, fixedContext)
	}
	if requests[0].SystemPrompt != requests[1].SystemPrompt {
		t.Fatal("skill toggle changed the system prompt")
	}
	if !reflect.DeepEqual(contexts[0], contexts[1]) {
		t.Fatalf("skill toggle changed other context:\nenabled=%+v\ndisabled=%+v", contexts[0], contexts[1])
	}
	if !reflect.DeepEqual(requests[0].Tools, requests[1].Tools) {
		t.Fatal("skill toggle changed tool definitions")
	}
	if !reflect.DeepEqual(projections[0], projections[1]) {
		t.Fatal("skill toggle changed fixed-time context outside the skill catalog")
	}
}
