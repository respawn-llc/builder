package runtime

import (
	"reflect"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestActiveGoalContinuationUsesOneCanonicalMetaContextSlot(t *testing.T) {
	first, second := llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("preserved continuation"), CompactContent: textutil.Value(clientui.GoalNudgeCompactLabel)}, llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("duplicate continuation"), CompactContent: textutil.Value(clientui.GoalNudgeCompactLabel)}
	result, err := newMetaContextBuilder(t.TempDir(), "", "", config.SkillPolicy{}, time.Unix(0, 0)).Build(metaContextBuildOptions{ExistingMessages: []llm.Message{
		first, second,
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorktreeMode), Content: textutil.Value("worktree")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), Content: textutil.Value("workflow")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeHeadlessMode), Content: textutil.Value("headless")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment")},
	}, ActiveGoal: &session.GoalState{Objective: "new mutable goal", Status: session.GoalStatusActive}})
	if err != nil || !reflect.DeepEqual(result.ActiveGoalContinuation, []llm.Message{first}) {
		t.Fatalf("active-goal continuation slot = %+v, err=%v", result.ActiveGoalContinuation, err)
	}
	assertMessageTypesInOrder(t, result.OrderedMetaMessages(), llm.MessageTypeEnvironment, llm.MessageTypeHeadlessMode, llm.MessageTypeActiveGoalContinuation, llm.MessageTypeWorkflowMode, llm.MessageTypeWorktreeMode)
	meta, ordinary := splitMetaContextMessages([]llm.Message{first, {Role: llm.RoleUser, Content: textutil.Value("request")}})
	if !reflect.DeepEqual(meta, []llm.Message{first}) || len(ordinary) != 1 || ordinary[0].Role != llm.RoleUser {
		t.Fatalf("classified meta=%+v ordinary=%+v, want continuation and user request", meta, ordinary)
	}
}

func TestReviewerReconstructionPreservesActiveGoalContinuationBeforeBoundary(t *testing.T) {
	continuation := llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("preserved active-goal continuation"), CompactContent: textutil.Value(clientui.GoalNudgeCompactLabel)}
	rebuilt, err := buildReviewerRequestMessagesWithBuilder([]llm.Message{continuation, {Role: llm.RoleUser, Content: textutil.Value("request")}}, newMetaContextBuilder(t.TempDir(), "test-model", "", config.SkillPolicy{}, time.Unix(0, 0)), false)
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	continuationIndex, boundaryIndex, continuationCount := -1, -1, 0
	for index, message := range rebuilt {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeActiveGoalContinuation {
			continuationIndex = index
			continuationCount++
		}
		if message.Role == llm.RoleDeveloper && message.MessageType == nil && messageContent(message) == reviewerMetaBoundaryMessage {
			boundaryIndex = index
		}
	}
	if continuationCount != 1 || continuationIndex < 0 || boundaryIndex <= continuationIndex || !reflect.DeepEqual(rebuilt[continuationIndex], continuation) {
		t.Fatalf("reviewer continuation/boundary = %d/%d in %+v", continuationIndex, boundaryIndex, rebuilt)
	}
}

func TestActiveGoalContinuationProjectsAsDetailDeveloperContext(t *testing.T) {
	entries := VisibleChatEntriesFromMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("active-goal continuation"), CompactContent: textutil.Value(clientui.GoalNudgeCompactLabel)})
	if len(entries) != 1 || entries[0].Visibility != transcript.EntryVisibilityDetail || entries[0].Role != string(transcript.EntryRoleDeveloperContext) || entries[0].MessageType != llm.MessageTypeActiveGoalContinuation || entries[0].CondensedText != clientui.GoalNudgeCompactLabel || entries[0].CompactLabel != clientui.GoalNudgeCompactLabel {
		t.Fatalf("active-goal continuation projection = %+v", entries)
	}
}
