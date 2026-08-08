package workflow_test

import (
	"errors"
	"testing"

	"core/server/workflow"
)

func TestPlanTargetAgentThinkingSelectionUsesConfiguredFallback(t *testing.T) {
	role := workflow.TargetAgentRole{
		Identity:           "coder",
		ConfiguredThinking: "high",
		Thinking:           workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
	}
	selection, err := workflow.PlanTargetAgentThinkingSelection(workflow.TargetAgentThinkingSelectionRequest{
		TargetRole: role,
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentThinkingSelection: %v", err)
	}
	if selection.Value != "high" || selection.Exposed {
		t.Fatalf("configured thinking = %+v", selection)
	}
}

func TestTargetAgentThinkingContractUnionsFiniteRoleCatalogs(t *testing.T) {
	contract := workflow.UnionTargetAgentThinkingCapabilities([]workflow.TargetAgentRole{
		{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "medium"}}},
		{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"medium", "high"}}},
	})
	want := []string{"high", "low", "medium"}
	if !contract.Finite || !contract.ReasoningCapable || len(contract.Levels) != len(want) {
		t.Fatalf("union contract = %+v", contract)
	}
	for index, level := range want {
		if contract.Levels[index] != level {
			t.Fatalf("union levels = %#v, want %#v", contract.Levels, want)
		}
	}
}

func TestPlanTargetAgentThinkingSelectionResolvesFiniteZeroOneMany(t *testing.T) {
	tests := []struct {
		name      string
		role      workflow.TargetAgentRole
		roles     []workflow.TargetAgentRole
		submitted string
		want      string
		exposed   bool
		wantError workflow.TargetAgentSelectionErrorCode
	}{
		{
			name:  "zero levels use configured value",
			role:  workflow.TargetAgentRole{ConfiguredThinking: "medium", Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true}},
			roles: []workflow.TargetAgentRole{{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true}}},
			want:  "medium",
		},
		{
			name:  "one level is automatic",
			role:  workflow.TargetAgentRole{ConfiguredThinking: "low", Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"high"}}},
			roles: []workflow.TargetAgentRole{{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"high"}}}},
			want:  "high",
		},
		{
			name:      "many levels require value",
			role:      workflow.TargetAgentRole{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}},
			roles:     []workflow.TargetAgentRole{{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}}},
			submitted: "high",
			want:      "high",
			exposed:   true,
		},
		{
			name:      "many levels reject blank value",
			role:      workflow.TargetAgentRole{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}},
			roles:     []workflow.TargetAgentRole{{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}}},
			wantError: workflow.TargetAgentSelectionErrorThinkingRequired,
		},
		{
			name:      "finite level rejects target role mismatch",
			role:      workflow.TargetAgentRole{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low"}}},
			roles:     []workflow.TargetAgentRole{{Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}}},
			submitted: "high",
			wantError: workflow.TargetAgentSelectionErrorUnsupportedThinking,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := workflow.PlanTargetAgentThinkingSelection(workflow.TargetAgentThinkingSelectionRequest{
				OverrideEnabled: true,
				TargetRole:      tt.role,
				EligibleRoles:   tt.roles,
				SubmittedValue:  tt.submitted,
			})
			if tt.wantError != "" {
				var selectionErr workflow.TargetAgentSelectionError
				if err == nil || !errors.As(err, &selectionErr) || selectionErr.Code != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanTargetAgentThinkingSelection: %v", err)
			}
			if selection.Value != tt.want || selection.Exposed != tt.exposed {
				t.Fatalf("selection = %+v, want value %q exposed %v", selection, tt.want, tt.exposed)
			}
		})
	}
}

func TestPlanTargetAgentThinkingSelectionSupportsOpenCatalogOnlyWithAuthoredDescription(t *testing.T) {
	role := workflow.TargetAgentRole{
		ConfiguredThinking: "medium",
		Thinking:           workflow.ThinkingCapability{ReasoningCapable: true, Finite: false},
	}
	_, err := workflow.PlanTargetAgentThinkingSelection(workflow.TargetAgentThinkingSelectionRequest{
		OverrideEnabled: true,
		TargetRole:      role,
		EligibleRoles:   []workflow.TargetAgentRole{role},
		SubmittedValue:  "custom",
	})
	var selectionErr workflow.TargetAgentSelectionError
	if err == nil || !errors.As(err, &selectionErr) || selectionErr.Code != workflow.TargetAgentSelectionErrorThinkingDescriptionRequired {
		t.Fatalf("error = %v, want authored-description requirement", err)
	}
	selection, err := workflow.PlanTargetAgentThinkingSelection(workflow.TargetAgentThinkingSelectionRequest{
		OverrideEnabled:     true,
		TargetRole:          role,
		EligibleRoles:       []workflow.TargetAgentRole{role},
		SubmittedValue:      "custom",
		AuthoredDescription: "Choose a provider-specific effort.",
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentThinkingSelection: %v", err)
	}
	if selection.Value != "custom" || !selection.Exposed {
		t.Fatalf("open thinking selection = %+v", selection)
	}
}

func TestPlanTargetAgentThinkingSelectionValidatesFinalRoleAgainstMixedCatalog(t *testing.T) {
	finiteWithoutLevels := workflow.TargetAgentRole{
		Identity: "finite-empty",
		Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true},
	}
	finiteWithLevel := workflow.TargetAgentRole{
		Identity: "finite-one",
		Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"high"}},
	}
	selection, err := workflow.PlanTargetAgentThinkingSelection(workflow.TargetAgentThinkingSelectionRequest{
		OverrideEnabled: true,
		TargetRole:      finiteWithoutLevels,
		EligibleRoles:   []workflow.TargetAgentRole{finiteWithoutLevels, finiteWithLevel},
		SubmittedValue:  "high",
	})
	if err != nil || selection.Value != "" || !selection.SubmittedValueIgnored {
		t.Fatalf("selection = %+v, error = %v, want configured no-thinking fallback", selection, err)
	}

	open := workflow.TargetAgentRole{
		Identity: "open",
		Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: false},
	}
	_, err = workflow.PlanTargetAgentThinkingSelection(workflow.TargetAgentThinkingSelectionRequest{
		OverrideEnabled:     true,
		TargetRole:          finiteWithLevel,
		EligibleRoles:       []workflow.TargetAgentRole{open, finiteWithLevel},
		SubmittedValue:      "custom",
		AuthoredDescription: "Custom provider value.",
	})
	var selectionErr workflow.TargetAgentSelectionError
	if err == nil || !errors.As(err, &selectionErr) || selectionErr.Code != workflow.TargetAgentSelectionErrorUnsupportedThinking {
		t.Fatalf("error = %v, want selected finite role validation", err)
	}
}
