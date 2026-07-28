package workflowruntime

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"core/server/llm"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
)

const (
	CompleteNodeToolName = "complete_node"
	structuredOutputName = "workflow_completion"
)

// ErrStructuredOutputUnsupported is returned when structured-output completion
// is requested but the provider lacks responses-API support.
var ErrStructuredOutputUnsupported = errors.New("workflow structured output completion requires provider responses API support")
var ErrShellCompletionUnavailable = errors.New("workflow shell-command completion requires exec_command")

type CompletionMode string

const (
	CompletionModeStructuredOutput   CompletionMode = "structured_output"
	CompletionModeTool               CompletionMode = "tool"
	CompletionModeShellCommand       CompletionMode = "shell_command"
	CompletionModeUnstructuredOutput CompletionMode = "unstructured_output"
)

type CompletionModeSelection struct {
	ConfiguredMode         config.WorkflowCompletionMode
	ProviderCapabilities   llm.ProviderCapabilities
	HasContinueSessionEdge bool
	ShellAvailable         bool
}

type CompletionContract struct {
	Transitions []CompletionTransition
}

type CompletionTransition struct {
	ID          string
	DisplayName string
	Description string
	Parameters  []workflow.Parameter
}

// CurrentNodeExecutionConfig is the live, process-local control contract for
// one admitted Current Node.
type CurrentNodeExecutionConfig struct {
	ScopeID                      runtimeids.ExecutionScopeID
	Contract                     CompletionContract
	CompletionMode               CompletionMode
	MaxInvalidCompletionAttempts int
	UseAutomaticToolChoice       bool
	Controller                   Controller
	TaskCommentCounter           TaskCommentCounter
	Instructions                 TaskInstructions
}

// PromptContract is the workflow prompt surface used to assemble a model
// request. It intentionally excludes live execution control, which belongs
// only to CurrentNodeExecutionConfig.
type PromptContract struct {
	Identity               string
	CompletionMode         CompletionMode
	UseAutomaticToolChoice bool
	Instructions           TaskInstructions
	Transitions            []CompletionTransition
	TaskCommentCount       int64
}

type TaskCommentCounter interface {
	CountTaskComments(context.Context, workflow.TaskID) (int64, error)
}

type TaskInstructions struct {
	CurrentNode     workflow.CurrentNodeReference
	TaskShortID     string
	TaskTitle       string
	TaskBody        string
	WorkflowID      string
	WorkflowShortID string
	WorkflowName    string
	NodeKey         string
	NodeDisplayName string
	ContextMode     string
	SourceSessionID string
	Transitions     []TransitionInstruction
	NodePrompt      string
}

// CurrentNodePromptIdentity gives one stable prompt SourcePath to the full
// natural Current Node reference. A branch-scoped Current Node must never
// share its prompt identity with a serial node or another fan-out branch.
func CurrentNodePromptIdentity(reference workflow.CurrentNodeReference) string {
	if err := reference.Validate(); err != nil {
		panic(fmt.Sprintf("current-node prompt identity requires a valid current node reference: %v", err))
	}
	identity := "workflow-current-node/" + string(reference.TaskID) + "/" + string(reference.NodeID)
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		return identity + "/branch/" + string(branchKey)
	}
	return identity
}

type TransitionInstruction struct {
	ID          string
	DisplayName string
	Description string
}

type CompletionRequest struct {
	ScopeID      runtimeids.ExecutionScopeID
	TransitionID string
	OutputValues map[string]string
	Commentary   string
}

type CompletionResult struct {
	TransitionID workflow.TransitionID
	State        string
}

type CompletionObservationRequest struct {
	ScopeID runtimeids.ExecutionScopeID
}

type CompletionObservationResult struct {
	Completed bool
}

type ViolationKind string

const (
	ViolationKindInvalidCompletion ViolationKind = "invalid_completion"
)

type ViolationResult struct {
	Count       int64
	Interrupted bool
}

