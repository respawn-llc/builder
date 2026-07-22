package runtime

import (
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/textutil"
)

func TestActiveGoalContinuationUsesOneCanonicalMetaContextSlot(t *testing.T) {
	result, err := newMetaContextBuilder(
		t.TempDir(),
		"",
		"",
		config.SkillPolicy{},
		time.Unix(0, 0),
	).Build(metaContextBuildOptions{
		ExistingMessages: []llm.Message{
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation),
				Content:     textutil.Value("first continuation"),
			},
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation),
				Content:     textutil.Value("duplicate continuation"),
			},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorktreeMode), Content: textutil.Value("worktree")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), Content: textutil.Value("workflow")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeHeadlessMode), Content: textutil.Value("headless")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment")},
		},
		ActiveGoal: &session.GoalState{
			Objective: "goal",
			Status:    session.GoalStatusActive,
		},
	})
	if err != nil {
		t.Fatalf("build meta context: %v", err)
	}
	if len(result.ActiveGoalContinuation) != 1 ||
		result.ActiveGoalContinuation[0].MessageType == nil ||
		*result.ActiveGoalContinuation[0].MessageType != llm.MessageTypeActiveGoalContinuation {
		t.Fatalf("active-goal continuation slot = %+v, want exactly one typed message", result.ActiveGoalContinuation)
	}

	want := []llm.MessageType{
		llm.MessageTypeEnvironment,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeWorktreeMode,
	}
	got := metaContextMessageTypes(result.OrderedMetaMessages())
	if len(got) != len(want) {
		t.Fatalf("ordered meta message types = %+v, want %+v", got, want)
	}
	for index, messageType := range want {
		if got[index] != messageType {
			t.Fatalf("ordered meta type[%d] = %q, want %q", index, got[index], messageType)
		}
	}
}

func metaContextMessageTypes(messages []llm.Message) []llm.MessageType {
	types := make([]llm.MessageType, 0, len(messages))
	for _, message := range messages {
		if message.MessageType != nil {
			types = append(types, *message.MessageType)
		}
	}
	return types
}
