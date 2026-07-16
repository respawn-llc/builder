package clientui

import (
	"testing"
	"time"

	"core/shared/runtimeids"
)

func TestPendingPromptAcceptsQuestionWithBoundedRecommendation(t *testing.T) {
	recommended := 2
	prompt := TranscriptPrompt{
		Kind:                   TranscriptPromptKindQuestion,
		State:                  TranscriptPromptStatePending,
		PromptID:               PromptID("prompt-1"),
		SessionID:              transcriptTestSessionID(t),
		StepID:                 transcriptTestStepID(t),
		Question:               "Choose a strategy",
		CreatedAt:              time.Unix(1_700_000_000, 0),
		Suggestions:            []string{"First", "Second"},
		RecommendedOptionIndex: &recommended,
		Tool: &ToolProvenance{
			ToolCallID: ToolCallID("call-1"),
			ToolName:   "ask_question",
		},
	}
	if err := prompt.Validate(); err != nil {
		t.Fatalf("validate pending question: %v", err)
	}
}

func TestPendingPromptRejectsInvalidQuestionOptions(t *testing.T) {
	base := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		State:     TranscriptPromptStatePending,
		PromptID:  PromptID("prompt-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Choose a strategy",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	zero := 0
	outOfBounds := 2
	tests := []TranscriptPrompt{
		func() TranscriptPrompt {
			prompt := base
			prompt.Suggestions = []string{" "}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.Suggestions = []string{"First"}
			prompt.RecommendedOptionIndex = &zero
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.Suggestions = []string{"First"}
			prompt.RecommendedOptionIndex = &outOfBounds
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.ApprovalOptions = []ApprovalDecision{ApprovalDecisionDeny}
			return prompt
		}(),
	}
	for _, prompt := range tests {
		if err := prompt.Validate(); err == nil {
			t.Fatalf("accepted invalid question prompt: %+v", prompt)
		}
	}
}

func TestPendingPromptAcceptsTypedApprovalWithoutServerLabels(t *testing.T) {
	prompt := TranscriptPrompt{
		Kind:      TranscriptPromptKindApproval,
		State:     TranscriptPromptStatePending,
		PromptID:  PromptID("approval-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Allow this operation?",
		CreatedAt: time.Unix(1_700_000_000, 0),
		ApprovalOptions: []ApprovalDecision{
			ApprovalDecisionAllowOnce,
			ApprovalDecisionAllowSession,
			ApprovalDecisionDeny,
		},
	}
	if err := prompt.Validate(); err != nil {
		t.Fatalf("validate pending approval: %v", err)
	}
}

func TestPendingPromptRejectsInvalidApprovalOptions(t *testing.T) {
	base := TranscriptPrompt{
		Kind:      TranscriptPromptKindApproval,
		State:     TranscriptPromptStatePending,
		PromptID:  PromptID("approval-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Allow this operation?",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	recommended := 1
	tests := []TranscriptPrompt{
		base,
		func() TranscriptPrompt {
			prompt := base
			prompt.Suggestions = []string{"Allow"}
			prompt.ApprovalOptions = []ApprovalDecision{ApprovalDecisionAllowOnce}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.RecommendedOptionIndex = &recommended
			prompt.ApprovalOptions = []ApprovalDecision{ApprovalDecisionAllowOnce}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.ApprovalOptions = []ApprovalDecision{
				ApprovalDecisionDeny,
				ApprovalDecisionDeny,
			}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.ApprovalOptions = []ApprovalDecision{"unknown"}
			return prompt
		}(),
	}
	for _, prompt := range tests {
		if err := prompt.Validate(); err == nil {
			t.Fatalf("accepted invalid approval prompt: %+v", prompt)
		}
	}
}

func TestPendingPromptRequiresCompleteIdentityAndToolProvenance(t *testing.T) {
	base := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		State:     TranscriptPromptStateResolved,
		PromptID:  PromptID("prompt-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Choose a strategy",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	tests := []TranscriptPrompt{
		func() TranscriptPrompt {
			prompt := base
			prompt.PromptID = " "
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.SessionID = runtimeids.SessionID{}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.Question = ""
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.StepID = runtimeids.StepID{}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.CreatedAt = time.Time{}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.Tool = &ToolProvenance{ToolCallID: ToolCallID("call-1")}
			return prompt
		}(),
		func() TranscriptPrompt {
			prompt := base
			prompt.Tool = &ToolProvenance{ToolName: "ask_question"}
			return prompt
		}(),
	}
	for _, prompt := range tests {
		if err := prompt.Validate(); err == nil {
			t.Fatalf("accepted incomplete pending prompt: %+v", prompt)
		}
	}
}
