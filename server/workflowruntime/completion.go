package workflowruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/jsoncontract"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"

	invjsonschema "github.com/invopop/jsonschema"
)

const (
	CompleteNodeToolName = "complete_node"
	structuredOutputName = "workflow_completion"
)

// ErrStructuredOutputUnsupported is returned when structured-output completion
// is requested but the provider lacks responses-API support.
var ErrStructuredOutputUnsupported = errors.New("workflow structured output completion requires provider responses API support")
var ErrShellCompletionUnavailable = errors.New("workflow shell-command completion requires exec_command")

type CompletionMode = sessioncontract.WorkflowCompletionMode

const (
	CompletionModeStructuredOutput   = sessioncontract.WorkflowCompletionModeStructuredOutput
	CompletionModeTool               = sessioncontract.WorkflowCompletionModeTool
	CompletionModeShellCommand       = sessioncontract.WorkflowCompletionModeShellCommand
	CompletionModeUnstructuredOutput = sessioncontract.WorkflowCompletionModeUnstructuredOutput
)

type CompletionModeSelection struct {
	ConfiguredMode         config.WorkflowCompletionMode
	ProviderCapabilities   llm.ProviderCapabilities
	HasContinueSessionEdge bool
	ShellAvailable         bool
}

type CompletionContract struct {
	Transitions []CompletionTransition
	function    jsoncontract.Function
	structured  jsoncontract.Structured
	accepted    jsoncontract.Function
	prepared    bool
}

type CompletionTransition struct {
	ID          string
	DisplayName string
	Description string
	Parameters  []workflow.Parameter
}

// TaskPromptDelivery identifies whether the first turn in one process-local
// execution delivers a new Node assignment or resumes the current assignment.
type TaskPromptDelivery uint8

const (
	TaskPromptDeliveryAssignment TaskPromptDelivery = iota
	TaskPromptDeliveryResume
)

// CurrentNodeExecutionConfig is the live, process-local control contract for
// one admitted Current Node.
type CurrentNodeExecutionConfig struct {
	ScopeID                      runtimeids.ExecutionScopeID
	TaskPromptDelivery           TaskPromptDelivery
	Contract                     CompletionContract
	CompletionMode               CompletionMode
	MaxInvalidCompletionAttempts int
	UseAutomaticToolChoice       bool
	Controller                   Controller
	TaskAwarenessSource          TaskAwarenessSource
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
	TaskAwareness          TaskAwareness
}

type TaskAwareness struct {
	CommentCount               int64
	UnsatisfiedDependencyCount int64
}

type TaskAwarenessSource interface {
	TaskAwareness(context.Context, workflow.TaskID) (TaskAwareness, error)
}

