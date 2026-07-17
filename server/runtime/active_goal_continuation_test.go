package runtime

import (
	"reflect"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/transcript"
)

func TestActiveGoalContinuationUsesOneCanonicalMetaContextSlot(t *testing.T) {
	first := llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeActiveGoalContinuation, Content: "preserved continuation", CompactContent: clientui.GoalNudgeCompactLabel}
	second := first
	second.Content = "duplicate continuation"
	result, err := newMetaContextBuilder(t.TempDir(), "", "", config.SkillPolicy{}, time.Unix(0, 0)).Build(metaContextBuildOptions{ExistingMessages: []llm.Message{
		first, second,
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorktreeMode, Content: "worktree"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorkflowMode, Content: "workflow"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "environment"},
	}, ActiveGoal: &session.GoalState{Objective: "new mutable goal", Status: session.GoalStatusActive}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.ActiveGoalContinuation, []llm.Message{first}) {
		t.Fatalf("active-goal continuation slot = %+v, want first existing message preserved", result.ActiveGoalContinuation)
	}
	assertMessageTypesInOrder(t, result.OrderedMetaMessages(), llm.MessageTypeEnvironment, llm.MessageTypeHeadlessMode, llm.MessageTypeActiveGoalContinuation, llm.MessageTypeWorkflowMode, llm.MessageTypeWorktreeMode)
	meta, ordinary := splitMetaContextMessages([]llm.Message{first, {Role: llm.RoleUser, Content: "request"}})
	if !reflect.DeepEqual(meta, []llm.Message{first}) || len(ordinary) != 1 || ordinary[0].Role != llm.RoleUser {
		t.Fatalf("classified meta=%+v ordinary=%+v, want continuation and user request", meta, ordinary)
	}
}

func TestReviewerReconstructionPreservesActiveGoalContinuationBeforeBoundary(t *testing.T) {
	continuation := llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeActiveGoalContinuation, Content: "preserved active-goal continuation", CompactContent: clientui.GoalNudgeCompactLabel}
	rebuilt, err := buildReviewerRequestMessagesWithBuilder([]llm.Message{continuation, {Role: llm.RoleUser, Content: "request"}}, newMetaContextBuilder(t.TempDir(), "test-model", "", config.SkillPolicy{}, time.Unix(0, 0)), false)
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	continuationIndex, boundaryIndex, continuationCount := -1, -1, 0
	for index, message := range rebuilt {
		if message.MessageType == llm.MessageTypeActiveGoalContinuation {
			continuationIndex = index
			continuationCount++
		}
		if message.Role == llm.RoleDeveloper && message.MessageType == "" && message.Content == reviewerMetaBoundaryMessage {
			boundaryIndex = index
		}
	}
	if continuationCount != 1 || continuationIndex < 0 || boundaryIndex <= continuationIndex || !reflect.DeepEqual(rebuilt[continuationIndex], continuation) {
		t.Fatalf("reviewer continuation/boundary = %d/%d in %+v", continuationIndex, boundaryIndex, rebuilt)
	}
}

func TestActiveGoalContinuationProjectsAsDetailDeveloperContext(t *testing.T) {
	entries := VisibleChatEntriesFromMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeActiveGoalContinuation, Content: "active-goal continuation", CompactContent: clientui.GoalNudgeCompactLabel})
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one active-goal continuation row", entries)
	}
	if entry := entries[0]; entry.Visibility != transcript.EntryVisibilityDetail || entry.Role != string(transcript.EntryRoleDeveloperContext) || entry.MessageType != llm.MessageTypeActiveGoalContinuation || entry.CondensedText != clientui.GoalNudgeCompactLabel || entry.CompactLabel != clientui.GoalNudgeCompactLabel {
		t.Fatalf("active-goal continuation projection = %+v", entry)
	}
}
