package workflow

import (
	"strings"

	"core/shared/config"
)

type WorkflowID string
type NodeID string
type TransitionGroupID string
type EdgeID string
type TaskID string
type PlacementID string
type RunID string
type TransitionID string
type ModelKey string

type NodeKind string

const (
	NodeKindStart    NodeKind = "start"
	NodeKindAgent    NodeKind = "agent"
	NodeKindJoin     NodeKind = "join"
	NodeKindTerminal NodeKind = "terminal"
)

type ContextMode string

const (
	ContextModeNewSession                ContextMode = "new_session"
	ContextModeContinueSession           ContextMode = "continue_session"
	ContextModeCompactAndContinueSession ContextMode = "compact_and_continue_session"
)

type ContextSourceKind string

const (
	ContextSourceImmediateSource ContextSourceKind = "immediate_source"
	ContextSourceSelectedNode    ContextSourceKind = "selected_node"
	ContextSourcePreviousTarget  ContextSourceKind = "previous_target"
)

type ContextSource struct {
	Kind    ContextSourceKind `json:"kind"`
	NodeKey ModelKey          `json:"node_key,omitempty"`
}

func CanonicalContextSource(source ContextSource) ContextSource {
	kind := ContextSourceKind(strings.TrimSpace(string(source.Kind)))
	nodeKey := ModelKey(strings.TrimSpace(string(source.NodeKey)))
	if kind == "" || kind == ContextSourceImmediateSource {
		return ContextSource{Kind: ContextSourceImmediateSource}
	}
	if kind == ContextSourcePreviousTarget {
		return ContextSource{Kind: ContextSourcePreviousTarget}
	}
	return ContextSource{Kind: kind, NodeKey: nodeKey}
}

type BindingSource string

const (
	BindingSourceTask             BindingSource = "task"
	BindingSourceTransitionOutput BindingSource = "transition_output"
	BindingSourceJoin             BindingSource = "join"
)

const (
	MaxModelKeyChars               = 64
	MaxDisplayNameChars            = 120
	MaxOutputFieldNameChars        = 64
	MaxOutputFieldDescriptionChars = 1000
	MaxInputFieldNameChars         = MaxOutputFieldNameChars
	MaxInputFieldDescriptionChars  = MaxOutputFieldDescriptionChars
	MaxParameterKeyChars           = MaxOutputFieldNameChars
	MaxParameterDescriptionChars   = MaxOutputFieldDescriptionChars
	MaxOutputValueBytes            = 64 * 1024
	MaxCommentaryBytes             = 64 * 1024
	MaxTaskCommentBytes            = 256 * 1024
)

const RuntimePromptParameterCommentary = "commentary"

type Definition struct {
	ID               WorkflowID
	DisplayName      string
	NodeGroups       []NodeGroup
	Nodes            []Node
	TransitionGroups []TransitionGroup
	Edges            []Edge
}

type NodeGroup struct {
	WorkflowID    WorkflowID
	ID            string
	Key           ModelKey
	DisplayName   string
	MemberNodeIDs []NodeID
}

type Node struct {
	WorkflowID         WorkflowID
	ID                 NodeID
	Key                ModelKey
	DisplayName        string
	Kind               NodeKind
	GroupID            string
	SubagentRole       string
	PromptTemplate     string
	CompletionMode     string
	InputFields        []InputField
	JoinInputProviders []JoinInputProvider
	OutputFields       []OutputField
}

type TransitionGroup struct {
	WorkflowID   WorkflowID
	ID           TransitionGroupID
	SourceNodeID NodeID
	TransitionID TransitionID
	DisplayName  string
	Description  string
}

type Edge struct {
	WorkflowID         WorkflowID
	ID                 EdgeID
	Key                ModelKey
	TransitionGroupID  TransitionGroupID
	TargetNodeID       NodeID
	ContextMode        ContextMode
	ContextSource      ContextSource
	RequiresApproval   bool
	PromptTemplate     string
	Parameters         []Parameter
	InputBindings      []InputBinding
	OutputRequirements []OutputRequirement
}

type Parameter struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

type OutputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type JoinInputProvider struct {
	InputName      string `json:"input_name"`
	ProviderEdgeID EdgeID `json:"provider_edge_id"`
}

type OutputRequirement struct {
	FieldName string `json:"field_name"`
}

type TemplatePlaceholder string

type InputBinding struct {
	Name   string        `json:"name"`
	Source BindingSource `json:"source"`
	Field  string        `json:"field"`
}

type ValidationContext string

const (
	ValidationContextDraft        ValidationContext = "draft"
	ValidationContextTaskCreation ValidationContext = "task_creation"
	ValidationContextExecution    ValidationContext = "execution"
)

