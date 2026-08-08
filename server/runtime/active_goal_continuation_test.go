package runtime

import (
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestActiveGoalContinuationUsesOneCanonicalMetaContextSlot(t *testing.T) {
	t.Parallel()
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

func TestReviewerReconstructionPlacesActiveGoalBeforeTranscriptBoundary(t *testing.T) {
	t.Parallel()
	continuation := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation),
		Content:     textutil.Value("continuation"),
	}
	rebuilt, err := buildReviewerRequestMessagesWithBuilder(
		[]llm.Message{
			continuation,
			{Role: llm.RoleUser, Content: textutil.Value("request")},
		},
		newMetaContextBuilder(t.TempDir(), "model", "", config.SkillPolicy{}, time.Unix(0, 0)),
		false,
	)
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}

	continuationIndex, boundaryIndex, transcriptIndex := -1, -1, -1
	continuationCount := 0
	for index, message := range rebuilt {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeActiveGoalContinuation {
			continuationIndex = index
			continuationCount++
		}
		if message.Role == llm.RoleDeveloper && message.MessageType == nil {
			if boundaryIndex >= 0 {
				t.Fatalf("reviewer request contains multiple untyped developer boundaries: %+v", rebuilt)
			}
			boundaryIndex = index
		}
		if message.Role == llm.RoleUser && transcriptIndex < 0 {
			transcriptIndex = index
		}
	}
	if continuationCount != 1 ||
		continuationIndex < 0 ||
		boundaryIndex <= continuationIndex ||
		transcriptIndex <= boundaryIndex {
		t.Fatalf(
			"reviewer active-goal ordering = continuation:%d boundary:%d transcript:%d count:%d",
			continuationIndex,
			boundaryIndex,
			transcriptIndex,
			continuationCount,
		)
	}
}

func TestActiveGoalContinuationProjectsAsDetailDeveloperContext(t *testing.T) {
	t.Parallel()
	entries := VisibleChatEntriesFromMessage(llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation),
		Content:     textutil.Value("continuation"),
	})
	if len(entries) != 1 ||
		entries[0].Visibility != transcript.EntryVisibilityDetail ||
		entries[0].Role != string(transcript.EntryRoleDeveloperContext) ||
		entries[0].MessageType != llm.MessageTypeActiveGoalContinuation {
		t.Fatalf("active-goal continuation entry = %+v", entries)
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
