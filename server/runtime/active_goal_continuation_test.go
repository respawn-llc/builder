package runtime

import (
	"path/filepath"
	"reflect"
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
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeEnvironment,
	}
	got := metaContextMessageTypes(result.Projection().Messages())
	if len(got) != len(want) {
		t.Fatalf("ordered meta message types = %+v, want %+v", got, want)
	}
	for index, messageType := range want {
		if got[index] != messageType {
			t.Fatalf("ordered meta type[%d] = %q, want %q", index, got[index], messageType)
		}
	}
}

func TestMetaContextProjectionSelectsWorkflowOverActiveGoal(t *testing.T) {
	t.Parallel()
	ordinary, err := newMetaContextBuilder(
		t.TempDir(),
		"",
		"",
		config.SkillPolicy{},
		time.Unix(0, 0),
	).Build(metaContextBuildOptions{
		ExistingMessages: []llm.Message{
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("goal")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), Content: textutil.Value("workflow")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment")},
		},
	})
	if err != nil {
		t.Fatalf("build ordinary meta context: %v", err)
	}
	assertMetaContextTypes(t, ordinary.Projection().Messages(), []llm.MessageType{
		llm.MessageTypeEnvironment,
	})

	result, err := newMetaContextBuilder(
		t.TempDir(),
		"",
		"",
		config.SkillPolicy{},
		time.Unix(0, 0),
	).Build(metaContextBuildOptions{
		ExistingMessages: []llm.Message{
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("goal")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), Content: textutil.Value("workflow")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment")},
		},
		IncludeWorkflow: true,
	})
	if err != nil {
		t.Fatalf("build meta context: %v", err)
	}
	assertMetaContextTypes(t, result.Projection().Messages(), []llm.MessageType{
		llm.MessageTypeWorkflowMode,
		llm.MessageTypeEnvironment,
	})

	goal, err := newMetaContextBuilder(
		t.TempDir(),
		"",
		"",
		config.SkillPolicy{},
		time.Unix(0, 0),
	).Build(metaContextBuildOptions{
		ExistingMessages: []llm.Message{
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("goal")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorkflowMode), Content: textutil.Value("workflow")},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment")},
		},
		ActiveGoal: &session.GoalState{Objective: "goal", Status: session.GoalStatusActive},
	})
	if err != nil {
		t.Fatalf("build goal meta context: %v", err)
	}
	assertMetaContextTypes(t, goal.Projection().Messages(), []llm.MessageType{
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeEnvironment,
	})
}

func TestMetaContextProjectionUsesCanonicalStablePrefixAcrossEmissionOrders(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	builder := newMetaContextBuilder(workspace, "model", "", config.SkillPolicy{}, time.Unix(0, 0)).
		withGlobalConfigDir(filepath.Join(workspace, "global"))
	base := []llm.Message{
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeAgentsMD), SourcePath: textutil.Value(filepath.Join(workspace, "AGENTS.md")), Content: textutil.Value("agents")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeSkills), Content: textutil.Value("skills")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeSubagents), Content: textutil.Value("subagents")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment snapshot")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeHeadlessMode), Content: textutil.Value("headless")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeWorktreeMode), Content: textutil.Value("worktree")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation), Content: textutil.Value("goal")},
	}
	forward, err := builder.Build(metaContextBuildOptions{
		ExistingMessages: base,
		SessionMode:      metaContextSessionModeGoal,
	})
	if err != nil {
		t.Fatalf("build forward projection: %v", err)
	}
	reverseInput := append([]llm.Message(nil), base...)
	for left, right := 0, len(reverseInput)-1; left < right; left, right = left+1, right-1 {
		reverseInput[left], reverseInput[right] = reverseInput[right], reverseInput[left]
	}
	reverse, err := builder.Build(metaContextBuildOptions{
		ExistingMessages: reverseInput,
		SessionMode:      metaContextSessionModeGoal,
	})
	if err != nil {
		t.Fatalf("build reverse projection: %v", err)
	}

	want := []llm.MessageType{
		llm.MessageTypeHeadlessMode,
		llm.MessageTypeSubagents,
		llm.MessageTypeSkills,
		llm.MessageTypeWorktreeMode,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeActiveGoalContinuation,
		llm.MessageTypeEnvironment,
	}
	assertMetaContextTypes(t, forward.Projection().Messages(), want)
	assertMetaContextTypes(t, reverse.Projection().Messages(), want)
	if got, want := metaContextMessageTypes(forward.Projection().Messages()), metaContextMessageTypes(reverse.Projection().Messages()); !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical projection changed with emission order: forward=%+v reverse=%+v", got, want)
	}

	reviewer, err := buildReviewerRequestMessagesWithBuilder(base, builder, false)
	if err != nil {
		t.Fatalf("build reviewer projection: %v", err)
	}
	boundaryIndex := -1
	for index, message := range reviewer {
		if message.Role == llm.RoleDeveloper && message.MessageType == nil {
			boundaryIndex = index
			break
		}
	}
	if boundaryIndex < 0 {
		t.Fatalf("reviewer projection omitted its transcript boundary: %+v", reviewer)
	}
	reviewerMeta := reviewer[:boundaryIndex]
	assertMetaContextTypes(t, reviewerMeta, want)
}

