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
	first := llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    llm.MessageTypeActiveGoalContinuation,
		Content:        "preserved continuation",
		CompactContent: clientui.GoalNudgeCompactLabel,
	}
	second := first
	second.Content = "duplicate continuation"
	activeGoal := session.GoalState{Objective: "new mutable goal", Status: session.GoalStatusActive}

	result, err := newMetaContextBuilder(t.TempDir(), "", "", config.SkillPolicy{}, time.Unix(0, 0)).Build(metaContextBuildOptions{
		ExistingMessages: []llm.Message{first, second},
		ActiveGoal:       &activeGoal,
	})
	if err != nil {
		t.Fatalf("build meta context: %v", err)
	}
	if !reflect.DeepEqual(result.ActiveGoalContinuation, []llm.Message{first}) {
		t.Fatalf("active-goal continuation slot = %+v, want first existing message preserved", result.ActiveGoalContinuation)
	}

	meta, ordinary := splitMetaContextMessages([]llm.Message{first, {Role: llm.RoleUser, Content: "request"}})
	if !reflect.DeepEqual(meta, []llm.Message{first}) {
		t.Fatalf("classified meta context = %+v, want active-goal continuation", meta)
	}
	if len(ordinary) != 1 || ordinary[0].Role != llm.RoleUser {
		t.Fatalf("ordinary transcript = %+v, want user request only", ordinary)
	}
}

func TestActiveGoalContinuationCanonicalOrdering(t *testing.T) {
	activeGoal := session.GoalState{Objective: "ship resumed goal", Status: session.GoalStatusActive}
	existing := []llm.Message{
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorktreeMode, Content: "worktree"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeWorkflowMode, Content: "workflow"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "environment"},
	}
	result, err := newMetaContextBuilder(t.TempDir(), "", "", config.SkillPolicy{}, time.Unix(0, 0)).Build(metaContextBuildOptions{
		ExistingMessages: existing,
		ActiveGoal:       &activeGoal,
	})
	if err != nil {
		t.Fatalf("build meta context: %v", err)
	}
	ordered := result.OrderedMetaMessages()
	got := make([]llm.MessageType, 0, len(ordered))
	for _, message := range ordered {
		got = append(got, message.MessageType)
	}
	want := []llm.MessageType{
		llm.MessageTypeEnvironment,
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeWorktreeMode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered message types = %#v, want %#v", got, want)
	}
}

func TestReviewerReconstructionPreservesActiveGoalContinuationBeforeBoundary(t *testing.T) {
	continuation := llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    llm.MessageTypeActiveGoalContinuation,
		Content:        "preserved active-goal continuation",
		CompactContent: clientui.GoalNudgeCompactLabel,
	}
	rebuilt, err := buildReviewerRequestMessagesWithBuilder(
		[]llm.Message{continuation, {Role: llm.RoleUser, Content: "request"}},
		newMetaContextBuilder(t.TempDir(), "test-model", "", config.SkillPolicy{}, time.Unix(0, 0)),
		false,
	)
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	continuationIndex, boundaryIndex := -1, -1
	for index, message := range rebuilt {
		if message.MessageType == llm.MessageTypeActiveGoalContinuation {
			if continuationIndex >= 0 {
				t.Fatalf("reviewer reconstruction duplicated active-goal continuation: %+v", rebuilt)
			}
			continuationIndex = index
			if !reflect.DeepEqual(message, continuation) {
				t.Fatalf("reviewer continuation = %+v, want preserved %+v", message, continuation)
			}
		}
		if message.Role == llm.RoleDeveloper && message.MessageType == "" && message.Content == reviewerMetaBoundaryMessage {
			boundaryIndex = index
		}
	}
	if continuationIndex < 0 || boundaryIndex < 0 || continuationIndex >= boundaryIndex {
		t.Fatalf("continuation/boundary indexes = %d/%d, want canonical continuation before reviewer boundary", continuationIndex, boundaryIndex)
	}
}

func TestActiveGoalContinuationProjectsAsDetailDeveloperContext(t *testing.T) {
	message := llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    llm.MessageTypeActiveGoalContinuation,
		Content:        "active-goal continuation",
		CompactContent: clientui.GoalNudgeCompactLabel,
	}
	entries := VisibleChatEntriesFromMessage(message)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one active-goal continuation row", entries)
	}
	entry := entries[0]
	if entry.Visibility != transcript.EntryVisibilityDetail {
		t.Fatalf("visibility = %q, want detail", entry.Visibility)
	}
	if entry.Role != string(transcript.EntryRoleDeveloperContext) {
		t.Fatalf("role = %q, want developer context", entry.Role)
	}
	if entry.MessageType != llm.MessageTypeActiveGoalContinuation {
		t.Fatalf("message type = %q, want active-goal continuation", entry.MessageType)
	}
	if entry.CondensedText != clientui.GoalNudgeCompactLabel || entry.CompactLabel != clientui.GoalNudgeCompactLabel {
		t.Fatalf("compact presentation = condensed:%q label:%q, want shared goal nudge label", entry.CondensedText, entry.CompactLabel)
	}
}
