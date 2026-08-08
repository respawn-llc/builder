package workflow_test

import (
	"errors"
	"reflect"
	"testing"

	"core/server/workflow"
)

type targetAgentCatalogStub struct {
	fallback   map[string]workflow.TargetAgentRole
	selectable []workflow.TargetAgentRole
}

func (c targetAgentCatalogStub) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	value, ok := c.fallback[role]
	return value, ok
}

func (c targetAgentCatalogStub) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return append([]workflow.TargetAgentRole(nil), c.selectable...)
}

func TestPlanTargetAgentSelectionUsesFallbackAndConfiguredQuestions(t *testing.T) {
	catalog := targetAgentCatalogStub{
		fallback: map[string]workflow.TargetAgentRole{
			"coder": {Identity: "coder", QuestionsEnabled: true},
		},
	}
	selection, err := workflow.PlanTargetAgentSelection(catalog, workflow.TargetAgentSelectionRequest{
		FallbackRole: "coder",
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentSelection: %v", err)
	}
	if selection.Role.Identity != "coder" || selection.QuestionsRequired {
		t.Fatalf("fallback selection = %+v", selection)
	}
}

func TestPlanTargetAgentSelectionForceEnablesQuestionsForExplicitRole(t *testing.T) {
	catalog := targetAgentCatalogStub{
		selectable: []workflow.TargetAgentRole{
			{Identity: "high", QuestionsEnabled: false, ExplicitAgentCallable: true},
			{Identity: "low", QuestionsEnabled: true, ExplicitAgentCallable: true},
		},
	}
	selection, err := workflow.PlanTargetAgentSelection(catalog, workflow.TargetAgentSelectionRequest{
		OverrideEnabled: true,
		SubmittedRole:   "high",
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentSelection: %v", err)
	}
	if selection.Role.Identity != "high" || !selection.QuestionsRequired {
		t.Fatalf("selected role = %+v", selection)
	}
}

func TestPlanTargetAgentSelectionResolvesZeroOneManyRoles(t *testing.T) {
	tests := []struct {
		name             string
		roles            []workflow.TargetAgentRole
		submitted        string
		wantRole         string
		wantError        workflow.TargetAgentSelectionErrorCode
		wantIgnoredInput bool
	}{
		{name: "zero roles unavailable", wantError: workflow.TargetAgentSelectionErrorNoSelectableRoles},
		{
			name:             "one role automatic",
			roles:            []workflow.TargetAgentRole{{Identity: "only", ExplicitAgentCallable: true}},
			submitted:        "ignored",
			wantRole:         "only",
			wantIgnoredInput: true,
		},
		{
			name:      "many roles require submitted role",
			roles:     []workflow.TargetAgentRole{{Identity: "high", ExplicitAgentCallable: true}, {Identity: "low", ExplicitAgentCallable: true}},
			submitted: "LOW",
			wantRole:  "low",
		},
		{
			name:      "many roles reject unavailable role",
			roles:     []workflow.TargetAgentRole{{Identity: "high", ExplicitAgentCallable: true}, {Identity: "low", ExplicitAgentCallable: true}},
			submitted: "missing",
			wantError: workflow.TargetAgentSelectionErrorUnavailableRole,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := workflow.PlanTargetAgentSelection(targetAgentCatalogStub{selectable: tt.roles}, workflow.TargetAgentSelectionRequest{
				OverrideEnabled: true,
				SubmittedRole:   tt.submitted,
			})
			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("PlanTargetAgentSelection succeeded, want %q", tt.wantError)
				}
				var selectionErr workflow.TargetAgentSelectionError
				if !errors.As(err, &selectionErr) || selectionErr.Code != tt.wantError {
					t.Fatalf("error = %v, want code %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanTargetAgentSelection: %v", err)
			}
			if selection.Role.Identity != tt.wantRole {
				t.Fatalf("role = %q, want %q", selection.Role.Identity, tt.wantRole)
			}
			if selection.SubmittedRoleIgnored != tt.wantIgnoredInput {
				t.Fatalf("submitted-role ignored = %v, want %v", selection.SubmittedRoleIgnored, tt.wantIgnoredInput)
			}
		})
	}
}

func TestPlanTargetAgentSelectionSortsAndCanonicalizesCatalogRoles(t *testing.T) {
	catalog := targetAgentCatalogStub{
		selectable: []workflow.TargetAgentRole{
			{Identity: "zeta", ExplicitAgentCallable: true},
			{Identity: "alpha", ExplicitAgentCallable: true},
		},
	}
	got := catalog.ExplicitCallableRoles()
	want := []workflow.TargetAgentRole{{Identity: "zeta", ExplicitAgentCallable: true}, {Identity: "alpha", ExplicitAgentCallable: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stub roles changed unexpectedly: %#v", got)
	}
	selection, err := workflow.PlanTargetAgentSelection(catalog, workflow.TargetAgentSelectionRequest{
		OverrideEnabled: true,
		SubmittedRole:   " ALPHA ",
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentSelection: %v", err)
	}
	if selection.Role.Identity != "alpha" {
		t.Fatalf("canonical role = %q, want alpha", selection.Role.Identity)
	}
}

func TestDefaultTargetAgentRoleDescriptionUsesAlphabeticalExplicitRoles(t *testing.T) {
	description := workflow.DefaultTargetAgentRoleDescription([]workflow.TargetAgentRole{
		{Identity: "zeta", ExplicitAgentCallable: true},
		{Identity: "alpha", ExplicitAgentCallable: true},
	})
	want := "Override the subagent role for the next node, available roles: alpha, zeta"
	if description != want {
		t.Fatalf("description = %q, want %q", description, want)
	}
}