type Controller interface {
	CompleteCurrentNode(context.Context, CompletionRequest) (CompletionResult, error)
	RecordProtocolViolation(context.Context, ViolationRequest) (ViolationResult, error)
	ResetProtocolViolationBudget(context.Context, ViolationResetRequest) error
	ObserveCurrentNodeCompletion(context.Context, CompletionObservationRequest) (CompletionObservationResult, error)
}

type ViolationRequest struct {
	ScopeID  runtimeids.ExecutionScopeID
	Kind     ViolationKind
	MaxCount int
	Detail   string
}

type ViolationResetRequest struct {
	ScopeID runtimeids.ExecutionScopeID
}

func SelectCompletionMode(selection CompletionModeSelection) (CompletionMode, error) {
	switch selection.ConfiguredMode {
	case config.WorkflowCompletionModeTool:
		return CompletionModeTool, nil
	case config.WorkflowCompletionModeStructuredOutput:
		if !ProviderSupportsStructuredOutput(selection.ProviderCapabilities) {
			return "", ErrStructuredOutputUnsupported
		}
		return CompletionModeStructuredOutput, nil
	case config.WorkflowCompletionModeShellCommand:
		if !selection.ShellAvailable {
			return "", ErrShellCompletionUnavailable
		}
		return CompletionModeShellCommand, nil
	case config.WorkflowCompletionModeUnstructured:
		return CompletionModeUnstructuredOutput, nil
	case config.WorkflowCompletionModeAuto, "":
		if !selection.ShellAvailable {
			return CompletionModeUnstructuredOutput, nil
		}
		if selection.HasContinueSessionEdge {
			return CompletionModeShellCommand, nil
		}
		if ProviderSupportsStructuredOutput(selection.ProviderCapabilities) {
			return CompletionModeStructuredOutput, nil
		}
		return CompletionModeTool, nil
	default:
		return "", fmt.Errorf("invalid workflow completion mode %q", selection.ConfiguredMode)
	}
}

func ProviderSupportsStructuredOutput(caps llm.ProviderCapabilities) bool {
	return caps.SupportsResponsesAPI
}

func ParseCompletionMode(raw string) (CompletionMode, error) {
	mode := CompletionMode(strings.TrimSpace(raw))
	switch mode {
	case CompletionModeStructuredOutput, CompletionModeTool, CompletionModeShellCommand, CompletionModeUnstructuredOutput:
		return mode, nil
	case "":
		return "", errors.New("workflow effective completion mode is required")
	default:
		return "", fmt.Errorf("invalid workflow effective completion mode %q", raw)
	}
}

func StructuredOutput(contract CompletionContract) (*llm.StructuredOutput, error) {
	schema, err := CompletionJSONSchema(contract)
	if err != nil {
		return nil, err
	}
	return &llm.StructuredOutput{
		Name:        structuredOutputName,
		Description: "Complete the current workflow node by selecting a transition and returning required transition parameters.",
		Schema:      schema,
		Strict:      true,
	}, nil
}

func CompletionJSONSchema(contract CompletionContract) (json.RawMessage, error) {
	transitions := normalizedTransitions(contract.Transitions)
	transitionIDs := sortedTransitionIDs(transitions)
	properties := map[string]any{
		"commentary": commentaryProperty(),
	}
	required := []string{}
	if len(transitions) > 1 {
		properties["transition"] = transitionProperty(transitionIDs)
		required = append(required, "transition")
	}
	required = append(required, "commentary")
	for _, parameter := range schemaParameters(transitions) {
		name := strings.TrimSpace(parameter.Key)
		if len(transitions) == 1 {
			properties[name] = parameterProperty(parameter)
			required = append(required, name)
			continue
		}
		properties[name] = nullableParameterProperty(parameter)
		required = append(required, name)
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func transitionProperty(transitionIDs []string) map[string]any {
	property := map[string]any{
		"type":        "string",
		"description": "Transition to take. Required when multiple outgoing transitions are available.",
	}
	if len(transitionIDs) > 0 {
		property["enum"] = transitionIDs
	}
	return property
}

func commentaryProperty() map[string]any {
	return map[string]any{
		"type":        []string{"string", "null"},
		"description": "Brief explanation of what was completed and why this transition was selected.",
	}
}

func parameterProperty(parameter workflow.Parameter) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": strings.TrimSpace(parameter.Description),
	}
}