func assertMetaContextTypes(t *testing.T, messages []llm.Message, want []llm.MessageType) {
	t.Helper()
	got := metaContextMessageTypes(messages)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("meta context order = %+v, want %+v", got, want)
	}
	if len(messages) == 0 || messages[len(messages)-1].MessageType == nil ||
		*messages[len(messages)-1].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("environment must be outside stable prefix: %+v", messages)
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

func TestReviewerReconstructionUsesLatestMetaContextMode(t *testing.T) {
	t.Parallel()
	rebuilt, err := buildReviewerRequestMessagesWithBuilder(
		[]llm.Message{
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeActiveGoalContinuation),
				Content:     textutil.Value("completed goal"),
			},
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
				SourcePath:  textutil.Value("current-node"),
				Content:     textutil.Value("current workflow"),
			},
			{Role: llm.RoleUser, Content: textutil.Value("request")},
		},
		newMetaContextBuilder(t.TempDir(), "model", "", config.SkillPolicy{}, time.Unix(0, 0)),
		false,
	)
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	counts := make(map[llm.MessageType]int)
	for _, message := range rebuilt {
		if message.MessageType != nil {
			counts[*message.MessageType]++
		}
	}
	if counts[llm.MessageTypeActiveGoalContinuation] != 0 || counts[llm.MessageTypeWorkflowMode] != 1 {
		t.Fatalf("reviewer meta-context mode counts = %+v, want latest Workflow only", counts)
	}
}

func TestReviewerReconstructionPreservesLatestWorkflowExit(t *testing.T) {
	t.Parallel()
	rebuilt, err := buildReviewerRequestMessagesWithBuilder(
		[]llm.Message{
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeWorkflowMode),
				SourcePath:  textutil.Value("discarded-node"),
				Content:     textutil.Value("discarded workflow"),
			},
			{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeWorkflowModeExit),
				Content:     textutil.Value("the discarded workflow assignment was rolled back"),
			},
			{Role: llm.RoleUser, Content: textutil.Value("request")},
		},
		newMetaContextBuilder(t.TempDir(), "model", "", config.SkillPolicy{}, time.Unix(0, 0)),
		false,
	)
	if err != nil {
		t.Fatalf("build reviewer request: %v", err)
	}
	boundaryIndex := -1
	for index, message := range rebuilt {
		if message.Role == llm.RoleDeveloper && message.MessageType == nil {
			boundaryIndex = index
			break
		}
	}
	if boundaryIndex < 0 {
		t.Fatalf("reviewer projection omitted its transcript boundary: %+v", rebuilt)
	}
	metaTypes := metaContextMessageTypes(rebuilt[:boundaryIndex])
	exitIndex := -1
	environmentIndex := -1
	for index, messageType := range metaTypes {
		switch messageType {
		case llm.MessageTypeWorkflowModeExit:
			exitIndex = index
		case llm.MessageTypeEnvironment:
			environmentIndex = index
		}
	}
	if exitIndex < 0 || environmentIndex < 0 || exitIndex >= environmentIndex {
		t.Fatalf("reviewer Workflow exit/environment order = %+v, want exit before environment", metaTypes)
	}
	counts := make(map[llm.MessageType]int)
	for _, message := range rebuilt[:boundaryIndex] {
		if message.MessageType != nil {
			counts[*message.MessageType]++
		}
	}
	if counts[llm.MessageTypeWorkflowMode] != 0 || counts[llm.MessageTypeWorkflowModeExit] != 1 {
		t.Fatalf("reviewer Workflow mode counts = %+v, want exit only", counts)
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