type RoleResolver interface {
	RoleExists(role string) bool
}

const DefaultAgentRole = config.DefaultSubagentRole

func IsDefaultAgentRole(role string) bool {
	return strings.TrimSpace(role) == DefaultAgentRole
}

type StaticRoleResolver map[string]bool

func (r StaticRoleResolver) RoleExists(role string) bool {
	if IsDefaultAgentRole(role) {
		return true
	}
	return r[role]
}

type ValidationOptions struct {
	Context      ValidationContext
	RoleResolver RoleResolver
}

type ValidationErrorCode string

const (
	CodeMissingWorkflowID              ValidationErrorCode = "workflow.validation.missing_workflow_id"
	CodeMissingNodeID                  ValidationErrorCode = "workflow.validation.missing_node_id"
	CodeDuplicateNodeID                ValidationErrorCode = "workflow.validation.duplicate_node_id"
	CodeMissingNodeKey                 ValidationErrorCode = "workflow.validation.missing_node_key"
	CodeInvalidNodeKey                 ValidationErrorCode = "workflow.validation.invalid_node_key"
	CodeDuplicateNodeKey               ValidationErrorCode = "workflow.validation.duplicate_node_key"
	CodeMissingStartNode               ValidationErrorCode = "workflow.validation.missing_start_node"
	CodeMultipleStartNodes             ValidationErrorCode = "workflow.validation.multiple_start_nodes"
	CodeInvalidStartNode               ValidationErrorCode = "workflow.validation.invalid_start_node"
	CodeInvalidStartOutgoingShape      ValidationErrorCode = "workflow.validation.invalid_start_outgoing_shape"
	CodeTerminalHasOutgoingEdge        ValidationErrorCode = "workflow.validation.terminal_has_outgoing_edge"
	CodeTerminalIsExecutable           ValidationErrorCode = "workflow.validation.terminal_is_executable"
	CodeJoinIsExecutable               ValidationErrorCode = "workflow.validation.join_is_executable"
	CodeInvalidJoinNode                ValidationErrorCode = "workflow.validation.invalid_join_node"
	CodeInvalidJoinOutgoingShape       ValidationErrorCode = "workflow.validation.invalid_join_outgoing_shape"
	CodeNodeUnreachableFromStart       ValidationErrorCode = "workflow.validation.node_unreachable_from_start"
	CodeNonTerminalCannotReachTerminal ValidationErrorCode = "workflow.validation.non_terminal_cannot_reach_terminal"
	CodeMissingTransitionGroupID       ValidationErrorCode = "workflow.validation.missing_transition_group_id"
	CodeDuplicateTransitionGroupID     ValidationErrorCode = "workflow.validation.duplicate_transition_group_id"
	CodeEmptyTransitionGroup           ValidationErrorCode = "workflow.validation.empty_transition_group"
	CodeMissingTransitionID            ValidationErrorCode = "workflow.validation.missing_transition_id"
	CodeInvalidTransitionID            ValidationErrorCode = "workflow.validation.invalid_transition_id"
	CodeDuplicateTransitionID          ValidationErrorCode = "workflow.validation.duplicate_transition_id"
	CodeEdgeTransitionGroupMissing     ValidationErrorCode = "workflow.validation.edge_transition_group_missing"
	CodeMissingEdgeID                  ValidationErrorCode = "workflow.validation.missing_edge_id"
	CodeDuplicateEdgeID                ValidationErrorCode = "workflow.validation.duplicate_edge_id"
	CodeMissingEdgeKey                 ValidationErrorCode = "workflow.validation.missing_edge_key"
	CodeInvalidEdgeKey                 ValidationErrorCode = "workflow.validation.invalid_edge_key"
	CodeDuplicateEdgeKey               ValidationErrorCode = "workflow.validation.duplicate_edge_key"
	CodeEdgeTargetMissing              ValidationErrorCode = "workflow.validation.edge_target_missing"
	CodeCrossWorkflowReference         ValidationErrorCode = "workflow.validation.cross_workflow_reference"
	CodeInvalidOutputField             ValidationErrorCode = "workflow.validation.invalid_output_field"
	CodeDuplicateOutputField           ValidationErrorCode = "workflow.validation.duplicate_output_field"
	CodeOutputFieldDescriptionRequired ValidationErrorCode = "workflow.validation.output_field_description_required"
	CodeOutputSchemaTooLarge           ValidationErrorCode = "workflow.validation.output_schema_too_large"
	CodeInvalidInputField              ValidationErrorCode = "workflow.validation.invalid_input_field"
	CodeDuplicateInputField            ValidationErrorCode = "workflow.validation.duplicate_input_field"
	CodeInputFieldDescriptionRequired  ValidationErrorCode = "workflow.validation.input_field_description_required"
	CodeInputSchemaTooLarge            ValidationErrorCode = "workflow.validation.input_schema_too_large"
	CodeInvalidParameter               ValidationErrorCode = "workflow.validation.invalid_parameter"
	CodeDuplicateParameter             ValidationErrorCode = "workflow.validation.duplicate_parameter"
	CodeParameterDescriptionRequired   ValidationErrorCode = "workflow.validation.parameter_description_required"
	CodeParameterSchemaTooLarge        ValidationErrorCode = "workflow.validation.parameter_schema_too_large"
	CodeTransitionPromptRequired       ValidationErrorCode = "workflow.validation.transition_prompt_required"
	CodeTransitionPromptForbidden      ValidationErrorCode = "workflow.validation.transition_prompt_forbidden"
	CodeUnknownOutputRequirement       ValidationErrorCode = "workflow.validation.unknown_output_requirement"
	CodeInvalidInputBinding            ValidationErrorCode = "workflow.validation.invalid_input_binding"
	CodeInvalidTemplatePlaceholder     ValidationErrorCode = "workflow.validation.invalid_template_placeholder"
	CodeProvisionFieldOverlap          ValidationErrorCode = "workflow.validation.provision_field_overlap"
	CodeMissingJoinInputProvider       ValidationErrorCode = "workflow.validation.missing_join_input_provider"
	CodeDuplicateJoinInputProvider     ValidationErrorCode = "workflow.validation.duplicate_join_input_provider"
	CodeInvalidJoinInputProvider       ValidationErrorCode = "workflow.validation.invalid_join_input_provider"
	CodeInvalidFirstNodeInput          ValidationErrorCode = "workflow.validation.invalid_first_node_input"
	CodeInvalidContextMode             ValidationErrorCode = "workflow.validation.invalid_context_mode"
	CodeInvalidContextSource           ValidationErrorCode = "workflow.validation.invalid_context_source"
	CodeInvalidFanoutJoinTopology      ValidationErrorCode = "workflow.validation.invalid_fanout_join_topology"
	CodeInvalidNodeGroup               ValidationErrorCode = "workflow.validation.invalid_node_group"
	CodeUnsupportedContextMode         ValidationErrorCode = "workflow.validation.unsupported_context_mode"
	CodeUnsupportedApprovalExecution   ValidationErrorCode = "workflow.validation.unsupported_approval_execution"
	CodeUnsupportedJoinExecution       ValidationErrorCode = "workflow.validation.unsupported_join_execution"
	CodeUnsupportedJoinBinding         ValidationErrorCode = "workflow.validation.unsupported_join_binding"
	CodeAgentRoleRequired              ValidationErrorCode = "workflow.validation.agent_role_required"
	CodeAgentRoleMissing               ValidationErrorCode = "workflow.validation.agent_role_missing"
	CodeInvalidNodeKind                ValidationErrorCode = "workflow.validation.invalid_node_kind"
	CodeInvalidDisplayName             ValidationErrorCode = "workflow.validation.invalid_display_name"
)