func nullableParameterProperty(parameter workflow.Parameter) map[string]any {
	description := strings.TrimSpace(parameter.Description)
	if description == "" {
		description = "Set to a string only when this parameter belongs to the selected transition; otherwise set null."
	} else {
		description += " Set to null when this parameter does not belong to the selected transition."
	}
	return map[string]any{
		"type":        []string{"string", "null"},
		"description": description,
	}
}

type ParsedCompletion struct {
	TransitionID string
	Commentary   string
	OutputValues map[string]string
}

type ValidationIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type ValidationError struct {
	Issues []ValidationIssue `json:"issues"`
}

func (e ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "workflow completion is invalid"
	}
	messages := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		if strings.TrimSpace(issue.Field) != "" {
			messages = append(messages, issue.Field+": "+issue.Message)
			continue
		}
		messages = append(messages, issue.Message)
	}
	return "workflow completion is invalid: " + strings.Join(messages, "; ")
}

func DecodeCompletion(raw json.RawMessage, contract CompletionContract) (ParsedCompletion, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ParsedCompletion{}, ValidationError{Issues: []ValidationIssue{{
			Code:    "invalid_json",
			Message: "completion must be a JSON object",
		}}}
	}
	if payload == nil {
		return ParsedCompletion{}, ValidationError{Issues: []ValidationIssue{{
			Code:    "invalid_json",
			Message: "completion must be a JSON object",
		}}}
	}
	transitions := normalizedTransitions(contract.Transitions)
	knownParameters := parameterSet(schemaParameters(transitions))
	parsed := ParsedCompletion{OutputValues: map[string]string{}}
	issues := []ValidationIssue{}
	seen := map[string]bool{}
	invalidFields := map[string]bool{}
	nullParameters := map[string]bool{}
	for _, key := range sortedMapKeys(payload) {
		value := payload[key]
		field := strings.TrimSpace(key)
		if field == "" {
			issues = append(issues, ValidationIssue{Code: "invalid_field", Message: "field name is required"})
			continue
		}
		seen[field] = true
		switch field {
		case "transition":
			text, ok, issue := decodeStringValue(value, field)
			if !ok {
				issues = append(issues, issue)
				invalidFields[field] = true
				continue
			}
			parsed.TransitionID = strings.TrimSpace(text)
		case "commentary":
			text, ok, issue := decodeOptionalStringValue(value, field)
			if !ok {
				issues = append(issues, issue)
				invalidFields[field] = true
				continue
			}
			parsed.Commentary = text
		default:
			if field == "transition_id" {
				issues = append(issues, ValidationIssue{Code: "unknown_field", Field: field, Message: "field is not part of the workflow completion schema"})
				continue
			}
			if !knownParameters[field] {
				issues = append(issues, ValidationIssue{Code: "unknown_parameter", Field: field, Message: "parameter is not declared by the advertised completion contract"})
				continue
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				nullParameters[field] = true
				continue
			}
			text, ok, issue := decodeParameterValue(value, field)
			if !ok {
				issues = append(issues, issue)
				invalidFields[field] = true
				continue
			}
			parsed.OutputValues[field] = text
		}
	}
	selected := CompletionTransition{}
	hasSelected := false
	if !invalidFields["transition"] {
		var transitionIssues []ValidationIssue
		selected, hasSelected, transitionIssues = selectedTransition(parsed.TransitionID, seen["transition"], transitions)
		issues = append(issues, transitionIssues...)
	}
	if hasSelected {
		parsed.TransitionID = strings.TrimSpace(selected.ID)
		selectedParameters := normalizedParameters(selected.Parameters)
		selectedParameterSet := parameterSet(selectedParameters)
		for _, key := range sortedMapKeys(nullParameters) {
			if selectedParameterSet[key] {
				parsed.OutputValues[key] = "null"
			}
		}
		for _, key := range sortedMapKeys(parsed.OutputValues) {
			if !selectedParameterSet[key] {
				issues = append(issues, ValidationIssue{Code: "unexpected_parameter", Field: key, Message: "parameter is not declared by the selected transition"})
			}
		}
		for _, parameter := range selectedParameters {
			key := strings.TrimSpace(parameter.Key)
			if nullParameters[key] {
				continue
			}
			if strings.TrimSpace(parsed.OutputValues[key]) == "" {
				issues = append(issues, ValidationIssue{Code: "required_parameter_missing", Field: key, Message: "parameter is required by the selected transition"})
			}
		}
	}
	if len(issues) > 0 {
		return ParsedCompletion{}, ValidationError{Issues: issues}
	}
	return parsed, nil
}

