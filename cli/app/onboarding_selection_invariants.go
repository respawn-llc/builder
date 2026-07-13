package app

import (
	"fmt"
	"runtime/debug"
	"strings"

	"core/shared/toolspec"
)

type onboardingInvariantDiagnostic struct {
	Operation           string
	StepID              string
	ModelIdentity       string
	VariantType         string
	VariantTag          string
	PendingPrimaryEdit  onboardingThinkingEditKind
	PendingReviewerEdit onboardingThinkingEditKind
	PendingAction       onboardingPendingAction
	Stack               string
}

type onboardingInvariantViolation struct {
	VariantType string
	VariantTag  string
}

type onboardingInternalStateError struct {
	Diagnostic onboardingInvariantDiagnostic
}

func (e *onboardingInternalStateError) Error() string {
	return fmt.Sprintf(
		"onboarding cannot continue because its selection state is invalid during %s at step %s (%s=%s)",
		e.Diagnostic.Operation,
		e.Diagnostic.StepID,
		e.Diagnostic.VariantType,
		e.Diagnostic.VariantTag,
	)
}

func (state *onboardingFlowState) validateInvariant(operation string, stepID onboardingStepID) error {
	if violation, ok := state.selections.invariantViolation(); ok {
		return state.handleInvariantViolation(operation, stepID, violation)
	}
	for _, item := range skillSelectionCandidates(state) {
		if _, ok := state.selections.skillEnablement[item.ID]; !ok {
			return state.handleInvariantViolation(operation, stepID, onboardingInvariantViolation{
				VariantType: "skill_enablement",
				VariantTag:  item.ID,
			})
		}
	}
	return nil
}

func (state *onboardingFlowState) handleInvariantViolation(
	operation string,
	stepID onboardingStepID,
	violation onboardingInvariantViolation,
) error {
	diagnostic := onboardingInvariantDiagnostic{
		Operation:           operation,
		StepID:              string(stepID),
		ModelIdentity:       state.selections.model.value,
		VariantType:         violation.VariantType,
		VariantTag:          violation.VariantTag,
		PendingPrimaryEdit:  state.selections.pendingPrimaryThinking.kind,
		PendingReviewerEdit: state.selections.pendingReviewerThinking.kind,
		PendingAction:       state.pendingAction,
		Stack:               string(debug.Stack()),
	}
	if state.debug {
		panic(diagnostic)
	}
	return &onboardingInternalStateError{Diagnostic: diagnostic}
}