type ValidationError struct {
	Code              ValidationErrorCode
	Message           string
	WorkflowID        WorkflowID
	NodeID            NodeID
	TransitionGroupID TransitionGroupID
	EdgeID            EdgeID
	FieldName         string
	InputName         string
	Placeholder       string
	ProviderEdgeID    EdgeID
	RelatedIDs        []string
	BlocksContext     bool
}

type RuntimeSupportEdge struct {
	ContextMode      ContextMode
	RequiresApproval bool
	TargetKind       NodeKind
	InputBindings    []InputBinding
}

type RuntimeSupportIssue struct {
	Code    ValidationErrorCode
	Message string
}

func UnsupportedRuntimeFeatures(edge RuntimeSupportEdge) []RuntimeSupportIssue {
	issues := []RuntimeSupportIssue{}
	return issues
}

type ValidationResult struct {
	Context ValidationContext
	Errors  []ValidationError
}

func (r ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r ValidationResult) HasBlockingErrors() bool {
	for _, err := range r.Errors {
		if err.BlocksContext {
			return true
		}
	}
	return false
}

func (r ValidationResult) BlockingErrors() []ValidationError {
	out := make([]ValidationError, 0, len(r.Errors))
	for _, err := range r.Errors {
		if err.BlocksContext {
			out = append(out, err)
		}
	}
	return out
}

func (r ValidationResult) Codes() []ValidationErrorCode {
	out := make([]ValidationErrorCode, 0, len(r.Errors))
	for _, err := range r.Errors {
		out = append(out, err.Code)
	}
	return out
}