func DecodeUnstructuredCompletion(content string, contract CompletionContract) (ParsedCompletion, error) {
	raw := strings.TrimSpace(content)
	if raw == "" {
		return ParsedCompletion{}, ValidationError{Issues: []ValidationIssue{{
			Code:    "invalid_json",
			Message: "completion must be a JSON object",
		}}}
	}
	return DecodeCompletion(json.RawMessage(raw), contract)
}

func decodeOptionalStringValue(value json.RawMessage, field string) (string, bool, ValidationIssue) {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", true, ValidationIssue{}
	}
	return decodeStringValue(value, field)
}

func decodeStringValue(value json.RawMessage, field string) (string, bool, ValidationIssue) {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", false, ValidationIssue{Code: "non_string_value", Field: field, Message: "value must be a string"}
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", false, ValidationIssue{Code: "non_string_value", Field: field, Message: "value must be a string"}
	}
	return text, true, ValidationIssue{}
}

func decodeParameterValue(value json.RawMessage, field string) (string, bool, ValidationIssue) {
	trimmed := bytes.TrimSpace(value)
	if bytes.Equal(trimmed, []byte("null")) {
		return "", false, ValidationIssue{Code: "non_string_value", Field: field, Message: "value must be a string"}
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text, true, ValidationIssue{}
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, trimmed); err != nil {
		return "", false, ValidationIssue{Code: "invalid_json", Field: field, Message: "value must be valid JSON"}
	}
	return compacted.String(), true, ValidationIssue{}
}

func sortedMapKeys[M ~map[K]V, K cmp.Ordered, V any](values M) []K {
	return slices.Sorted(maps.Keys(values))
}

func ToolErrorPayload(err error) json.RawMessage {
	issues := []ValidationIssue{{Code: "invalid_completion", Message: strings.TrimSpace(err.Error())}}
	var validation ValidationError
	if errors.As(err, &validation) {
		issues = validation.Issues
	}
	raw, marshalErr := json.Marshal(map[string]any{
		"error":  "workflow completion rejected",
		"issues": issues,
	})
	if marshalErr != nil {
		return json.RawMessage(`{"error":"workflow completion rejected"}`)
	}
	return raw
}

func ToolSuccessPayload(result CompletionResult) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"status":     "completed",
		"transition": string(result.TransitionID),
		"state":      result.State,
	})
	if err != nil {
		return json.RawMessage(`{"status":"completed"}`)
	}
	return raw
}

func normalizeStoreCompletionError(err error) error {
	var validation workflowstore.CompletionValidationError
	if !errors.As(err, &validation) {
		return err
	}
	issues := make([]ValidationIssue, 0, len(validation.Issues))
	for _, issue := range validation.Issues {
		issues = append(issues, normalizeStoreValidationIssue(issue))
	}
	return ValidationError{Issues: issues}
}