func (selections onboardingSelections) invariantViolation() (onboardingInvariantViolation, bool) {
	valid := func(value string, allowed ...string) bool {
		for _, candidate := range allowed {
			if value == candidate {
				return true
			}
		}
		return false
	}
	checks := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"theme", string(selections.theme.kind), []string{"auto", "light", "dark"}},
		{"model", string(selections.model.kind), []string{"known", "custom"}},
		{"context_window", string(selections.contextWindow.kind), []string{"default", "large", "custom"}},
		{"thinking", string(selections.thinking.kind), []string{"default", "disabled", "level", "custom"}},
		{"verbosity", string(selections.verbosity.kind), []string{"omitted", "level"}},
		{"supervisor.frequency", string(selections.supervisor.frequency), []string{"off", "edits", "all"}},
		{"supervisor.model", string(selections.supervisor.model.kind), []string{"inherited", "overridden"}},
		{"supervisor.thinking", string(selections.supervisor.thinking.kind), []string{"inherited", "overridden"}},
		{"compaction", string(selections.compaction), []string{"local", "native", "manual_only"}},
		{"skill_import", string(selections.skillImport.Mode), []string{"none", "symlink_source"}},
		{"pending_primary_thinking", string(selections.pendingPrimaryThinking.kind), []string{"none", "pending", "revisiting"}},
		{"pending_reviewer_thinking", string(selections.pendingReviewerThinking.kind), []string{"none", "pending", "revisiting"}},
	}
	for _, check := range checks {
		if !valid(check.value, check.allowed...) {
			return onboardingInvariantViolation{VariantType: check.name, VariantTag: check.value}, true
		}
	}
	if strings.TrimSpace(selections.model.value) == "" {
		return onboardingInvariantViolation{VariantType: "model.value", VariantTag: selections.model.value}, true
	}
	if selections.contextWindow.kind == onboardingContextCustom && selections.contextWindow.tokens <= 0 {
		return onboardingInvariantViolation{VariantType: "context_window.tokens", VariantTag: fmt.Sprint(selections.contextWindow.tokens)}, true
	}
	if selections.thinking.requiresValue() && strings.TrimSpace(selections.thinking.value) == "" {
		return onboardingInvariantViolation{VariantType: "thinking.value", VariantTag: selections.thinking.value}, true
	}
	if selections.verbosity.kind == onboardingVerbosityLevel && strings.TrimSpace(selections.verbosity.value) == "" {
		return onboardingInvariantViolation{VariantType: "verbosity.value", VariantTag: selections.verbosity.value}, true
	}
	if selections.supervisor.model.kind == onboardingReviewerModelOverridden {
		if violation, ok := selections.supervisor.model.override.invariantViolation("supervisor.model.override"); ok {
			return violation, true
		}
	}
	if selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden {
		if violation, ok := selections.supervisor.thinking.override.invariantViolation("supervisor.thinking.override"); ok {
			return violation, true
		}
	}
	if selections.skillImport.Mode == onboardingImportModeSymlinkSource {
		ref := selections.skillImport.ChoiceRef
		if onboardingImportMode(ref.Mode) != onboardingImportModeSymlinkSource {
			return onboardingInvariantViolation{VariantType: "skill_import.choice_ref.mode", VariantTag: ref.Mode}, true
		}
		if ref.ImportProviderID == nil || strings.TrimSpace(*ref.ImportProviderID) == "" {
			return onboardingInvariantViolation{VariantType: "skill_import.choice_ref.import_provider_id", VariantTag: fmt.Sprint(ref.ImportProviderID)}, true
		}
		if ref.SourceRootPath == nil || strings.TrimSpace(*ref.SourceRootPath) == "" {
			return onboardingInvariantViolation{VariantType: "skill_import.choice_ref.source_root_path", VariantTag: fmt.Sprint(ref.SourceRootPath)}, true
		}
	}
	if selections.preserved.providerOverride != nil && strings.TrimSpace(*selections.preserved.providerOverride) == "" {
		return onboardingInvariantViolation{VariantType: "preserved.provider_override", VariantTag: *selections.preserved.providerOverride}, true
	}
	if selections.preserved.openAIBaseURL != nil && strings.TrimSpace(*selections.preserved.openAIBaseURL) == "" {
		return onboardingInvariantViolation{VariantType: "preserved.openai_base_url", VariantTag: *selections.preserved.openAIBaseURL}, true
	}
	if selections.preserved.modelTimeoutSeconds != nil && *selections.preserved.modelTimeoutSeconds <= 0 {
		return onboardingInvariantViolation{VariantType: "preserved.model_timeout_seconds", VariantTag: fmt.Sprint(*selections.preserved.modelTimeoutSeconds)}, true
	}
	if selections.preserved.baselineModelContextWindow != nil && *selections.preserved.baselineModelContextWindow <= 0 {
		return onboardingInvariantViolation{VariantType: "preserved.baseline_model_context_window", VariantTag: fmt.Sprint(*selections.preserved.baselineModelContextWindow)}, true
	}
	for _, id := range toolspec.CatalogIDs() {
		if _, ok := selections.preserved.enabledTools[id]; !ok {
			return onboardingInvariantViolation{VariantType: "preserved.enabled_tools", VariantTag: string(id)}, true
		}
	}
	return onboardingInvariantViolation{}, false
}

func (selection onboardingModelSelection) invariantViolation(prefix string) (onboardingInvariantViolation, bool) {
	if selection.kind != onboardingModelKnown && selection.kind != onboardingModelCustom {
		return onboardingInvariantViolation{VariantType: prefix, VariantTag: string(selection.kind)}, true
	}
	if strings.TrimSpace(selection.value) == "" {
		return onboardingInvariantViolation{VariantType: prefix + ".value", VariantTag: selection.value}, true
	}
	return onboardingInvariantViolation{}, false
}

func (selection onboardingThinkingSelection) invariantViolation(prefix string) (onboardingInvariantViolation, bool) {
	switch selection.kind {
	case onboardingThinkingDefault, onboardingThinkingDisabled:
		return onboardingInvariantViolation{}, false
	case onboardingThinkingLevel, onboardingThinkingCustom:
		if strings.TrimSpace(selection.value) == "" {
			return onboardingInvariantViolation{VariantType: prefix + ".value", VariantTag: selection.value}, true
		}
		return onboardingInvariantViolation{}, false
	default:
		return onboardingInvariantViolation{VariantType: prefix, VariantTag: string(selection.kind)}, true
	}
}

func (selection onboardingThinkingSelection) requiresValue() bool {
	return selection.kind == onboardingThinkingLevel || selection.kind == onboardingThinkingCustom
}