type TaskInstructions struct {
	CurrentNode      workflow.CurrentNodeReference
	TaskShortID      string
	TaskTitle        string
	TaskBody         string
	WorkflowID       runtimeids.WorkflowID
	WorkflowName     string
	NodeKey          string
	NodeDisplayName  string
	ContextMode      string
	SourceSessionID  string
	Transitions      []TransitionInstruction
	TransitionPrompt string
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
	SessionID    *runtimeids.SessionID
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

// PostCompletionCompactionResult preserves a durable history-replacement
// receipt separately from operational work that ran after that replacement.
type PostCompletionCompactionResult struct {
	CommitReceipt session.CommitReceipt
	Diagnostic    error
}

// PostCompletionRuntime is the live runtime authority supplied by the Agent
// runner to the workflow controller after the completed turn returns.
type PostCompletionRuntime struct {
	UsedTokens          int
	PreCompactionTokens int
	CompactionMode      string
	Compact             func(context.Context) PostCompletionCompactionResult
}

// PostTurnFinalizer owns the process-local completion fence and releases held
// successors only after post-turn finalization has completed.
type PostTurnFinalizer interface {
	FinalizeCurrentNodePostTurn(context.Context, runtimeids.ExecutionScopeID, runtimeids.SessionID, PostCompletionRuntime) error
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
	ScopeID   runtimeids.ExecutionScopeID
	SessionID *runtimeids.SessionID
	Kind      ViolationKind
	MaxCount  int
	Detail    string
}

type ViolationResetRequest struct {
	ScopeID   runtimeids.ExecutionScopeID
	SessionID *runtimeids.SessionID
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
	return sessioncontract.ParseWorkflowCompletionMode(raw)
}

type completionPayloadShape struct {
	Transition *string `json:"transition,omitempty" jsonschema_description:"Transition to take. Required when multiple outgoing transitions are available."`
	Commentary *string `json:"commentary,omitempty" jsonschema:"nullable" jsonschema_description:"Brief explanation of what was completed and why this transition was selected."`
}

type completionSchemaProfile uint8

const (
	completionSchemaFunction completionSchemaProfile = iota
	completionSchemaStructured
	completionSchemaAccepted
)

func NewCompletionContract(transitions []CompletionTransition) (CompletionContract, error) {
	return CompletionContract{Transitions: transitions}.Prepare()
}

func (c CompletionContract) Prepare() (CompletionContract, error) {
	if c.prepared {
		return c, nil
	}
	transitions := normalizedTransitions(c.Transitions)
	preparer := jsoncontract.NewPreparer(false)
	function, err := preparer.Function(
		"workflow completion function",
		completionPayloadShape{},
		completionSchemaCustomizer(transitions, completionSchemaFunction),
	)
	if err != nil {
		return CompletionContract{}, err
	}
	structured, err := preparer.Structured(
		"workflow completion structured output",
		completionPayloadShape{},
		completionSchemaCustomizer(transitions, completionSchemaStructured),
	)
	if err != nil {
		return CompletionContract{}, err
	}
	accepted, err := preparer.Function(
		"workflow completion accepted input",
		completionPayloadShape{},
		completionSchemaCustomizer(transitions, completionSchemaAccepted),
	)
	if err != nil {
		return CompletionContract{}, err
	}
	return CompletionContract{
		Transitions: transitions,
		function:    function,
		structured:  structured,
		accepted:    accepted,
		prepared:    true,
	}, nil
}

func completionSchemaCustomizer(
	transitions []CompletionTransition,
	profile completionSchemaProfile,
) jsoncontract.Customize {
	return func(schema *invjsonschema.Schema) error {
		if schema.Properties == nil {
			schema.Properties = invjsonschema.NewProperties()
		}
		multipleTransitions := len(transitions) > 1
		if profile == completionSchemaAccepted {
			schema.AdditionalProperties = invjsonschema.TrueSchema
		}
		if multipleTransitions {
			if profile != completionSchemaAccepted {
				schema.Properties.Set("transition", completionTransitionSchema(transitions))
				schema.Required = appendUnique(schema.Required, "transition")
			}
		} else if profile != completionSchemaAccepted {
			schema.Properties.Delete("transition")
			schema.Required = removeString(schema.Required, "transition")
		}
		if profile == completionSchemaStructured {
			schema.Properties.Set("commentary", completionNullableStringSchema(
				"Brief explanation of what was completed and why this transition was selected.",
			))
			schema.Required = appendUnique(schema.Required, "commentary")
		}
		for _, parameter := range schemaParameters(transitions) {
			name := strings.TrimSpace(parameter.Key)
			switch profile {
			case completionSchemaAccepted:
				schema.Properties.Set(name, &invjsonschema.Schema{
					Description: strings.TrimSpace(parameter.Description),
				})
			case completionSchemaFunction, completionSchemaStructured:
				schema.Properties.Set(
					name,
					completionParameterSchema(parameter, multipleTransitions),
				)
				schema.Required = appendUnique(schema.Required, name)
			}
		}
		return nil
	}
}

func completionTransitionSchema(transitions []CompletionTransition) *invjsonschema.Schema {
	ids := sortedTransitionIDs(transitions)
	values := make([]any, len(ids))
	for index, id := range ids {
		values[index] = id
	}
	return &invjsonschema.Schema{
		Type:        "string",
		Enum:        values,
		Description: "Transition to take. Required when multiple outgoing transitions are available.",
	}
}

func completionParameterSchema(
	parameter workflow.Parameter,
	nullable bool,
) *invjsonschema.Schema {
	description := strings.TrimSpace(parameter.Description)
	if !nullable {
		return &invjsonschema.Schema{Type: "string", Description: description}
	}
	if description == "" {
		description = "Set to a string only when this parameter belongs to the selected transition; otherwise set null."
	} else {
		description += " Set to null when this parameter does not belong to the selected transition."
	}
	return completionNullableStringSchema(description)
}

func completionNullableStringSchema(description string) *invjsonschema.Schema {
	return &invjsonschema.Schema{
		AnyOf: []*invjsonschema.Schema{
			{Type: "string"},
			{Type: "null"},
		},
		Description: description,
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func removeString(values []string, value string) []string {
	filtered := values[:0]
	for _, existing := range values {
		if existing != value {
			filtered = append(filtered, existing)
		}
	}
	return filtered
}

func StructuredOutput(contract CompletionContract) (*llm.StructuredOutput, error) {
	schema, err := StructuredSchema(contract)
	if err != nil {
		return nil, err
	}
	return &llm.StructuredOutput{
		Name:        structuredOutputName,
		Description: "Complete the current workflow node by selecting a transition and returning required transition parameters.",
		Schema:      schema,
	}, nil
}

func StructuredSchema(contract CompletionContract) (jsoncontract.Structured, error) {
	if !contract.prepared {
		return jsoncontract.Structured{}, errors.New("workflow completion contract is not prepared")
	}
	return contract.structured, nil
}

func FunctionSchema(contract CompletionContract) (jsoncontract.Function, error) {
	if !contract.prepared {
		return jsoncontract.Function{}, errors.New("workflow completion contract is not prepared")
	}
	return contract.function, nil
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
	if !contract.prepared {
		return ParsedCompletion{}, errors.New("workflow completion contract is not prepared")
	}
	payload, err := contract.accepted.ValidateValue(raw)
	if err != nil {
		return ParsedCompletion{}, err
	}
	transitions := contract.Transitions
	parsed := ParsedCompletion{OutputValues: map[string]string{}}
	issues := []ValidationIssue{}
	nullParameters := map[string]bool{}
	transitionValue, transitionProvided := payload.Field("transition")
	if transitionProvided {
		parsed.TransitionID, _ = transitionValue.String()
		parsed.TransitionID = strings.TrimSpace(parsed.TransitionID)
	}
	commentaryValue, commentaryProvided := payload.Field("commentary")
	if commentaryProvided && !commentaryValue.IsNull() {
		parsed.Commentary, _ = commentaryValue.String()
	}
	for _, parameter := range schemaParameters(transitions) {
		key := strings.TrimSpace(parameter.Key)
		value, present := payload.Field(key)
		if !present {
			continue
		}
		if value.IsNull() {
			nullParameters[key] = true
			continue
		}
		if text, ok := value.String(); ok {
			parsed.OutputValues[key] = text
			continue
		}
		compact, compactErr := value.CompactJSON()
		if compactErr != nil {
			return ParsedCompletion{}, compactErr
		}
		parsed.OutputValues[key] = string(compact)
	}
	selected := CompletionTransition{}
	hasSelected := false
	selected, hasSelected, transitionIssues := selectedTransition(
		parsed.TransitionID,
		transitionProvided,
		transitions,
	)
	issues = append(issues, transitionIssues...)
	if hasSelected {
		parsed.TransitionID = strings.TrimSpace(selected.ID)
		selectedParameters := normalizedParameters(selected.Parameters)
		selectedParameterSet := parameterSet(selectedParameters)
		for key := range nullParameters {
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
			if strings.TrimSpace(parsed.OutputValues[key]) == "" {
				issues = append(issues, requiredParameterMissingIssue(parameter))
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

func sortedMapKeys[M ~map[string]V, V any](values M) []string {
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

func requiredParameterMissingIssue(parameter workflow.Parameter) ValidationIssue {
	if workflow.CanonicalParameterPurpose(parameter.Purpose) == workflow.ParameterPurposeTargetAssignee {
		return ValidationIssue{
			Code:    "workflow.target_agent.unavailable_role",
			Field:   strings.TrimSpace(parameter.Key),
			Message: "a selectable target Agent role is required",
		}
	}
	return ValidationIssue{
		Code:    "required_parameter_missing",
		Field:   strings.TrimSpace(parameter.Key),
		Message: "parameter is required by the selected transition",
	}
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
		out = append(out, workflow.Parameter{
			Key:         key,
			Description: strings.TrimSpace(parameter.Description),
			Purpose:     workflow.CanonicalParameterPurpose(parameter.Purpose),
		})
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
			out = append(out, workflow.Parameter{
				Key:         key,
				Description: strings.TrimSpace(parameter.Description),
				Purpose:     workflow.CanonicalParameterPurpose(parameter.Purpose),
			})
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