func normalizeStoreValidationIssue(issue workflowstore.CompletionValidationIssue) ValidationIssue {
	field := strings.TrimSpace(issue.Field)
	code := strings.TrimSpace(issue.Code)
	message := strings.TrimSpace(issue.Message)
	switch code {
	case "transition_id_required":
		return ValidationIssue{Code: "transition_required", Field: "transition", Message: "transition is required when multiple transitions are available"}
	case "invalid_transition_id":
		return ValidationIssue{Code: "invalid_transition", Field: "transition", Message: "transition is not available in the advertised completion contract"}
	case "no_outgoing_transition":
		return ValidationIssue{Code: code, Field: "transition", Message: "no outgoing transition is available in the advertised completion contract"}
	case "required_output_missing":
		return ValidationIssue{Code: "required_parameter_missing", Field: field, Message: "parameter is required by the selected transition"}
	case "unknown_output_field":
		return ValidationIssue{Code: "unknown_parameter", Field: field, Message: "parameter is not declared by the advertised completion contract"}
	case "output_field_required":
		return ValidationIssue{Code: "parameter_required", Field: field, Message: "parameter name is required"}
	case "output_too_large":
		return ValidationIssue{Code: "parameter_too_large", Field: field, Message: "parameter value is too large"}
	}
	if field == "transition_id" {
		field = "transition"
	}
	return ValidationIssue{Code: code, Field: field, Message: message}
}

func selectedTransition(value string, provided bool, transitions []CompletionTransition) (CompletionTransition, bool, []ValidationIssue) {
	if len(transitions) == 0 {
		return CompletionTransition{}, false, []ValidationIssue{{Code: "no_outgoing_transition", Field: "transition", Message: "no outgoing transition is available for this Current Node execution"}}
	}
	transitionID := strings.TrimSpace(value)
	if transitionID == "" {
		if len(transitions) == 1 {
			return transitions[0], true, nil
		}
		message := "transition is required when multiple transitions are available"
		if provided {
			message = "transition must be non-empty when multiple transitions are available"
		}
		return CompletionTransition{}, false, []ValidationIssue{{Code: "required_field_missing", Field: "transition", Message: message}}
	}
	for _, transition := range transitions {
		if strings.TrimSpace(transition.ID) == transitionID {
			return transition, true, nil
		}
	}
	return CompletionTransition{}, false, []ValidationIssue{{Code: "invalid_transition", Field: "transition", Message: "transition is not declared by the advertised completion contract"}}
}

func normalizedTransitions(transitions []CompletionTransition) []CompletionTransition {
	out := []CompletionTransition{}
	seen := map[string]bool{}
	for _, transition := range transitions {
		id := strings.TrimSpace(transition.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, CompletionTransition{
			ID:          id,
			DisplayName: strings.TrimSpace(transition.DisplayName),
			Description: strings.TrimSpace(transition.Description),
			Parameters:  normalizedParameters(transition.Parameters),
		})
	}
	return out
}

func normalizedParameters(parameters []workflow.Parameter) []workflow.Parameter {
	out := []workflow.Parameter{}
	seen := map[string]bool{}
	for _, parameter := range parameters {
		key := strings.TrimSpace(parameter.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, workflow.Parameter{Key: key, Description: strings.TrimSpace(parameter.Description)})
	}
	return out
}

func schemaParameters(transitions []CompletionTransition) []workflow.Parameter {
	out := []workflow.Parameter{}
	seen := map[string]bool{}
	for _, transition := range transitions {
		for _, parameter := range normalizedParameters(transition.Parameters) {
			key := strings.TrimSpace(parameter.Key)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, workflow.Parameter{Key: key, Description: strings.TrimSpace(parameter.Description)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.TrimSpace(out[i].Key) < strings.TrimSpace(out[j].Key)
	})
	return out
}

func parameterSet(parameters []workflow.Parameter) map[string]bool {
	out := make(map[string]bool, len(parameters))
	for _, parameter := range parameters {
		key := strings.TrimSpace(parameter.Key)
		if key != "" {
			out[key] = true
		}
	}
	return out
}

func sortedTransitionIDs(transitions []CompletionTransition) []string {
	out := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		id := strings.TrimSpace(transition.ID)
		if id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
