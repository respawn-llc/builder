package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/workflowcontract"
	"core/shared/workflowkey"
	"core/shared/worktreecontract"
)

const (
	WorkflowRequestErrorRequired     = "workflow.request.required"
	WorkflowRequestErrorInvalidKey   = "workflow.request.invalid_key"
	WorkflowRequestErrorInvalidValue = "workflow.request.invalid_value"
	WorkflowRequestErrorInvalidMode  = "workflow.request.invalid_mode"
	WorkflowRequestErrorTooLong      = "workflow.request.too_long"
)

const WorkflowPaginationMaxLimit = OffsetPaginationMaxLimit
const WorkflowTaskListMaxSortSelectors = 7
const WorkflowBoardNodeCardsMaxPageSize = 25

type WorkflowNodeKind string

const (
	WorkflowNodeKindStart    WorkflowNodeKind = "start"
	WorkflowNodeKindAgent    WorkflowNodeKind = "agent"
	WorkflowNodeKindScript   WorkflowNodeKind = "script"
	WorkflowNodeKindJoin     WorkflowNodeKind = "join"
	WorkflowNodeKindTerminal WorkflowNodeKind = "terminal"
)

const (
	WorkflowGraphDraftMaxNodeGroups       = 200
	WorkflowGraphDraftMaxNodes            = 200
	WorkflowGraphDraftMaxTransitionGroups = 1000
	WorkflowGraphDraftMaxEdges            = 1000
	WorkflowGraphDraftMaxFieldsPerEntity  = 200
)

type WorkflowRequestValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e WorkflowRequestValidationError) Error() string {
	if strings.TrimSpace(e.Field) == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

type WorkflowValidationMode string

const (
	WorkflowValidationModeDraft        WorkflowValidationMode = "draft"
	WorkflowValidationModeTaskCreation WorkflowValidationMode = "task_creation"
	WorkflowValidationModeExecution    WorkflowValidationMode = "execution"
)

type WorkflowProjectLinkDefaultMode string

const (
	WorkflowProjectLinkDefaultNever            WorkflowProjectLinkDefaultMode = "never"
	WorkflowProjectLinkDefaultAlways           WorkflowProjectLinkDefaultMode = "always"
	WorkflowProjectLinkDefaultIfProjectHasNone WorkflowProjectLinkDefaultMode = "if_project_has_none"
)

type WorkflowRecord struct {
	ID                    runtimeids.WorkflowID                `json:"id"`
	Name                  string                               `json:"name"`
	Description           string                               `json:"description"`
	Version               int64                                `json:"version"`
	ExecutionTargetPolicy WorkflowExecutionTargetConfiguration `json:"execution_target_policy"`
	ProjectLink           *WorkflowListProjectLink             `json:"project_link,omitempty"`
}

type WorkflowListProjectLink struct {
	Default bool `json:"default"`
}

type WorkflowNode struct {
	ID                 string                      `json:"id"`
	WorkflowID         runtimeids.WorkflowID       `json:"workflow_id"`
	Key                string                      `json:"key"`
	Kind               string                      `json:"kind"`
	DisplayName        string                      `json:"display_name"`
	GroupID            *string                     `json:"group_id"`
	GroupKey           string                      `json:"group_key,omitempty"`
	SubagentRole       string                      `json:"subagent_role,omitempty"`
	CompletionMode     string                      `json:"completion_mode,omitempty"`
	ScriptPath         *string                     `json:"script_path,omitempty"`
	JoinInputProviders []WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
}

type WorkflowNodeGroup struct {
	GroupID     string                `json:"group_id"`
	WorkflowID  runtimeids.WorkflowID `json:"workflow_id"`
	GroupKey    string                `json:"group_key"`
	DisplayName string                `json:"display_name"`
	SortOrder   int                   `json:"sort_order"`
}

type WorkflowTransitionGroup struct {
	ID           string                `json:"id"`
	WorkflowID   runtimeids.WorkflowID `json:"workflow_id"`
	SourceNodeID string                `json:"source_node_id"`
	TransitionID string                `json:"transition_id"`
	DisplayName  string                `json:"display_name"`
	Description  string                `json:"description,omitempty"`
}

type WorkflowEdge struct {
	ID                 string                      `json:"id"`
	WorkflowID         runtimeids.WorkflowID       `json:"workflow_id"`
	TransitionGroupID  string                      `json:"transition_group_id"`
	Key                string                      `json:"key"`
	TargetNodeID       string                      `json:"target_node_id"`
	AssigneeSelection  string                      `json:"assignee_selection"`
	ThinkingSelection  string                      `json:"thinking_selection"`
	RequiresApproval   bool                        `json:"requires_approval"`
	ContextMode        string                      `json:"context_mode"`
	ContextSource      WorkflowContextSource       `json:"context_source"`
	PromptTemplate     string                      `json:"prompt_template,omitempty"`
	Parameters         []WorkflowParameter         `json:"parameters,omitempty"`
	InputBindings      []WorkflowInputBinding      `json:"input_bindings,omitempty"`
	OutputRequirements []WorkflowOutputRequirement `json:"output_requirements,omitempty"`
}

type WorkflowContextSource struct {
	Kind    string `json:"kind"`
	NodeKey string `json:"node_key,omitempty"`
}

// WorkflowOutputField is read-only/derived in workflow editor contracts. It is used for runtime
// output snapshots, board summaries, and derived provision fields.
type WorkflowOutputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type WorkflowParameter struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Purpose     string `json:"purpose"`
}

type WorkflowJoinInputProvider struct {
	InputName      string `json:"input_name"`
	ProviderEdgeID string `json:"provider_edge_id"`
}

// WorkflowOutputRequirement describes derived runtime/read-model requirements. It is not accepted
// by node/edge add, update, or graph draft requests as canonical user-authored wiring.
type WorkflowOutputRequirement struct {
	FieldName string `json:"field_name"`
}

// WorkflowInputBinding describes derived runtime/read-model bindings. It is not accepted by
// node/edge add, update, or graph draft requests as canonical user-authored wiring.
type WorkflowInputBinding struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Field  string `json:"field"`
}

type WorkflowDerivedWiring struct {
	Nodes            []WorkflowDerivedNodeWiring            `json:"nodes,omitempty"`
	TransitionGroups []WorkflowDerivedTransitionGroupWiring `json:"transition_groups,omitempty"`
	Edges            []WorkflowDerivedEdgeWiring            `json:"edges,omitempty"`
	Diagnostics      []WorkflowValidationError              `json:"diagnostics,omitempty"`
}

type WorkflowDerivedNodeWiring struct {
	NodeID                  string                `json:"node_id"`
	PossibleProvisionFields []WorkflowOutputField `json:"possible_provision_fields,omitempty"`
	JoinOutputFields        []WorkflowOutputField `json:"join_output_fields,omitempty"`
}

type WorkflowDerivedTransitionGroupWiring struct {
	TransitionGroupID       string                `json:"transition_group_id"`
	RequiredProvisionFields []WorkflowOutputField `json:"required_provision_fields,omitempty"`
}

type WorkflowDerivedEdgeWiring struct {
	EdgeID                         string                        `json:"edge_id"`
	InputBindings                  []WorkflowInputBinding        `json:"input_bindings,omitempty"`
	RequiredProvisionFields        []WorkflowOutputField         `json:"required_provision_fields,omitempty"`
	RequiredProviderFields         []WorkflowOutputField         `json:"required_provider_fields,omitempty"`
	AssigneeSelectionApplicability WorkflowSelectorApplicability `json:"assignee_selection_applicability"`
	ThinkingSelectionApplicability WorkflowSelectorApplicability `json:"thinking_selection_applicability"`
}

type WorkflowSelectorApplicabilityReason string

const (
	WorkflowSelectorApplicabilityReasonEligible                 WorkflowSelectorApplicabilityReason = "eligible"
	WorkflowSelectorApplicabilityReasonTopology                 WorkflowSelectorApplicabilityReason = "topology"
	WorkflowSelectorApplicabilityReasonContextSource            WorkflowSelectorApplicabilityReason = "context_source"
	WorkflowSelectorApplicabilityReasonNoCallableRoles          WorkflowSelectorApplicabilityReason = "no_callable_roles"
	WorkflowSelectorApplicabilityReasonNoThinkingSupport        WorkflowSelectorApplicabilityReason = "no_thinking_support"
	WorkflowSelectorApplicabilityReasonUnavailableConfiguration WorkflowSelectorApplicabilityReason = "unavailable_configuration"
	WorkflowSelectorApplicabilityReasonSoleCallableRole         WorkflowSelectorApplicabilityReason = "sole_callable_role"
	WorkflowSelectorApplicabilityReasonNoThinkingLevels         WorkflowSelectorApplicabilityReason = "no_thinking_levels"
	WorkflowSelectorApplicabilityReasonSoleThinkingLevel        WorkflowSelectorApplicabilityReason = "sole_thinking_level"
)

type WorkflowSelectorApplicability struct {
	Available        bool                                `json:"available"`
	ParameterVisible bool                                `json:"parameter_visible"`
	Reason           WorkflowSelectorApplicabilityReason `json:"reason"`
}

func (a WorkflowSelectorApplicability) Validate() error {
	switch a.Reason {
	case WorkflowSelectorApplicabilityReasonEligible,
		WorkflowSelectorApplicabilityReasonTopology,
		WorkflowSelectorApplicabilityReasonContextSource,
		WorkflowSelectorApplicabilityReasonNoCallableRoles,
		WorkflowSelectorApplicabilityReasonNoThinkingSupport,
		WorkflowSelectorApplicabilityReasonUnavailableConfiguration,
		WorkflowSelectorApplicabilityReasonSoleCallableRole,
		WorkflowSelectorApplicabilityReasonNoThinkingLevels,
		WorkflowSelectorApplicabilityReasonSoleThinkingLevel:
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "reason", "selector applicability reason is invalid")
	}
	if a.Reason == WorkflowSelectorApplicabilityReasonEligible && (!a.Available || !a.ParameterVisible) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "available", "eligible selector applicability must be available")
	}
	if a.Reason != WorkflowSelectorApplicabilityReasonEligible && a.Available {
		switch a.Reason {
		case WorkflowSelectorApplicabilityReasonSoleCallableRole,
			WorkflowSelectorApplicabilityReasonNoThinkingLevels,
			WorkflowSelectorApplicabilityReasonSoleThinkingLevel:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "available", "unavailable selector applicability must not be available")
		}
	}
	if a.Reason == WorkflowSelectorApplicabilityReasonSoleCallableRole ||
		a.Reason == WorkflowSelectorApplicabilityReasonNoThinkingLevels ||
		a.Reason == WorkflowSelectorApplicabilityReasonSoleThinkingLevel {
		if !a.Available || a.ParameterVisible {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "parameter_visible", "automatic selector applicability must hide its parameter")
		}
	}
	return nil
}

func (e WorkflowDerivedEdgeWiring) Validate() error {
	if err := validateRequired("edge_id", e.EdgeID); err != nil {
		return err
	}
	if err := e.AssigneeSelectionApplicability.Validate(); err != nil {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "assignee_selection_applicability", err.Error())
	}
	return e.ThinkingSelectionApplicability.Validate()
}

type WorkflowDefinition struct {
	NodeGroups       []WorkflowNodeGroup       `json:"node_groups,omitempty"`
	Workflow         WorkflowRecord            `json:"workflow"`
	Nodes            []WorkflowNode            `json:"nodes"`
	TransitionGroups []WorkflowTransitionGroup `json:"transition_groups"`
	Edges            []WorkflowEdge            `json:"edges"`
	DerivedWiring    WorkflowDerivedWiring     `json:"derived_wiring"`
}

type WorkflowGraphDraft struct {
	NodeGroups       []WorkflowGraphDraftNodeGroup       `json:"node_groups,omitempty"`
	Nodes            []WorkflowGraphDraftNode            `json:"nodes"`
	TransitionGroups []WorkflowGraphDraftTransitionGroup `json:"transition_groups"`
	Edges            []WorkflowGraphDraftEdge            `json:"edges"`
}

type WorkflowGraphDraftNodeGroup struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
}

type WorkflowGraphDraftNode struct {
	ID                 string                      `json:"id"`
	Key                string                      `json:"key"`
	Kind               string                      `json:"kind"`
	DisplayName        string                      `json:"display_name"`
	GroupID            *string                     `json:"group_id"`
	GroupKey           string                      `json:"group_key,omitempty"`
	SubagentRole       string                      `json:"subagent_role,omitempty"`
	CompletionMode     string                      `json:"completion_mode,omitempty"`
	ScriptPath         *string                     `json:"script_path,omitempty"`
	JoinInputProviders []WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
}

type WorkflowGraphDraftTransitionGroup struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	TransitionID string `json:"transition_id"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description,omitempty"`
}

type WorkflowGraphDraftEdge struct {
	ID                string                `json:"id"`
	TransitionGroupID string                `json:"transition_group_id"`
	Key               string                `json:"key"`
	TargetNodeID      string                `json:"target_node_id"`
	AssigneeSelection string                `json:"assignee_selection"`
	ThinkingSelection string                `json:"thinking_selection"`
	RequiresApproval  bool                  `json:"requires_approval"`
	ContextMode       string                `json:"context_mode"`
	ContextSource     WorkflowContextSource `json:"context_source"`
	PromptTemplate    string                `json:"prompt_template,omitempty"`
	Parameters        []WorkflowParameter   `json:"parameters,omitempty"`
}

func WorkflowGraphDraftFromDefinition(definition WorkflowDefinition) WorkflowGraphDraft {
	graph := WorkflowGraphDraft{
		NodeGroups:       make([]WorkflowGraphDraftNodeGroup, 0, len(definition.NodeGroups)),
		Nodes:            make([]WorkflowGraphDraftNode, 0, len(definition.Nodes)),
		TransitionGroups: make([]WorkflowGraphDraftTransitionGroup, 0, len(definition.TransitionGroups)),
		Edges:            make([]WorkflowGraphDraftEdge, 0, len(definition.Edges)),
	}
	for _, group := range definition.NodeGroups {
		graph.NodeGroups = append(graph.NodeGroups, WorkflowGraphDraftNodeGroup{
			ID: group.GroupID, Key: group.GroupKey, DisplayName: group.DisplayName,
		})
	}
	for _, node := range definition.Nodes {
		graph.Nodes = append(graph.Nodes, WorkflowGraphDraftNode{
			ID: node.ID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName,
			GroupID: textutil.Pointer(node.GroupID), GroupKey: node.GroupKey,
			SubagentRole: node.SubagentRole, CompletionMode: node.CompletionMode,
			ScriptPath:         textutil.Pointer(node.ScriptPath),
			JoinInputProviders: slices.Clone(node.JoinInputProviders),
		})
	}
	for _, group := range definition.TransitionGroups {
		graph.TransitionGroups = append(graph.TransitionGroups, WorkflowGraphDraftTransitionGroup{
			ID: group.ID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID,
			DisplayName: group.DisplayName, Description: group.Description,
		})
	}
	for _, edge := range definition.Edges {
		graph.Edges = append(graph.Edges, WorkflowGraphDraftEdge{
			ID: edge.ID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key,
			TargetNodeID: edge.TargetNodeID, AssigneeSelection: edge.AssigneeSelection,
			ThinkingSelection: edge.ThinkingSelection, RequiresApproval: edge.RequiresApproval,
			ContextMode: edge.ContextMode, ContextSource: edge.ContextSource,
			PromptTemplate: edge.PromptTemplate, Parameters: slices.Clone(edge.Parameters),
		})
	}
	return graph
}

type WorkflowGraphValidateDraftRequest struct {
	WorkflowID runtimeids.WorkflowID    `json:"workflow_id"`
	Metadata   *WorkflowGraphMetadata   `json:"metadata,omitempty"`
	Graph      WorkflowGraphDraft       `json:"graph"`
	Modes      []WorkflowValidationMode `json:"modes"`
}

type WorkflowGraphValidateDraftResponse struct {
	Results       map[WorkflowValidationMode]WorkflowValidateResponse `json:"results"`
	DerivedWiring WorkflowDerivedWiring                               `json:"derived_wiring"`
}

// WorkflowGraphDeriveWiringRequest asks only for the derived wiring of a draft
// graph, skipping the expensive validation modes, so editor inspectors can keep
// wiring suggestions fresh during the dirty period without paying for full
// validation on every keystroke.
type WorkflowGraphDeriveWiringRequest struct {
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
	Graph      WorkflowGraphDraft    `json:"graph"`
}

type WorkflowGraphDeriveWiringResponse struct {
	DerivedWiring WorkflowDerivedWiring `json:"derived_wiring"`
}

type WorkflowGraphSavePreviewRequest struct {
	WorkflowID      runtimeids.WorkflowID  `json:"workflow_id"`
	ExpectedVersion int64                  `json:"expected_version"`
	Metadata        *WorkflowGraphMetadata `json:"metadata,omitempty"`
	Graph           WorkflowGraphDraft     `json:"graph"`
}

type WorkflowGraphMetadata struct {
	Name                  string                                `json:"name"`
	Description           string                                `json:"description"`
	ExecutionTargetPolicy *WorkflowExecutionTargetConfiguration `json:"execution_target_policy,omitempty"`
}

type WorkflowGraphSaveConfirmation struct {
	ExpectedRemovedNodeGroupCount       int64 `json:"expected_removed_node_group_count"`
	ExpectedRemovedNodeCount            int64 `json:"expected_removed_node_count"`
	ExpectedRemovedTransitionGroupCount int64 `json:"expected_removed_transition_group_count"`
	ExpectedRemovedEdgeCount            int64 `json:"expected_removed_edge_count"`
	ExpectedNodeTaskReferenceCount      int64 `json:"expected_node_task_reference_count"`
	ExpectedEdgeTaskReferenceCount      int64 `json:"expected_edge_task_reference_count"`
}

type WorkflowGraphSaveRequest struct {
	WorkflowID      runtimeids.WorkflowID          `json:"workflow_id"`
	ExpectedVersion int64                          `json:"expected_version"`
	Metadata        *WorkflowGraphMetadata         `json:"metadata,omitempty"`
	Graph           WorkflowGraphDraft             `json:"graph"`
	Confirmation    *WorkflowGraphSaveConfirmation `json:"confirmation,omitempty"`
}

type WorkflowGraphSavePreviewResponse struct {
	CurrentVersion       int64                                               `json:"current_version"`
	Changed              bool                                                `json:"changed"`
	ValidationResults    map[WorkflowValidationMode]WorkflowValidateResponse `json:"validation_results"`
	Impact               WorkflowGraphSaveImpact                             `json:"impact"`
	Blockers             []WorkflowGraphSaveBlocker                          `json:"blockers,omitempty"`
	CanSave              bool                                                `json:"can_save"`
	ConfirmationRequired bool                                                `json:"confirmation_required"`
}

type WorkflowGraphSaveResponse struct {
	Saved                bool                                                `json:"saved"`
	Changed              bool                                                `json:"changed"`
	Definition           *WorkflowDefinition                                 `json:"definition,omitempty"`
	CurrentVersion       int64                                               `json:"current_version"`
	ValidationResults    map[WorkflowValidationMode]WorkflowValidateResponse `json:"validation_results"`
	Impact               WorkflowGraphSaveImpact                             `json:"impact"`
	Blockers             []WorkflowGraphSaveBlocker                          `json:"blockers,omitempty"`
	CanSave              bool                                                `json:"can_save"`
	ConfirmationRequired bool                                                `json:"confirmation_required"`
}

type WorkflowGraphSaveImpact struct {
	RemovedNodeGroupCount             int64                          `json:"removed_node_group_count"`
	RemovedNodeCount                  int64                          `json:"removed_node_count"`
	RemovedTransitionGroupCount       int64                          `json:"removed_transition_group_count"`
	RemovedEdgeCount                  int64                          `json:"removed_edge_count"`
	RemovedEntities                   []WorkflowGraphEntityReference `json:"removed_entities"`
	NodeTaskReferenceCount            int64                          `json:"node_task_reference_count"`
	EdgeTaskReferenceCount            int64                          `json:"edge_task_reference_count"`
	ActiveCurrentNodeCount            int64                          `json:"active_current_node_count"`
	PendingApprovalCount              int64                          `json:"pending_approval_count"`
	StartNodeChangeCount              int64                          `json:"start_node_change_count"`
	LastTerminalChangeCount           int64                          `json:"last_terminal_change_count"`
	TaskReferencedNodeKindChangeCount int64                          `json:"task_referenced_node_kind_change_count"`
}

const (
	WorkflowGraphEntityTypeEdge            = workflowcontract.WorkflowGraphEntityTypeEdge
	WorkflowGraphEntityTypeNode            = workflowcontract.WorkflowGraphEntityTypeNode
	WorkflowGraphEntityTypeNodeGroup       = workflowcontract.WorkflowGraphEntityTypeNodeGroup
	WorkflowGraphEntityTypeTransitionGroup = workflowcontract.WorkflowGraphEntityTypeTransitionGroup
)

type WorkflowGraphEntityType = workflowcontract.WorkflowGraphEntityType
type WorkflowGraphEntityReference = workflowcontract.WorkflowGraphEntityReference

type WorkflowGraphSaveBlocker struct {
	Code             string                         `json:"code"`
	Message          string                         `json:"message"`
	Count            int64                          `json:"count"`
	AffectedEntities []WorkflowGraphEntityReference `json:"affected_entities"`
}

type WorkflowCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type WorkflowCreateResponse struct {
	Workflow WorkflowRecord `json:"workflow"`
}

type WorkflowCreateAndLinkProjectRequest struct {
	Name          string                         `json:"name"`
	Description   string                         `json:"description,omitempty"`
	ProjectID     string                         `json:"project_id"`
	DefaultPolicy WorkflowProjectLinkDefaultMode `json:"default_policy,omitempty"`
}

type WorkflowCreateAndLinkProjectResponse struct {
	Workflow WorkflowRecord      `json:"workflow"`
	Link     ProjectWorkflowLink `json:"link"`
}

type WorkflowUpdateRequest struct {
	WorkflowID  runtimeids.WorkflowID `json:"workflow_id"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
}

type WorkflowListRequest struct {
	Offset     *int                   `json:"offset,omitempty"`
	Limit      *int                   `json:"limit,omitempty"`
	Query      string                 `json:"query,omitempty"`
	ProjectID  *string                `json:"project_id,omitempty"`
	WorkflowID *runtimeids.WorkflowID `json:"workflow_id,omitempty"`
}

type WorkflowListResponse struct {
	Workflows  []WorkflowRecord `json:"workflows"`
	ProjectID  *string          `json:"project_id,omitempty"`
	NextOffset *int             `json:"next_offset,omitempty"`
}

type WorkflowGetRequest struct {
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
}

type WorkflowGetResponse struct {
	Definition WorkflowDefinition `json:"definition"`
}

type WorkflowLinkProjectRequest struct {
	ProjectID     string                         `json:"project_id"`
	WorkflowID    runtimeids.WorkflowID          `json:"workflow_id"`
	DefaultPolicy WorkflowProjectLinkDefaultMode `json:"default_policy,omitempty"`
}

type WorkflowLinkProjectResponse struct {
	Link ProjectWorkflowLink `json:"link"`
}

type WorkflowListProjectLinksRequest struct {
	ProjectID string `json:"project_id"`
}

type WorkflowListProjectLinksResponse struct {
	Links []ProjectWorkflowLink `json:"links"`
}

type WorkflowSetDefaultProjectLinkRequest struct {
	ProjectID  string                `json:"project_id"`
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
}

type WorkflowSetDefaultProjectLinkResponse struct {
	Link ProjectWorkflowLink `json:"link"`
}

type ProjectWorkflowLink struct {
	ID         string                `json:"id"`
	ProjectID  string                `json:"project_id"`
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
	Default    bool                  `json:"default"`
}

type WorkflowUnlinkProjectRequest struct {
	LinkID                   string `json:"link_id"`
	ReplacementDefaultLinkID string `json:"replacement_default_link_id,omitempty"`
}

type WorkflowUnlinkProjectResponse struct {
	LinkID   string                         `json:"link_id"`
	Unlinked bool                           `json:"unlinked"`
	Blockers []WorkflowUnlinkProjectBlocker `json:"blockers,omitempty"`
}

type WorkflowUnlinkProjectBlocker struct {
	Code    string                        `json:"code"`
	Message string                        `json:"message"`
	Count   int                           `json:"count,omitempty"`
	Tasks   []WorkflowUnlinkTaskReference `json:"tasks,omitempty"`
}

type WorkflowUnlinkTaskReference struct {
	TaskID  string `json:"task_id"`
	ShortID string `json:"short_id"`
	Title   string `json:"title,omitempty"`
}

type WorkflowDeletePreviewRequest struct {
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
}

type WorkflowDeletePreviewResponse struct {
	Impact WorkflowDeleteImpact `json:"impact"`
}

type WorkflowDeleteRequest struct {
	WorkflowID           runtimeids.WorkflowID `json:"workflow_id"`
	Confirmed            bool                  `json:"confirmed"`
	ExpectedVersion      int64                 `json:"expected_version"`
	ExpectedProjectCount int64                 `json:"expected_project_count"`
	ExpectedLinkCount    int64                 `json:"expected_link_count"`
	ExpectedTaskCount    int64                 `json:"expected_task_count"`
	CleanupArtifacts     bool                  `json:"cleanup_artifacts,omitempty"`
}

type WorkflowDeleteResponse struct {
	Deleted  bool                    `json:"deleted"`
	Impact   WorkflowDeleteImpact    `json:"impact"`
	Blockers []WorkflowDeleteBlocker `json:"blockers,omitempty"`
}

type WorkflowDeleteImpact struct {
	WorkflowID                     runtimeids.WorkflowID `json:"workflow_id"`
	Version                        int64                 `json:"version"`
	ProjectCount                   int64                 `json:"project_count"`
	LinkCount                      int64                 `json:"link_count"`
	DefaultReplacementProjectCount int64                 `json:"default_replacement_project_count"`
	TaskCount                      int64                 `json:"task_count"`
	CurrentNodeCount               int64                 `json:"current_node_count"`
	PendingApprovalCount           int64                 `json:"pending_approval_count"`
	BlockedTaskCount               int64                 `json:"blocked_task_count"`
}

type WorkflowDeleteBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

type WorkflowValidateRequest struct {
	WorkflowID runtimeids.WorkflowID  `json:"workflow_id"`
	Mode       WorkflowValidationMode `json:"mode"`
}

type WorkflowScriptPathValidateRequest struct {
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
	NodeID     string                `json:"node_id"`
	ScriptPath string                `json:"script_path"`
}

type WorkflowValidateResponse struct {
	Valid  bool                      `json:"valid"`
	Errors []WorkflowValidationError `json:"errors"`
}

type WorkflowValidationError struct {
	Code              string                          `json:"code"`
	Message           string                          `json:"message"`
	WorkflowID        *runtimeids.WorkflowID          `json:"workflow_id,omitempty"`
	NodeID            *string                         `json:"node_id"`
	TransitionGroupID *string                         `json:"transition_group_id"`
	EdgeID            *string                         `json:"edge_id"`
	Details           *WorkflowValidationErrorDetails `json:"details,omitempty"`
	RelatedIDs        []string                        `json:"related_ids,omitempty"`
	BlocksContext     bool                            `json:"blocks_context"`
}

type WorkflowValidationErrorDetails struct {
	FieldName      string  `json:"field_name,omitempty"`
	InputName      string  `json:"input_name,omitempty"`
	Placeholder    string  `json:"placeholder,omitempty"`
	ProviderEdgeID *string `json:"provider_edge_id"`
	Role           *string `json:"role,omitempty"`
	RequiredTool   *string `json:"required_tool,omitempty"`
}

type WorkflowTaskCreateRequest struct {
	ProjectID         string                               `json:"project_id"`
	WorkflowID        *runtimeids.WorkflowID               `json:"workflow_id,omitempty"`
	Title             string                               `json:"title"`
	Body              string                               `json:"body,omitempty"`
	SourceURL         string                               `json:"source_url,omitempty"`
	SourceWorkspaceID string                               `json:"source_workspace_id,omitempty"`
	LabelIDs          []string                             `json:"label_ids"`
	DependencyIntents []WorkflowTaskDependencyCreateIntent `json:"dependency_intents,omitempty"`
}

type WorkflowTaskCreateResponse struct {
	Task WorkflowTaskSummary `json:"task"`
}

type WorkflowTaskCreateSelectionReason string

const (
	WorkflowTaskCreateSelectionReasonNoLinkedWorkflows       WorkflowTaskCreateSelectionReason = "no_linked_workflows"
	WorkflowTaskCreateSelectionReasonWorkflowNotLinked       WorkflowTaskCreateSelectionReason = "workflow_not_linked"
	WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault WorkflowTaskCreateSelectionReason = "ambiguous_without_default"
)

type WorkflowTaskCreateSelectionError struct {
	Reason     WorkflowTaskCreateSelectionReason
	ProjectID  string
	WorkflowID *runtimeids.WorkflowID
}

func (e *WorkflowTaskCreateSelectionError) Error() string {
	if e == nil {
		return "workflow task create selection error"
	}
	return "workflow task create selection error: " + string(e.Reason)
}

func (e *WorkflowTaskCreateSelectionError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskCreateSelection
}

func (e *WorkflowTaskCreateSelectionError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type       string                            `json:"type"`
		Reason     WorkflowTaskCreateSelectionReason `json:"reason"`
		ProjectID  string                            `json:"project_id"`
		WorkflowID *runtimeids.WorkflowID            `json:"workflow_id,omitempty"`
	}{
		Type:       "workflow_task_create_selection_error",
		Reason:     e.Reason,
		ProjectID:  e.ProjectID,
		WorkflowID: e.WorkflowID,
	})
}

func DecodeWorkflowTaskCreateSelectionError(data json.RawMessage, message string) error {
	var envelope struct {
		Type       string                            `json:"type"`
		Reason     WorkflowTaskCreateSelectionReason `json:"reason"`
		ProjectID  string                            `json:"project_id"`
		WorkflowID *runtimeids.WorkflowID            `json:"workflow_id,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_task_create_selection_error" ||
		strings.TrimSpace(envelope.ProjectID) == "" ||
		strings.TrimSpace(envelope.ProjectID) != envelope.ProjectID ||
		!validWorkflowTaskCreateSelectionError(envelope.Reason, envelope.WorkflowID) {
		return errors.New(strings.TrimSpace(message))
	}
	return &WorkflowTaskCreateSelectionError{
		Reason:     envelope.Reason,
		ProjectID:  envelope.ProjectID,
		WorkflowID: envelope.WorkflowID,
	}
}

func validWorkflowTaskCreateSelectionError(reason WorkflowTaskCreateSelectionReason, workflowID *runtimeids.WorkflowID) bool {
	switch reason {
	case WorkflowTaskCreateSelectionReasonNoLinkedWorkflows,
		WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault:
		return workflowID == nil
	case WorkflowTaskCreateSelectionReasonWorkflowNotLinked:
		return workflowID != nil && !workflowID.IsZero()
	default:
		return false
	}
}

type WorkflowTaskCreateConflictReason string

const (
	WorkflowTaskCreateConflictReasonSerialization WorkflowTaskCreateConflictReason = "serialization_conflict"
)

type WorkflowTaskCreateConflictError struct {
	Reason WorkflowTaskCreateConflictReason
}

func (e *WorkflowTaskCreateConflictError) Error() string {
	if e == nil {
		return "workflow task create conflict"
	}
	return "workflow task create conflict: " + string(e.Reason)
}

func (e *WorkflowTaskCreateConflictError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskCreateConflict
}

func (e *WorkflowTaskCreateConflictError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type   string                           `json:"type"`
		Reason WorkflowTaskCreateConflictReason `json:"reason"`
	}{
		Type:   "workflow_task_create_conflict_error",
		Reason: e.Reason,
	})
}

func DecodeWorkflowTaskCreateConflictError(data json.RawMessage, message string) error {
	var envelope struct {
		Type   string                           `json:"type"`
		Reason WorkflowTaskCreateConflictReason `json:"reason"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_task_create_conflict_error" ||
		envelope.Reason != WorkflowTaskCreateConflictReasonSerialization {
		return errors.New(strings.TrimSpace(message))
	}
	return &WorkflowTaskCreateConflictError{Reason: envelope.Reason}
}

type WorkflowTaskMutationSelfTargetError struct {
	TaskID string
}

func (e *WorkflowTaskMutationSelfTargetError) Error() string {
	if e == nil {
		return "workflow task mutation self-target denied"
	}
	return fmt.Sprintf("workflow task mutation self-target denied for task %q", e.TaskID)
}

func (e *WorkflowTaskMutationSelfTargetError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskMutationSelfTarget
}

func (e *WorkflowTaskMutationSelfTargetError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type   string `json:"type"`
		TaskID string `json:"task_id"`
	}{
		Type:   "workflow_task_mutation_self_target_error",
		TaskID: e.TaskID,
	})
}

func DecodeWorkflowTaskMutationSelfTargetError(data json.RawMessage, message string) error {
	var envelope struct {
		Type   string `json:"type"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_task_mutation_self_target_error" ||
		strings.TrimSpace(envelope.TaskID) == "" {
		return errors.New(strings.TrimSpace(message))
	}
	return &WorkflowTaskMutationSelfTargetError{TaskID: envelope.TaskID}
}

type WorkflowTaskStartConflictReason string

const WorkflowTaskStartConflictAlreadyStarted WorkflowTaskStartConflictReason = "already_started"

type WorkflowTaskStartConflictError struct {
	TaskID string                          `json:"task_id"`
	Reason WorkflowTaskStartConflictReason `json:"reason"`
}

func (e *WorkflowTaskStartConflictError) Error() string {
	return "workflow task start conflict"
}

func (e *WorkflowTaskStartConflictError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskStartConflict
}

func (e *WorkflowTaskStartConflictError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type   string                          `json:"type"`
		TaskID string                          `json:"task_id"`
		Reason WorkflowTaskStartConflictReason `json:"reason"`
	}{
		Type:   "workflow_task_start_conflict",
		TaskID: e.TaskID,
		Reason: e.Reason,
	})
}

func (e *WorkflowTaskStartConflictError) Validate() error {
	if e == nil {
		return errors.New("workflow task start conflict is required")
	}
	if strings.TrimSpace(e.TaskID) == "" {
		return errors.New("workflow task start conflict task_id is required")
	}
	if e.Reason != WorkflowTaskStartConflictAlreadyStarted {
		return errors.New("workflow task start conflict reason is invalid")
	}
	return nil
}

func DecodeWorkflowTaskStartConflictError(data json.RawMessage, message string) error {
	fallbackMessage := strings.TrimSpace(message)
	if fallbackMessage == "" {
		fallbackMessage = "workflow task start conflict"
	}
	fallback := errors.New(fallbackMessage)
	var payload struct {
		Type   string                          `json:"type"`
		TaskID string                          `json:"task_id"`
		Reason WorkflowTaskStartConflictReason `json:"reason"`
	}
	if err := protocol.DecodeStrictJSON(data, &payload); err != nil ||
		payload.Type != "workflow_task_start_conflict" {
		return fallback
	}
	result := &WorkflowTaskStartConflictError{TaskID: payload.TaskID, Reason: payload.Reason}
	if err := result.Validate(); err != nil {
		return fallback
	}
	return result
}

type WorkflowTaskUpdateRequest struct {
	TaskID            string  `json:"task_id"`
	Title             *string `json:"title,omitempty"`
	Body              *string `json:"body,omitempty"`
	SourceWorkspaceID string  `json:"source_workspace_id,omitempty"`
}

type WorkflowTaskUpdateResponse struct {
	Task WorkflowTaskSummary `json:"task"`
}

type WorkflowTaskStartRequest struct {
	TaskID                     string                            `json:"task_id"`
	InvokingSessionID          *runtimeids.SessionID             `json:"invoking_session_id,omitempty"`
	SetupOperationID           WorktreeSetupOperationID          `json:"setup_operation_id"`
	ExecutionTarget            *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
	BranchName                 *string                           `json:"branch_name,omitempty"`
	ProceedDespiteDependencies bool                              `json:"proceed_despite_dependencies,omitempty"`
}

type WorkflowTaskStartResponse struct {
	Outcome                    WorkflowTaskActionOutcome                    `json:"outcome,omitempty"`
	Applied                    *WorkflowTaskStartApplied                    `json:"applied,omitempty"`
	SelectionRequired          *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
	UnsatisfiedDependencyCount *int                                         `json:"unsatisfied_dependency_count,omitempty"`
}

type WorkflowTaskStartApplied struct {
	CurrentNodes []WorkflowTaskCurrentNode `json:"current_nodes"`
}

type WorkflowTaskCurrentNode struct {
	NodeID              string  `json:"node_id"`
	TransitionBranchKey *string `json:"transition_branch_key,omitempty"`
	SessionID           *string `json:"session_id,omitempty"`
	EffectiveAssignee   *string `json:"effective_assignee,omitempty"`
	EffectiveThinking   *string `json:"effective_thinking,omitempty"`
}

type WorkflowTaskResumeRequest struct {
	TaskID            string                            `json:"task_id"`
	InvokingSessionID *runtimeids.SessionID             `json:"invoking_session_id,omitempty"`
	SetupOperationID  WorktreeSetupOperationID          `json:"setup_operation_id"`
	ExecutionTarget   *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
	BranchName        *string                           `json:"branch_name,omitempty"`
}

type WorkflowTaskResumeResponse struct {
	Outcome           WorkflowExecutionTargetActionOutcome         `json:"outcome,omitempty"`
	Applied           *WorkflowTaskResumeApplied                   `json:"applied,omitempty"`
	NoOp              *WorkflowTaskResumeNoOp                      `json:"no_op,omitempty"`
	SelectionRequired *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
}

type WorkflowTaskResumeApplied struct {
	CurrentNodes []WorkflowTaskCurrentNode `json:"current_nodes"`
}

type WorkflowTaskResumeNoOp = WorkflowTaskResumeApplied

type WorkflowTaskApproveRequest struct {
	ApprovalID        string                `json:"approval_id"`
	InvokingSessionID *runtimeids.SessionID `json:"invoking_session_id,omitempty"`
}

type WorkflowTaskApproveResponse struct {
	Outcome           WorkflowExecutionTargetActionOutcome         `json:"outcome,omitempty"`
	Applied           *WorkflowTaskApproveApplied                  `json:"applied,omitempty"`
	SelectionRequired *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
}

type WorkflowTaskApproveApplied struct {
	TaskID       string                    `json:"task_id"`
	CurrentNodes []WorkflowTaskCurrentNode `json:"current_nodes"`
}

type WorkflowTaskMoveRequest struct {
	TaskID                     string                            `json:"task_id"`
	InvokingSessionID          *runtimeids.SessionID             `json:"invoking_session_id,omitempty"`
	TargetNodeID               string                            `json:"target_node_id"`
	TransitionKey              *string                           `json:"transition_key,omitempty"`
	Values                     map[string]map[string]string      `json:"values,omitempty"`
	Commentary                 string                            `json:"commentary,omitempty"`
	ExecutionTarget            *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
	BranchName                 *string                           `json:"branch_name,omitempty"`
	ProceedDespiteDependencies bool                              `json:"proceed_despite_dependencies,omitempty"`
}

type WorkflowTaskMoveResponse struct {
	Outcome                    WorkflowExecutionTargetActionOutcome         `json:"outcome,omitempty"`
	NoOp                       *WorkflowTaskMoveNoOp                        `json:"no_op,omitempty"`
	Applied                    *WorkflowTaskMoveApplied                     `json:"applied,omitempty"`
	SelectionRequired          *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
	UnsatisfiedDependencyCount *int                                         `json:"unsatisfied_dependency_count,omitempty"`
}

type WorkflowTaskMoveNoOp struct {
	CurrentNodes             []WorkflowTaskCurrentNode `json:"current_nodes"`
	RetainedPreviousWorktree *RetainedPreviousWorktree `json:"retained_previous_worktree"`
}

type WorkflowTaskMoveApplied struct {
	CurrentNodes             []WorkflowTaskCurrentNode `json:"current_nodes"`
	RetainedPreviousWorktree *RetainedPreviousWorktree `json:"retained_previous_worktree"`
}

const (
	WorkflowTaskCompleteActorAgent = "agent"
	WorkflowTaskCompleteActorUser  = "user"
)

var ErrWorkflowTaskCompleteTargetNotFound = errors.New("workflow task completion target not found")
var ErrWorkflowTaskCompleteSelectorAmbiguous = errors.New("workflow task completion selector is ambiguous")

type WorkflowTaskCompleteSelectorAmbiguousError struct {
	Message string
}

func (e WorkflowTaskCompleteSelectorAmbiguousError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		return ErrWorkflowTaskCompleteSelectorAmbiguous.Error()
	}
	return message
}

func (e WorkflowTaskCompleteSelectorAmbiguousError) Is(target error) bool {
	return target == ErrWorkflowTaskCompleteSelectorAmbiguous
}

type WorkflowTaskCompleteRequest struct {
	SessionID      string            `json:"session_id,omitempty"`
	TaskID         string            `json:"task_id,omitempty"`
	TransitionID   string            `json:"transition_id,omitempty"`
	OutputValues   map[string]string `json:"output_values,omitempty"`
	Commentary     string            `json:"commentary,omitempty"`
	ActorKind      string            `json:"actor_kind"`
	AgentSessionID string            `json:"agent_session_id,omitempty"`
	Force          bool              `json:"force,omitempty"`
}

type WorkflowTaskCompleteResponse struct {
	TaskID            string                        `json:"task_id"`
	CurrentNodes      []WorkflowTaskCurrentNode     `json:"current_nodes"`
	PendingApprovalID *string                       `json:"pending_approval_id,omitempty"`
	Handoff           WorkflowTaskCompletionHandoff `json:"handoff"`
}

type WorkflowTaskCompletionHandoff struct {
	SourceNodeDisplayName  string `json:"source_node_display_name"`
	DestinationDisplayName string `json:"destination_display_name"`
}

type WorkflowTaskDeleteRequest struct {
	TaskID string `json:"task_id"`
}

type WorkflowTaskInterruptRequest struct {
	TaskID            string                `json:"task_id"`
	InvokingSessionID *runtimeids.SessionID `json:"invoking_session_id,omitempty"`
	SessionID         string                `json:"session_id,omitempty"`
	Reason            string                `json:"reason,omitempty"`
}

type WorkflowTaskInterruptResponse struct {
}

type WorkflowAttentionListRequest struct {
	PageSize  int    `json:"page_size,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

type WorkflowAttentionListResponse struct {
	Items             []WorkflowAttentionItem `json:"items"`
	NextPageToken     string                  `json:"next_page_token,omitempty"`
	GeneratedAtUnixMs int64                   `json:"generated_at_unix_ms"`
}

type WorkflowTaskAttentionListRequest struct {
	TaskID string `json:"task_id"`
}

type WorkflowTaskAttentionListResponse struct {
	Items             []WorkflowAttentionItem `json:"items"`
	GeneratedAtUnixMs int64                   `json:"generated_at_unix_ms"`
}

type WorkflowAttentionItem struct {
	ID               string                             `json:"id"`
	Kind             string                             `json:"kind"`
	ProjectID        string                             `json:"project_id"`
	WorkflowID       runtimeids.WorkflowID              `json:"workflow_id"`
	TaskID           string                             `json:"task_id"`
	TaskShortID      string                             `json:"task_short_id"`
	TaskTitle        string                             `json:"task_title"`
	Message          *string                            `json:"message,omitempty"`
	ApprovalID       *string                            `json:"approval_id,omitempty"`
	CurrentNode      *WorkflowTaskCurrentNode           `json:"current_node,omitempty"`
	SessionID        *string                            `json:"session_id,omitempty"`
	SessionName      *string                            `json:"session_name"`
	DetailJSON       *string                            `json:"detail_json,omitempty"`
	Question         *WorkflowAttentionQuestionPrompt   `json:"question,omitempty"`
	ApprovalSnapshot *WorkflowAttentionApprovalSnapshot `json:"approval_snapshot,omitempty"`
	OccurredAtUnixMs int64                              `json:"occurred_at_unix_ms"`
}

type WorkflowAttentionApprovalSnapshot struct {
	SourceNodeDisplayName string                            `json:"source_node_display_name"`
	Targets               []WorkflowAttentionApprovalTarget `json:"targets"`
	Commentary            string                            `json:"commentary,omitempty"`
	OutputValues          map[string]string                 `json:"output_values"`
	WorkflowRevisionSeen  int64                             `json:"workflow_revision_seen"`
}

type WorkflowAttentionApprovalTarget struct {
	DisplayName string `json:"display_name"`
}

type WorkflowAttentionQuestionKind string

const (
	WorkflowAttentionQuestionKindOrdinary WorkflowAttentionQuestionKind = "ordinary"
	WorkflowAttentionQuestionKindApproval WorkflowAttentionQuestionKind = "approval"
)

type WorkflowAttentionQuestionPrompt struct {
	SessionID              runtimeids.SessionID          `json:"session_id"`
	StepID                 runtimeids.StepID             `json:"step_id"`
	PromptID               clientui.PromptID             `json:"prompt_id"`
	Kind                   WorkflowAttentionQuestionKind `json:"kind"`
	Suggestions            []string                      `json:"suggestions,omitempty"`
	RecommendedOptionIndex *int                          `json:"recommended_option_index,omitempty"`
	ApprovalDecisions      []clientui.ApprovalDecision   `json:"approval_decisions,omitempty"`
}

type WorkflowTaskCommentAddRequest struct {
	TaskID   string `json:"task_id"`
	Body     string `json:"body"`
	Author   string `json:"author"`
	AuthorID string `json:"author_id,omitempty"`
}

type WorkflowTaskCommentAddResponse struct {
	Comment WorkflowTaskComment `json:"comment"`
}

type WorkflowTaskCommentListResponse struct {
	WorkflowOffsetPage[WorkflowTaskComment]
	TotalCount int64 `json:"total_count"`
}

type WorkflowTaskCommentReplaceRequest struct {
	CommentID string `json:"comment_id"`
	Body      string `json:"body"`
}

type WorkflowTaskCommentDeleteRequest struct {
	CommentID string `json:"comment_id"`
}

type WorkflowBoardRequest struct {
	ProjectID        string                  `json:"project_id"`
	WorkflowID       *runtimeids.WorkflowID  `json:"workflow_id,omitempty"`
	LabelFilter      WorkflowTaskLabelFilter `json:"label_filter"`
	DependencyFilter *bool                   `json:"dependency_filter,omitempty"`
}

type WorkflowTaskStatusKind string

const (
	WorkflowTaskStatusKindDone            WorkflowTaskStatusKind = "done"
	WorkflowTaskStatusKindWaitingQuestion WorkflowTaskStatusKind = "waiting_question"
	WorkflowTaskStatusKindWaitingApproval WorkflowTaskStatusKind = "waiting_approval"
	WorkflowTaskStatusKindInterrupted     WorkflowTaskStatusKind = "interrupted"
	WorkflowTaskStatusKindRunning         WorkflowTaskStatusKind = "running"
	WorkflowTaskStatusKindQueued          WorkflowTaskStatusKind = "queued"
	WorkflowTaskStatusKindBacklog         WorkflowTaskStatusKind = "backlog"
	WorkflowTaskStatusKindActive          WorkflowTaskStatusKind = "active"
)

type WorkflowTaskNativeState string

const (
	WorkflowTaskNativeStateTerminal        WorkflowTaskNativeState = "terminal"
	WorkflowTaskNativeStateWaitingAsk      WorkflowTaskNativeState = "waiting_ask"
	WorkflowTaskNativeStateWaitingApproval WorkflowTaskNativeState = "waiting_approval"
	WorkflowTaskNativeStateInterrupted     WorkflowTaskNativeState = "interrupted"
	WorkflowTaskNativeStateRunning         WorkflowTaskNativeState = "running"
	WorkflowTaskNativeStateQueued          WorkflowTaskNativeState = "queued"
	WorkflowTaskNativeStateActive          WorkflowTaskNativeState = "active"
)

// NativeState returns the runtime-native state represented by a public task status.
func (k WorkflowTaskStatusKind) NativeState() (WorkflowTaskNativeState, bool) {
	switch k {
	case WorkflowTaskStatusKindDone:
		return WorkflowTaskNativeStateTerminal, true
	case WorkflowTaskStatusKindWaitingQuestion:
		return WorkflowTaskNativeStateWaitingAsk, true
	case WorkflowTaskStatusKindWaitingApproval:
		return WorkflowTaskNativeStateWaitingApproval, true
	case WorkflowTaskStatusKindInterrupted:
		return WorkflowTaskNativeStateInterrupted, true
	case WorkflowTaskStatusKindRunning:
		return WorkflowTaskNativeStateRunning, true
	case WorkflowTaskStatusKindQueued:
		return WorkflowTaskNativeStateQueued, true
	case WorkflowTaskStatusKindBacklog, WorkflowTaskStatusKindActive:
		return WorkflowTaskNativeStateActive, true
	default:
		return "", false
	}
}

type WorkflowTaskAttentionKind string

const (
	WorkflowTaskAttentionKindQuestion    WorkflowTaskAttentionKind = "question"
	WorkflowTaskAttentionKindApproval    WorkflowTaskAttentionKind = "approval"
	WorkflowTaskAttentionKindInterrupted WorkflowTaskAttentionKind = "interrupted"
)

type WorkflowTaskListSortField string

const (
	WorkflowTaskListSortFieldCreated WorkflowTaskListSortField = "created"
	WorkflowTaskListSortFieldUpdated WorkflowTaskListSortField = "updated"
	WorkflowTaskListSortFieldStatus  WorkflowTaskListSortField = "status"
	WorkflowTaskListSortFieldColumn  WorkflowTaskListSortField = "column"
	WorkflowTaskListSortFieldTitle   WorkflowTaskListSortField = "title"
	WorkflowTaskListSortFieldLabels  WorkflowTaskListSortField = "labels"
	WorkflowTaskListSortFieldShortID WorkflowTaskListSortField = "short_id"
)

type WorkflowTaskListSortDirection string

const (
	WorkflowTaskListSortDirectionAsc  WorkflowTaskListSortDirection = "asc"
	WorkflowTaskListSortDirectionDesc WorkflowTaskListSortDirection = "desc"
)

type WorkflowTaskListSort struct {
	Field     WorkflowTaskListSortField     `json:"field"`
	Direction WorkflowTaskListSortDirection `json:"direction"`
}

type WorkflowTaskListRequest struct {
	ProjectID        *string                     `json:"project_id,omitempty"`
	WorkflowID       *runtimeids.WorkflowID      `json:"workflow_id,omitempty"`
	Group            *WorkflowProjectTaskGroup   `json:"group,omitempty"`
	ColumnKeys       []string                    `json:"column_keys,omitempty"`
	StatusKinds      []WorkflowTaskStatusKind    `json:"status_kinds,omitempty"`
	AttentionKinds   []WorkflowTaskAttentionKind `json:"attention_kinds,omitempty"`
	LabelFilter      WorkflowTaskLabelFilter     `json:"label_filter"`
	DependencyFilter *bool                       `json:"dependency_filter,omitempty"`
	Sort             []WorkflowTaskListSort      `json:"sort,omitempty"`
	Offset           *int                        `json:"offset,omitempty"`
	Limit            *int                        `json:"limit,omitempty"`
}

type WorkflowTaskListScope struct {
	ProjectID  string                 `json:"project_id"`
	WorkflowID *runtimeids.WorkflowID `json:"workflow_id,omitempty"`
}

type WorkflowTaskListMatchingWorkflowCardinality string

const (
	WorkflowTaskListMatchingWorkflowCardinalityNone     WorkflowTaskListMatchingWorkflowCardinality = "none"
	WorkflowTaskListMatchingWorkflowCardinalityOne      WorkflowTaskListMatchingWorkflowCardinality = "one"
	WorkflowTaskListMatchingWorkflowCardinalityMultiple WorkflowTaskListMatchingWorkflowCardinality = "multiple"
)

type WorkflowTaskListResponse struct {
	Scope                       WorkflowTaskListScope                       `json:"scope"`
	MatchingWorkflowCardinality WorkflowTaskListMatchingWorkflowCardinality `json:"matching_workflow_cardinality"`
	NextOffset                  *int                                        `json:"next_offset,omitempty"`
	GeneratedAtUnixMs           int64                                       `json:"generated_at_unix_ms"`
	Tasks                       []WorkflowTaskListItem                      `json:"tasks"`
}

type WorkflowProjectTaskGroupCountsRequest struct {
	ProjectID string `json:"project_id"`
}

type WorkflowProjectTaskGroup string

const (
	WorkflowProjectTaskGroupActive  WorkflowProjectTaskGroup = "active"
	WorkflowProjectTaskGroupBacklog WorkflowProjectTaskGroup = "backlog"
	WorkflowProjectTaskGroupDone    WorkflowProjectTaskGroup = "done"
)

type WorkflowProjectTaskGroupDefinition struct {
	Group       WorkflowProjectTaskGroup `json:"group"`
	StatusKinds []WorkflowTaskStatusKind `json:"status_kinds"`
}

func WorkflowProjectTaskGroupDefinitions() []WorkflowProjectTaskGroupDefinition {
	return []WorkflowProjectTaskGroupDefinition{
		{Group: WorkflowProjectTaskGroupActive, StatusKinds: []WorkflowTaskStatusKind{
			WorkflowTaskStatusKindWaitingQuestion,
			WorkflowTaskStatusKindWaitingApproval,
			WorkflowTaskStatusKindInterrupted,
			WorkflowTaskStatusKindRunning,
			WorkflowTaskStatusKindQueued,
			WorkflowTaskStatusKindActive,
		}},
		{Group: WorkflowProjectTaskGroupBacklog, StatusKinds: []WorkflowTaskStatusKind{WorkflowTaskStatusKindBacklog}},
		{Group: WorkflowProjectTaskGroupDone, StatusKinds: []WorkflowTaskStatusKind{WorkflowTaskStatusKindDone}},
	}
}

func (g WorkflowProjectTaskGroup) StatusKinds() []WorkflowTaskStatusKind {
	for _, definition := range WorkflowProjectTaskGroupDefinitions() {
		if definition.Group == g {
			return slices.Clone(definition.StatusKinds)
		}
	}
	return nil
}

type WorkflowProjectTaskGroupCounts struct {
	Active  int `json:"active"`
	Backlog int `json:"backlog"`
	Done    int `json:"done"`
}

type WorkflowProjectTaskGroupCountsResponse struct {
	ProjectID         string                               `json:"project_id"`
	Definitions       []WorkflowProjectTaskGroupDefinition `json:"definitions"`
	Counts            WorkflowProjectTaskGroupCounts       `json:"counts"`
	GeneratedAtUnixMs int64                                `json:"generated_at_unix_ms"`
}

type WorkflowTaskListItem struct {
	TaskID             string                          `json:"task_id"`
	ShortID            string                          `json:"short_id"`
	WorkflowID         runtimeids.WorkflowID           `json:"workflow_id"`
	WorkflowName       *string                         `json:"workflow_name,omitempty"`
	Title              string                          `json:"title"`
	CreatedAtUnixMs    int64                           `json:"created_at_unix_ms"`
	UpdatedAtUnixMs    int64                           `json:"updated_at_unix_ms"`
	ColumnKeys         *[]string                       `json:"column_keys,omitempty"`
	Status             WorkflowTaskStatus              `json:"status"`
	Labels             []WorkflowProjectLabel          `json:"labels"`
	DependencyProgress *WorkflowTaskDependencyProgress `json:"dependency_progress,omitempty"`
}

type WorkflowTaskListScopeErrorReason string

const (
	WorkflowTaskListScopeReasonNoLinkedWorkflows       WorkflowTaskListScopeErrorReason = "no_linked_workflows"
	WorkflowTaskListScopeReasonWorkflowNotLinked       WorkflowTaskListScopeErrorReason = "workflow_not_linked"
	WorkflowTaskListScopeReasonWorkflowRequiredColumns WorkflowTaskListScopeErrorReason = "workflow_required_for_columns"
)

type WorkflowTaskListScopeError struct {
	Reason     WorkflowTaskListScopeErrorReason
	ProjectID  *string
	WorkflowID *runtimeids.WorkflowID
}

func (e *WorkflowTaskListScopeError) Error() string {
	if e == nil {
		return "workflow task list scope error"
	}
	return "workflow task list scope error: " + string(e.Reason)
}

func (e *WorkflowTaskListScopeError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskListScope
}

func (e *WorkflowTaskListScopeError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type       string                           `json:"type"`
		Reason     WorkflowTaskListScopeErrorReason `json:"reason"`
		ProjectID  *string                          `json:"project_id,omitempty"`
		WorkflowID *runtimeids.WorkflowID           `json:"workflow_id,omitempty"`
	}{
		Type:       "workflow_task_list_scope_error",
		Reason:     e.Reason,
		ProjectID:  e.ProjectID,
		WorkflowID: e.WorkflowID,
	})
}

func DecodeWorkflowTaskListScopeError(data json.RawMessage, message string) error {
	var envelope struct {
		Type       string                           `json:"type"`
		Reason     WorkflowTaskListScopeErrorReason `json:"reason"`
		ProjectID  *string                          `json:"project_id,omitempty"`
		WorkflowID *runtimeids.WorkflowID           `json:"workflow_id,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_task_list_scope_error" {
		return errors.New(strings.TrimSpace(message))
	}
	scopeErr := &WorkflowTaskListScopeError{
		Reason:     envelope.Reason,
		ProjectID:  envelope.ProjectID,
		WorkflowID: envelope.WorkflowID,
	}
	if err := scopeErr.validate(); err != nil {
		return errors.New(strings.TrimSpace(message))
	}
	return scopeErr
}

func (e *WorkflowTaskListScopeError) validate() error {
	if e == nil || e.ProjectID == nil || strings.TrimSpace(*e.ProjectID) == "" || strings.TrimSpace(*e.ProjectID) != *e.ProjectID {
		return errors.New("workflow task list scope error requires an exact project id")
	}
	switch e.Reason {
	case WorkflowTaskListScopeReasonNoLinkedWorkflows, WorkflowTaskListScopeReasonWorkflowRequiredColumns:
		if e.WorkflowID != nil {
			return errors.New("workflow task list scope error reason forbids workflow id")
		}
	case WorkflowTaskListScopeReasonWorkflowNotLinked:
		if e.WorkflowID == nil {
			return errors.New("workflow-not-linked task list scope error requires workflow id")
		}
		if e.WorkflowID.IsZero() {
			return errors.New("workflow-not-linked task list scope error requires workflow id")
		}
	default:
		return errors.New("workflow task list scope error reason is invalid")
	}
	return nil
}

type WorkflowBoardResponse struct {
	Board WorkflowBoard `json:"board"`
}

type WorkflowBoardNodeCardsListRequest struct {
	ProjectID        string                  `json:"project_id"`
	WorkflowID       runtimeids.WorkflowID   `json:"workflow_id"`
	NodeID           string                  `json:"node_id"`
	LabelFilter      WorkflowTaskLabelFilter `json:"label_filter"`
	DependencyFilter *bool                   `json:"dependency_filter,omitempty"`
	PageSize         int                     `json:"page_size"`
	Sort             *WorkflowTaskListSort   `json:"sort,omitempty"`
	Offset           *int                    `json:"offset,omitempty"`
}

type WorkflowBoardNodeCardsListResponse struct {
	ProjectID         string                  `json:"project_id"`
	WorkflowID        runtimeids.WorkflowID   `json:"workflow_id"`
	NodeID            string                  `json:"node_id"`
	Cards             []WorkflowBoardTaskCard `json:"cards"`
	NextOffset        *int                    `json:"next_offset,omitempty"`
	GeneratedAtUnixMs int64                   `json:"generated_at_unix_ms"`
}

type WorkflowBoard struct {
	ProjectID         string                `json:"project_id"`
	Project           ProjectBoardProject   `json:"project"`
	SelectedWorkflow  *WorkflowPickerItem   `json:"selected_workflow,omitempty"`
	WorkflowPicker    []WorkflowPickerItem  `json:"workflows"`
	Groups            []WorkflowBoardGroup  `json:"groups"`
	Columns           []WorkflowBoardColumn `json:"columns"`
	GeneratedAtUnixMs int64                 `json:"generated_at_unix_ms"`
}

type ProjectBoardProject struct {
	ProjectKey             string `json:"project_key"`
	DisplayName            string `json:"display_name"`
	DefaultWorkspaceID     string `json:"default_workspace_id"`
	AttachedWorkspaceCount int    `json:"attached_workspace_count"`
}

type WorkflowPickerItem struct {
	WorkflowID           runtimeids.WorkflowID     `json:"workflow_id"`
	DisplayName          string                    `json:"display_name"`
	Description          string                    `json:"description"`
	Version              int64                     `json:"version"`
	IsProjectDefault     bool                      `json:"is_project_default"`
	ValidForTaskCreation bool                      `json:"valid_for_task_creation"`
	ValidationErrors     []WorkflowValidationError `json:"validation_errors,omitempty"`
}

type WorkflowBoardGroup struct {
	GroupID     string   `json:"group_id"`
	Key         string   `json:"key"`
	DisplayName string   `json:"display_name"`
	SortOrder   int      `json:"sort_order"`
	NodeIDs     []string `json:"node_ids"`
}

type WorkflowBoardColumn struct {
	Node      WorkflowBoardNodeSummary `json:"node"`
	GroupID   *string                  `json:"group_id"`
	SortOrder int                      `json:"sort_order"`
	IsBacklog bool                     `json:"is_backlog"`
	IsDone    bool                     `json:"is_done"`
	TaskCount int                      `json:"task_count"`
}

type WorkflowBoardNodeSummary struct {
	NodeID       string                `json:"node_id"`
	Key          string                `json:"key"`
	Kind         string                `json:"kind"`
	DisplayName  string                `json:"display_name"`
	AssigneeRole string                `json:"assignee_role,omitempty"`
	SortOrder    int                   `json:"sort_order"`
	OutputFields []WorkflowOutputField `json:"output_fields,omitempty"`
}

type WorkflowBoardTaskCard struct {
	TaskID             string                          `json:"task_id"`
	ShortID            string                          `json:"short_id"`
	Title              string                          `json:"title"`
	Preview            MarkdownPreview                 `json:"preview"`
	WorkflowID         runtimeids.WorkflowID           `json:"workflow_id"`
	ActiveNodeIDs      []string                        `json:"active_node_ids,omitempty"`
	SourceWorkspace    ProjectWorkspaceSummary         `json:"source_workspace"`
	Status             WorkflowTaskStatus              `json:"status"`
	Actions            WorkflowTaskActions             `json:"actions"`
	LabelIDs           []string                        `json:"label_ids"`
	DependencyProgress *WorkflowTaskDependencyProgress `json:"dependency_progress,omitempty"`
	UpdatedAtUnixMs    int64                           `json:"updated_at_unix_ms"`
}

type WorkflowTaskDependencyProgress struct {
	SatisfiedCount int `json:"satisfied_count"`
	TotalCount     int `json:"total_count"`
}

type MarkdownPreview struct {
	Markdown  string `json:"markdown"`
	Truncated bool   `json:"truncated"`
}

type WorkflowTaskStatus struct {
	Kind           WorkflowTaskStatusKind      `json:"kind"`
	NativeState    WorkflowTaskNativeState     `json:"native_state"`
	NodeIDs        []string                    `json:"node_ids,omitempty"`
	AttentionTypes []WorkflowTaskAttentionKind `json:"attention_types,omitempty"`
}

type WorkflowTaskActions struct {
	CanStart     bool `json:"can_start"`
	CanInterrupt bool `json:"can_interrupt"`
	CanResume    bool `json:"can_resume"`
	CanDelete    bool `json:"can_delete"`
}

type WorkflowProjectSubscribeRequest struct {
	ProjectID string `json:"project_id,omitempty"`
}

type WorkflowSubscribeRequest struct {
	WorkflowID runtimeids.WorkflowID `json:"workflow_id"`
}

type WorkflowProjectEventResource = protocol.WorkflowProjectEventResource

const (
	WorkflowProjectEventResourceWorkflow     = protocol.WorkflowProjectEventResourceWorkflow
	WorkflowProjectEventResourceWorkflowLink = protocol.WorkflowProjectEventResourceWorkflowLink
	WorkflowProjectEventResourceTask         = protocol.WorkflowProjectEventResourceTask
	WorkflowProjectEventResourceLabel        = protocol.WorkflowProjectEventResourceLabel
)

type WorkflowProjectEventAction = protocol.WorkflowProjectEventAction

const (
	WorkflowProjectEventActionCreated             = protocol.WorkflowProjectEventActionCreated
	WorkflowProjectEventActionUpdated             = protocol.WorkflowProjectEventActionUpdated
	WorkflowProjectEventActionRenamed             = protocol.WorkflowProjectEventActionRenamed
	WorkflowProjectEventActionReordered           = protocol.WorkflowProjectEventActionReordered
	WorkflowProjectEventActionDeleted             = protocol.WorkflowProjectEventActionDeleted
	WorkflowProjectEventActionGraphSaved          = protocol.WorkflowProjectEventActionGraphSaved
	WorkflowProjectEventActionLinked              = protocol.WorkflowProjectEventActionLinked
	WorkflowProjectEventActionDefaultChanged      = protocol.WorkflowProjectEventActionDefaultChanged
	WorkflowProjectEventActionUnlinked            = protocol.WorkflowProjectEventActionUnlinked
	WorkflowProjectEventActionStarted             = protocol.WorkflowProjectEventActionStarted
	WorkflowProjectEventActionInterrupted         = protocol.WorkflowProjectEventActionInterrupted
	WorkflowProjectEventActionResumed             = protocol.WorkflowProjectEventActionResumed
	WorkflowProjectEventActionApproved            = protocol.WorkflowProjectEventActionApproved
	WorkflowProjectEventActionMoved               = protocol.WorkflowProjectEventActionMoved
	WorkflowProjectEventActionCompleted           = protocol.WorkflowProjectEventActionCompleted
	WorkflowProjectEventActionCommentAdded        = protocol.WorkflowProjectEventActionCommentAdded
	WorkflowProjectEventActionCommentUpdated      = protocol.WorkflowProjectEventActionCommentUpdated
	WorkflowProjectEventActionCommentDeleted      = protocol.WorkflowProjectEventActionCommentDeleted
	WorkflowProjectEventActionQuestionWaiting     = protocol.WorkflowProjectEventActionQuestionWaiting
	WorkflowProjectEventActionQuestionCleared     = protocol.WorkflowProjectEventActionQuestionCleared
	WorkflowProjectEventActionLabelsChanged       = protocol.WorkflowProjectEventActionLabelsChanged
	WorkflowProjectEventActionDependenciesChanged = protocol.WorkflowProjectEventActionDependenciesChanged
)

type WorkflowProjectEvent struct {
	ProjectID        *string                      `json:"project_id,omitempty"`
	WorkflowID       *runtimeids.WorkflowID       `json:"workflow_id,omitempty"`
	Resource         WorkflowProjectEventResource `json:"resource"`
	Action           WorkflowProjectEventAction   `json:"action"`
	PrimaryEntityID  string                       `json:"primary_entity_id"`
	RelatedIDs       []string                     `json:"related_ids,omitempty"`
	OccurredAtUnixMs int64                        `json:"occurred_at_unix_ms"`
}

func (e WorkflowProjectEvent) Validate() error {
	if e.ProjectID != nil {
		if err := validateRequired("project_id", *e.ProjectID); err != nil {
			return err
		}
		if strings.TrimSpace(*e.ProjectID) != *e.ProjectID {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "project_id", "project_id must not have leading or trailing whitespace")
		}
	}
	if e.WorkflowID != nil {
		if err := validateRequiredWorkflowID(*e.WorkflowID); err != nil {
			return err
		}
	}
	if err := validateRequired("primary_entity_id", e.PrimaryEntityID); err != nil {
		return err
	}
	if strings.TrimSpace(e.PrimaryEntityID) != e.PrimaryEntityID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "primary_entity_id", "primary_entity_id must not have leading or trailing whitespace")
	}
	if e.OccurredAtUnixMs <= 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "occurred_at_unix_ms", "occurred_at_unix_ms must be positive")
	}
	if !workflowProjectEventActionAllowed(e.Resource, e.Action) {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "action", "action is not valid for resource")
	}
	switch e.Resource {
	case WorkflowProjectEventResourceWorkflow:
		if e.WorkflowID == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "workflow_id", "workflow_id is required")
		}
	case WorkflowProjectEventResourceWorkflowLink, WorkflowProjectEventResourceTask:
		if e.ProjectID == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "project_id", "project_id is required")
		}
		if e.WorkflowID == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "workflow_id", "workflow_id is required")
		}
	case WorkflowProjectEventResourceLabel:
		if e.ProjectID == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "project_id", "project_id is required")
		}
		if e.WorkflowID != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "workflow_id", "workflow_id must be absent for label events")
		}
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "resource", "resource is invalid")
	}
	seen := map[string]struct{}{e.PrimaryEntityID: {}}
	for _, relatedID := range e.RelatedIDs {
		if err := validateRequired("related_ids", relatedID); err != nil {
			return err
		}
		if strings.TrimSpace(relatedID) != relatedID {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "related_ids", "related_ids must not have leading or trailing whitespace")
		}
		if _, exists := seen[relatedID]; exists {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "related_ids", "related_ids must be unique and must not repeat primary_entity_id")
		}
		seen[relatedID] = struct{}{}
	}
	return nil
}

func workflowProjectEventActionAllowed(resource WorkflowProjectEventResource, action WorkflowProjectEventAction) bool {
	switch resource {
	case WorkflowProjectEventResourceWorkflow:
		switch action {
		case WorkflowProjectEventActionUpdated,
			WorkflowProjectEventActionDeleted,
			WorkflowProjectEventActionGraphSaved:
			return true
		}
	case WorkflowProjectEventResourceWorkflowLink:
		switch action {
		case WorkflowProjectEventActionLinked,
			WorkflowProjectEventActionDefaultChanged,
			WorkflowProjectEventActionUnlinked:
			return true
		}
	case WorkflowProjectEventResourceTask:
		switch action {
		case WorkflowProjectEventActionCreated,
			WorkflowProjectEventActionUpdated,
			WorkflowProjectEventActionDeleted,
			WorkflowProjectEventActionStarted,
			WorkflowProjectEventActionInterrupted,
			WorkflowProjectEventActionResumed,
			WorkflowProjectEventActionApproved,
			WorkflowProjectEventActionMoved,
			WorkflowProjectEventActionCompleted,
			WorkflowProjectEventActionCommentAdded,
			WorkflowProjectEventActionCommentUpdated,
			WorkflowProjectEventActionCommentDeleted,
			WorkflowProjectEventActionQuestionWaiting,
			WorkflowProjectEventActionQuestionCleared,
			WorkflowProjectEventActionLabelsChanged,
			WorkflowProjectEventActionDependenciesChanged:
			return true
		}
	case WorkflowProjectEventResourceLabel:
		switch action {
		case WorkflowProjectEventActionCreated,
			WorkflowProjectEventActionRenamed,
			WorkflowProjectEventActionReordered,
			WorkflowProjectEventActionDeleted:
			return true
		}
	}
	return false
}

type WorkflowProjectSubscription interface {
	Next(ctx context.Context) (WorkflowProjectEvent, error)
	Close() error
}

type WorkflowSubscription interface {
	Next(ctx context.Context) (WorkflowProjectEvent, error)
	Close() error
}

type WorkflowTaskGetRequest struct {
	TaskID    string `json:"task_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	ShortID   string `json:"short_id,omitempty"`
}

type WorkflowTaskGetResponse struct {
	Task WorkflowTaskDetail `json:"task"`
}

type WorkflowTaskActivityListResponse struct {
	WorkflowOffsetPage[WorkflowTaskActivityItem]
}

type WorkflowTaskSessionStatus string

const (
	WorkflowTaskSessionStatusRunning  WorkflowTaskSessionStatus = "running"
	WorkflowTaskSessionStatusQuestion WorkflowTaskSessionStatus = "question"
	WorkflowTaskSessionStatusIdle     WorkflowTaskSessionStatus = "idle"
)

type WorkflowTaskSessionItem struct {
	SessionID   string                    `json:"session_id"`
	SessionName *string                   `json:"session_name,omitempty"`
	NodeName    *string                   `json:"node_name,omitempty"`
	AgentRole   string                    `json:"agent_role"`
	Status      WorkflowTaskSessionStatus `json:"status"`
}

type WorkflowTaskSessionListResponse struct {
	TaskID string `json:"task_id"`
	WorkflowOffsetPage[WorkflowTaskSessionItem]
}

type WorkflowTaskSummary struct {
	ID                string                `json:"id"`
	ProjectID         string                `json:"project_id"`
	WorkflowID        runtimeids.WorkflowID `json:"workflow_id"`
	ShortID           string                `json:"short_id"`
	Title             string                `json:"title"`
	BodyPreview       string                `json:"body_preview,omitempty"`
	SourceWorkspaceID string                `json:"source_workspace_id,omitempty"`
	CreatedAtUnixMs   int64                 `json:"created_at_unix_ms"`
	UpdatedAtUnixMs   int64                 `json:"updated_at_unix_ms"`
	Done              bool                  `json:"done"`
	ActiveNodeIDs     []string              `json:"active_node_ids,omitempty"`
}

type WorkflowTaskDetail struct {
	Summary              WorkflowTaskSummary         `json:"summary"`
	Project              ProjectBoardProject         `json:"project"`
	Workflow             WorkflowTaskWorkflowSummary `json:"workflow"`
	Body                 string                      `json:"body"`
	SourceURL            string                      `json:"source_url,omitempty"`
	SourceWorkspace      ProjectWorkspaceSummary     `json:"source_workspace"`
	ExecutionTarget      *WorkflowExecutionTarget    `json:"execution_target,omitempty"`
	WorktreePath         *string                     `json:"worktree_path"`
	CurrentNodes         []WorkflowTaskCurrentNode   `json:"current_nodes"`
	LiveSessions         []WorkflowTaskLiveSession   `json:"live_sessions"`
	CurrentScripts       []WorkflowTaskCurrentScript `json:"current_scripts"`
	RetainedSessionCount int                         `json:"retained_session_count"`
	Status               WorkflowTaskStatus          `json:"status"`
	Actions              WorkflowTaskActions         `json:"actions"`
	LabelIDs             []string                    `json:"label_ids"`
	AttentionCount       int                         `json:"attention_count"`
	Dependencies         WorkflowTaskDependencies    `json:"dependencies"`
}

type WorkflowTaskLiveSession struct {
	SessionID       string  `json:"session_id"`
	SessionName     *string `json:"session_name,omitempty"`
	NodeDisplayName string  `json:"node_display_name"`
}

type WorkflowTaskDependencyDirection string

const (
	WorkflowTaskDependencyDirectionBlockedBy WorkflowTaskDependencyDirection = "blocked-by"
	WorkflowTaskDependencyDirectionBlocks    WorkflowTaskDependencyDirection = "blocks"
)

type WorkflowTaskDependencySatisfaction string

const (
	WorkflowTaskDependencySatisfied   WorkflowTaskDependencySatisfaction = "satisfied"
	WorkflowTaskDependencyUnsatisfied WorkflowTaskDependencySatisfaction = "unsatisfied"
)

type WorkflowTaskDependencyAddAvailability struct {
	Available    *WorkflowTaskDependencyAvailable    `json:"available,omitempty"`
	LimitReached *WorkflowTaskDependencyLimitReached `json:"limit_reached,omitempty"`
}

type WorkflowTaskDependencyAvailable struct {
	RemainingCapacity int `json:"remaining_capacity"`
}

type WorkflowTaskDependencyLimitReached struct{}

type WorkflowTaskDependencyItem struct {
	TaskID       string                              `json:"task_id"`
	ShortID      string                              `json:"short_id"`
	Title        string                              `json:"title"`
	WorkflowID   string                              `json:"workflow_id"`
	Status       WorkflowTaskStatus                  `json:"status"`
	Satisfaction *WorkflowTaskDependencySatisfaction `json:"satisfaction,omitempty"`
}

type WorkflowTaskDependencyDirectionProjection struct {
	Direction        WorkflowTaskDependencyDirection        `json:"direction"`
	TotalCount       int                                    `json:"total_count"`
	UnsatisfiedCount *int                                   `json:"unsatisfied_count,omitempty"`
	Items            []WorkflowTaskDependencyItem           `json:"items"`
	AddAvailability  *WorkflowTaskDependencyAddAvailability `json:"add_availability,omitempty"`
}

type WorkflowTaskDependencies struct {
	BlockerCount             int                                         `json:"blocker_count"`
	UnsatisfiedBlockerCount  int                                         `json:"unsatisfied_blocker_count"`
	DirectlyBlockedTaskCount int                                         `json:"directly_blocked_task_count"`
	Directions               []WorkflowTaskDependencyDirectionProjection `json:"directions"`
}

type WorkflowTaskWorkflowSummary struct {
	WorkflowID  runtimeids.WorkflowID `json:"workflow_id"`
	DisplayName string                `json:"display_name"`
	Version     int64                 `json:"version"`
}

type WorkflowTaskCurrentScript struct {
	CurrentNode WorkflowTaskCurrentNode `json:"current_node"`
	Path        string                  `json:"path"`
}

type WorkflowTaskComment struct {
	ID              string `json:"id"`
	TaskID          string `json:"task_id"`
	Body            string `json:"body"`
	Author          string `json:"author"`
	AuthorID        string `json:"author_id,omitempty"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
	UpdatedAt       int64  `json:"updated_at_unix_ms"`
}

type WorkflowTaskActivityItem struct {
	ActivityID       string                      `json:"activity_id"`
	Type             string                      `json:"type"`
	TaskID           string                      `json:"task_id"`
	OccurredAtUnixMs int64                       `json:"occurred_at_unix_ms"`
	UpdatedAtUnixMs  int64                       `json:"updated_at_unix_ms"`
	Comment          *WorkflowTaskComment        `json:"comment,omitempty"`
	SessionStarted   *WorkflowTaskSessionStarted `json:"session_started,omitempty"`
}

type WorkflowTaskSessionStarted struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

func (r WorkflowCreateRequest) Validate() error {
	_, err := r.Prepare()
	return err
}

func (r WorkflowCreateRequest) Prepare() (workflowcontract.WorkflowName, error) {
	return prepareWorkflowName(r.Name, "name")
}

func (r WorkflowCreateAndLinkProjectRequest) Validate() error {
	_, err := r.Prepare()
	return err
}

func (r WorkflowCreateAndLinkProjectRequest) Prepare() (workflowcontract.WorkflowName, error) {
	name, err := prepareWorkflowName(r.Name, "name")
	if err != nil {
		return workflowcontract.WorkflowName{}, err
	}
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return workflowcontract.WorkflowName{}, err
	}
	if err := validateWorkflowProjectLinkDefaultMode(r.DefaultPolicy); err != nil {
		return workflowcontract.WorkflowName{}, err
	}
	return name, nil
}

func (r WorkflowUpdateRequest) Validate() error {
	_, err := r.Prepare()
	return err
}

func (r WorkflowUpdateRequest) Prepare() (workflowcontract.WorkflowName, error) {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return workflowcontract.WorkflowName{}, err
	}
	return prepareWorkflowName(r.Name, "name")
}

func (r WorkflowListRequest) Validate() error {
	if _, err := ResolveWorkflowOffsetWindow(r.Offset, r.Limit); err != nil {
		return err
	}
	if r.ProjectID != nil {
		if err := validateRequired("project_id", *r.ProjectID); err != nil {
			return err
		}
		if strings.TrimSpace(*r.ProjectID) != *r.ProjectID {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "project_id", "project_id must not contain surrounding whitespace")
		}
	}
	if r.WorkflowID != nil {
		if err := validateRequiredWorkflowID(*r.WorkflowID); err != nil {
			return err
		}
	}
	return nil
}

type WorkflowOffsetWindow = OffsetWindow

func ResolveWorkflowOffsetWindow(offset *int, limit *int) (WorkflowOffsetWindow, error) {
	window, err := ResolveOffsetWindow(offset, limit)
	if err == nil {
		return window, nil
	}
	var windowError *offsetWindowError
	if !errors.As(err, &windowError) {
		return WorkflowOffsetWindow{}, err
	}
	return WorkflowOffsetWindow{}, workflowRequestError(
		WorkflowRequestErrorInvalidMode,
		windowError.Field,
		windowError.Message,
	)
}

func (r WorkflowGetRequest) Validate() error {
	return validateRequiredWorkflowID(r.WorkflowID)
}

func (r WorkflowLinkProjectRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	return validateWorkflowProjectLinkDefaultMode(r.DefaultPolicy)
}

func (r WorkflowListProjectLinksRequest) Validate() error {
	return validateRequired("project_id", r.ProjectID)
}

func (r WorkflowSetDefaultProjectLinkRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	return validateRequiredWorkflowID(r.WorkflowID)
}

func (r WorkflowUnlinkProjectRequest) Validate() error {
	return validateRequired("link_id", r.LinkID)
}

func (r WorkflowDeletePreviewRequest) Validate() error {
	return validateRequiredWorkflowID(r.WorkflowID)
}

func (r WorkflowDeleteRequest) Validate() error {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	if r.ExpectedVersion < 0 {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "expected_version", Message: "expected version must be non-negative"}
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"expected_project_count", r.ExpectedProjectCount},
		{"expected_link_count", r.ExpectedLinkCount},
		{"expected_task_count", r.ExpectedTaskCount},
	} {
		if field.value < 0 {
			return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: field.name, Message: "expected count must be non-negative"}
		}
	}
	return nil
}

func (r WorkflowValidateRequest) Validate() error {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	switch r.Mode {
	case "", WorkflowValidationModeDraft, WorkflowValidationModeTaskCreation, WorkflowValidationModeExecution:
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "mode", "mode must be draft, task_creation, or execution")
	}
}

func (r WorkflowScriptPathValidateRequest) Validate() error {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	return validateRequired("node_id", r.NodeID)
}

func (r WorkflowGraphValidateDraftRequest) Validate() error {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	if err := validateWorkflowGraphMetadata(r.Metadata); err != nil {
		return err
	}
	if err := validateWorkflowGraphValidationModes(r.Modes); err != nil {
		return err
	}
	return validateWorkflowGraphDraftEnvelope(r.Graph)
}

func (r WorkflowGraphDeriveWiringRequest) Validate() error {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	return validateWorkflowGraphDraftEnvelope(r.Graph)
}

func (r WorkflowGraphSavePreviewRequest) Validate() error {
	return validateWorkflowGraphSavePreviewRequest(r)
}

func (r WorkflowGraphSavePreviewRequest) ValidateRPC() error {
	return validateWorkflowGraphSavePreviewRequest(r)
}

func validateWorkflowGraphSavePreviewRequest(r WorkflowGraphSavePreviewRequest) error {
	if err := validateWorkflowGraphSavePreviewFields(r); err != nil {
		return err
	}
	return validateWorkflowGraphDraftEnvelope(r.Graph)
}

func validateWorkflowGraphSavePreviewFields(r WorkflowGraphSavePreviewRequest) error {
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	if err := validateWorkflowGraphMetadata(r.Metadata); err != nil {
		return err
	}
	if r.ExpectedVersion < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "expected_version", "expected_version must be non-negative")
	}
	return nil
}

func (r WorkflowGraphSaveRequest) Validate() error {
	return validateWorkflowGraphSaveRequest(r)
}

func (r WorkflowGraphSaveRequest) ValidateRPC() error {
	return validateWorkflowGraphSaveRequest(r)
}

func validateWorkflowGraphSaveRequest(r WorkflowGraphSaveRequest) error {
	if err := validateWorkflowGraphSavePreviewRequest(WorkflowGraphSavePreviewRequest{WorkflowID: r.WorkflowID, ExpectedVersion: r.ExpectedVersion, Metadata: r.Metadata, Graph: r.Graph}); err != nil {
		return err
	}
	return validateWorkflowGraphSaveConfirmation(r.Confirmation)
}

func validateWorkflowGraphSaveConfirmation(confirmation *WorkflowGraphSaveConfirmation) error {
	if confirmation == nil {
		return nil
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"expected_removed_node_group_count", confirmation.ExpectedRemovedNodeGroupCount},
		{"expected_removed_node_count", confirmation.ExpectedRemovedNodeCount},
		{"expected_removed_transition_group_count", confirmation.ExpectedRemovedTransitionGroupCount},
		{"expected_removed_edge_count", confirmation.ExpectedRemovedEdgeCount},
		{"expected_node_task_reference_count", confirmation.ExpectedNodeTaskReferenceCount},
		{"expected_edge_task_reference_count", confirmation.ExpectedEdgeTaskReferenceCount},
	} {
		if field.value < 0 {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field.name, field.name+" must be non-negative")
		}
	}
	return nil
}

func (r WorkflowGraphSavePreviewResponse) Validate() error {
	return validateWorkflowGraphSaveResponse(r.CurrentVersion, r.Impact, r.Blockers)
}

func (r WorkflowGraphSaveResponse) Validate() error {
	return validateWorkflowGraphSaveResponse(r.CurrentVersion, r.Impact, r.Blockers)
}

func validateWorkflowGraphSaveResponse(currentVersion int64, impact WorkflowGraphSaveImpact, blockers []WorkflowGraphSaveBlocker) error {
	if currentVersion < 0 {
		return errors.New("current_version must be non-negative")
	}
	if err := impact.Validate(); err != nil {
		return fmt.Errorf("impact: %w", err)
	}
	return validateWorkflowGraphSaveBlockers(blockers)
}

func (i WorkflowGraphSaveImpact) Validate() error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"removed_node_group_count", i.RemovedNodeGroupCount},
		{"removed_node_count", i.RemovedNodeCount},
		{"removed_transition_group_count", i.RemovedTransitionGroupCount},
		{"removed_edge_count", i.RemovedEdgeCount},
		{"node_task_reference_count", i.NodeTaskReferenceCount},
		{"edge_task_reference_count", i.EdgeTaskReferenceCount},
		{"active_current_node_count", i.ActiveCurrentNodeCount},
		{"pending_approval_count", i.PendingApprovalCount},
		{"start_node_change_count", i.StartNodeChangeCount},
		{"last_terminal_change_count", i.LastTerminalChangeCount},
		{"task_referenced_node_kind_change_count", i.TaskReferencedNodeKindChangeCount},
	} {
		if field.value < 0 {
			return fmt.Errorf("%s must be non-negative", field.name)
		}
	}
	if err := validateWorkflowGraphEntityReferences(i.RemovedEntities, "removed_entities"); err != nil {
		return err
	}
	counts := map[WorkflowGraphEntityType]int64{}
	for _, entity := range i.RemovedEntities {
		counts[entity.EntityType]++
	}
	for _, expected := range []struct {
		entityType WorkflowGraphEntityType
		count      int64
	}{
		{WorkflowGraphEntityTypeNodeGroup, i.RemovedNodeGroupCount},
		{WorkflowGraphEntityTypeNode, i.RemovedNodeCount},
		{WorkflowGraphEntityTypeTransitionGroup, i.RemovedTransitionGroupCount},
		{WorkflowGraphEntityTypeEdge, i.RemovedEdgeCount},
	} {
		if counts[expected.entityType] != expected.count {
			return fmt.Errorf("removed_entities %s count = %d, want %d", expected.entityType, counts[expected.entityType], expected.count)
		}
	}
	return nil
}

func validateWorkflowGraphEntityReference(r WorkflowGraphEntityReference) error {
	switch r.EntityType {
	case WorkflowGraphEntityTypeEdge,
		WorkflowGraphEntityTypeNode,
		WorkflowGraphEntityTypeNodeGroup,
		WorkflowGraphEntityTypeTransitionGroup:
	default:
		return errors.New("entity_type is invalid")
	}
	return validateRequired("entity_id", r.EntityID)
}

func (b WorkflowGraphSaveBlocker) Validate() error {
	if err := validateRequired("code", b.Code); err != nil {
		return err
	}
	if err := validateRequired("message", b.Message); err != nil {
		return err
	}
	if b.Count < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "count", "count must be non-negative")
	}
	return validateWorkflowGraphEntityReferences(b.AffectedEntities, "affected_entities")
}

func validateWorkflowGraphSaveBlockers(blockers []WorkflowGraphSaveBlocker) error {
	for index, blocker := range blockers {
		if err := blocker.Validate(); err != nil {
			return fmt.Errorf("blockers[%d]: %w", index, err)
		}
	}
	return nil
}

func validateWorkflowGraphEntityReferences(references []WorkflowGraphEntityReference, field string) error {
	if references == nil {
		return fmt.Errorf("%s must be present", field)
	}
	for index, reference := range references {
		if err := validateWorkflowGraphEntityReference(reference); err != nil {
			return fmt.Errorf("%s[%d]: %w", field, index, err)
		}
	}
	if !slices.IsSortedFunc(references, CompareWorkflowGraphEntityReferences) {
		return fmt.Errorf("%s must use canonical entity_type and entity_id order", field)
	}
	for index := 1; index < len(references); index++ {
		if CompareWorkflowGraphEntityReferences(references[index-1], references[index]) == 0 {
			return fmt.Errorf("%s contains duplicate entity reference", field)
		}
	}
	return nil
}

// CompareWorkflowGraphEntityReferences defines the canonical graph entity ordering used by graph-save producers and consumers.
func CompareWorkflowGraphEntityReferences(left WorkflowGraphEntityReference, right WorkflowGraphEntityReference) int {
	return workflowcontract.CompareWorkflowGraphEntityReferences(left, right)
}

func validateWorkflowGraphMetadata(metadata *WorkflowGraphMetadata) error {
	if metadata == nil {
		return nil
	}
	name, err := workflowcontract.NewWorkflowName(metadata.Name)
	if err != nil {
		return workflowNameRequestError(err, "metadata.name")
	}
	if name.String() != metadata.Name {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "metadata.name", "metadata.name must not have leading or trailing whitespace")
	}
	if metadata.Description != strings.TrimSpace(metadata.Description) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "metadata.description", "metadata.description must not have leading or trailing whitespace")
	}
	if metadata.ExecutionTargetPolicy != nil {
		if err := metadata.ExecutionTargetPolicy.Validate(true); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowName(raw string, field string) error {
	_, err := prepareWorkflowName(raw, field)
	return err
}

func prepareWorkflowName(raw string, field string) (workflowcontract.WorkflowName, error) {
	name, err := workflowcontract.NewWorkflowName(raw)
	return name, workflowNameRequestError(err, field)
}

func workflowNameRequestError(err error, field string) error {
	switch {
	case errors.Is(err, workflowcontract.ErrWorkflowNameRequired):
		return workflowRequestError(WorkflowRequestErrorRequired, field, field+" is required")
	case errors.Is(err, workflowcontract.ErrWorkflowNameTooLong):
		return workflowRequestError(WorkflowRequestErrorTooLong, field, field+" must be <= 120 characters")
	default:
		return err
	}
}

func validateWorkflowGraphValidationModes(modes []WorkflowValidationMode) error {
	if len(modes) == 0 {
		return workflowRequestError(WorkflowRequestErrorRequired, "modes", "modes is required")
	}
	for _, mode := range modes {
		switch mode {
		case WorkflowValidationModeDraft, WorkflowValidationModeTaskCreation, WorkflowValidationModeExecution:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "modes", "modes must contain only draft, task_creation, or execution")
		}
	}
	return nil
}

func validateWorkflowGraphDraftEnvelope(graph WorkflowGraphDraft) error {
	if err := validateWorkflowGraphDraftCollectionBounds(graph); err != nil {
		return err
	}
	for _, node := range graph.Nodes {
		if kind := WorkflowNodeKind(strings.TrimSpace(node.Kind)); !slices.Contains([]WorkflowNodeKind{WorkflowNodeKindStart, WorkflowNodeKindAgent, WorkflowNodeKindScript, WorkflowNodeKindJoin, WorkflowNodeKindTerminal}, kind) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "graph.nodes.kind", "node kind is invalid")
		}
		if node.GroupID != nil && strings.TrimSpace(*node.GroupID) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "graph.nodes.group_id", "group_id must be non-blank when present")
		}
		if len(node.JoinInputProviders) > WorkflowGraphDraftMaxFieldsPerEntity {
			return workflowRequestError(WorkflowRequestErrorTooLong, "graph.nodes.join_input_providers", fmt.Sprintf("join_input_providers must be <= %d", WorkflowGraphDraftMaxFieldsPerEntity))
		}
	}
	for _, edge := range graph.Edges {
		switch strings.TrimSpace(edge.AssigneeSelection) {
		case "configured", "previous_node":
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "graph.edges.assignee_selection", "graph.edges.assignee_selection must be configured or previous_node")
		}
		switch strings.TrimSpace(edge.ThinkingSelection) {
		case "configured", "previous_node":
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "graph.edges.thinking_selection", "graph.edges.thinking_selection must be configured or previous_node")
		}
		switch strings.TrimSpace(edge.ContextMode) {
		case "new_session", "continue_session", "compact_and_continue_session":
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "graph.edges.context_mode", "graph.edges.context_mode is invalid")
		}
		switch strings.TrimSpace(edge.ContextSource.Kind) {
		case "", "immediate_source", "selected_node", "previous_target", "previous_target_or_new":
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "graph.edges.context_source.kind", "context source kind is invalid")
		}
		for _, parameter := range edge.Parameters {
			switch strings.TrimSpace(parameter.Purpose) {
			case "ordinary", "target_assignee", "target_thinking":
			default:
				return workflowRequestError(WorkflowRequestErrorInvalidValue, "graph.edges.parameters.purpose", "parameter purpose is invalid")
			}
		}
		if len(edge.Parameters) > WorkflowGraphDraftMaxFieldsPerEntity {
			return workflowRequestError(WorkflowRequestErrorTooLong, "graph.edges.parameters", fmt.Sprintf("parameters must be <= %d", WorkflowGraphDraftMaxFieldsPerEntity))
		}
	}
	for _, group := range graph.TransitionGroups {
		if len([]rune(group.Description)) > 1000 {
			return workflowRequestError(WorkflowRequestErrorTooLong, "graph.transition_groups.description", "description must be <= 1000 characters")
		}
	}
	return nil
}

func validateWorkflowGraphDraftCollectionBounds(graph WorkflowGraphDraft) error {
	for _, field := range []struct {
		name  string
		count int
		limit int
	}{
		{"node_groups", len(graph.NodeGroups), WorkflowGraphDraftMaxNodeGroups},
		{"nodes", len(graph.Nodes), WorkflowGraphDraftMaxNodes},
		{"transition_groups", len(graph.TransitionGroups), WorkflowGraphDraftMaxTransitionGroups},
		{"edges", len(graph.Edges), WorkflowGraphDraftMaxEdges},
	} {
		if field.count > field.limit {
			return workflowRequestError(WorkflowRequestErrorTooLong, "graph."+field.name, fmt.Sprintf("%s must be <= %d", field.name, field.limit))
		}
	}
	return nil
}

func validateGraphEntityID(field, value string) error {
	if _, err := runtimeids.GraphEntityIDBlob(value); err != nil {
		return workflowRequestError(
			WorkflowRequestErrorInvalidValue,
			field,
			field+" must be canonical UUIDv4 text",
		)
	}
	return nil
}

func (r WorkflowTaskCreateRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if _, err := workflowcontract.NewTaskTitle(r.Title); err != nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "title", err.Error())
	}
	if err := validateLabelIDs("label_ids", r.LabelIDs); err != nil {
		return err
	}
	roleCounts := map[WorkflowTaskDependencyRole]int{}
	type dependencyIntentIdentity struct {
		role          WorkflowTaskDependencyRole
		relatedTaskID string
	}
	seenIntents := map[dependencyIntentIdentity]struct{}{}
	for index, intent := range r.DependencyIntents {
		field := fmt.Sprintf("dependency_intents[%d]", index)
		if err := intent.validate(field); err != nil {
			return err
		}
		identity := dependencyIntentIdentity{role: intent.NewTaskRole, relatedTaskID: intent.RelatedTaskID}
		if _, exists := seenIntents[identity]; exists {
			return workflowRequestError(
				WorkflowRequestErrorInvalidValue,
				field+".related_task_id",
				"dependency intent duplicates an earlier entry for the same new Task role",
			)
		}
		seenIntents[identity] = struct{}{}
		roleCounts[intent.NewTaskRole]++
		if roleCounts[intent.NewTaskRole] > workflowcontract.MaxTaskDependencies {
			return workflowRequestError(
				WorkflowRequestErrorTooLong,
				"dependency_intents",
				fmt.Sprintf("dependency intents must contain at most %d entries per new Task role", workflowcontract.MaxTaskDependencies),
			)
		}
	}
	if r.WorkflowID != nil {
		return validateRequiredWorkflowID(*r.WorkflowID)
	}
	return nil
}

func (r WorkflowTaskUpdateRequest) Validate() error {
	if err := validateTaskID("task_id", r.TaskID); err != nil {
		return err
	}
	if r.Title != nil {
		if _, err := workflowcontract.NewTaskTitle(*r.Title); err != nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "title", err.Error())
		}
	}
	return nil
}

func (r WorkflowTaskGetResponse) Validate() error {
	if err := r.Task.Validate(); err != nil {
		return err
	}
	if r.Task.AttentionCount < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.attention_count", "attention_count must be non-negative")
	}
	if r.Task.WorktreePath != nil && strings.TrimSpace(*r.Task.WorktreePath) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.worktree_path", "worktree_path must be non-blank when present")
	}
	if r.Task.RetainedSessionCount < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.retained_session_count", "retained_session_count must be non-negative")
	}
	if r.Task.CurrentNodes == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "task.current_nodes", "current_nodes is required")
	}
	for index, node := range r.Task.CurrentNodes {
		if err := validateWorkflowTaskCurrentNode(node); err != nil {
			return prefixWorkflowProjectionValidationField("task.current_nodes", index, err)
		}
	}
	if r.Task.LiveSessions == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "task.live_sessions", "live_sessions is required")
	}
	for index, session := range r.Task.LiveSessions {
		if strings.TrimSpace(session.SessionID) == "" || (index > 0 && r.Task.LiveSessions[index-1].SessionID >= session.SessionID) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.live_sessions", "live_sessions must contain sorted unique non-blank Session IDs")
		}
		if session.SessionName != nil && strings.TrimSpace(*session.SessionName) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.live_sessions", "live_sessions must contain non-blank Session names when present")
		}
		if strings.TrimSpace(session.NodeDisplayName) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.live_sessions", "live_sessions must contain non-blank Agent Node display names")
		}
	}
	if r.Task.CurrentScripts == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "task.current_scripts", "current_scripts is required")
	}
	for index, script := range r.Task.CurrentScripts {
		if err := validateWorkflowTaskCurrentNode(script.CurrentNode); err != nil {
			return prefixWorkflowProjectionValidationField("task.current_scripts", index, err)
		}
		if strings.TrimSpace(script.Path) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.current_scripts", "current_scripts must contain non-blank paths")
		}
	}
	if r.Task.ExecutionTarget != nil {
		return r.Task.ExecutionTarget.Validate()
	}
	return nil
}

func (r WorkflowTaskDetail) Validate() error {
	if err := validateRequired("task.summary.id", r.Summary.ID); err != nil {
		return err
	}
	if err := validateLabelIDs("task.label_ids", r.LabelIDs); err != nil {
		return err
	}
	for index, node := range r.CurrentNodes {
		if err := validateWorkflowTaskCurrentNode(node); err != nil {
			return prefixWorkflowProjectionValidationField("task.current_nodes", index, err)
		}
	}
	for index, script := range r.CurrentScripts {
		if err := validateWorkflowTaskCurrentNode(script.CurrentNode); err != nil {
			return prefixWorkflowProjectionValidationField("task.current_scripts", index, err)
		}
	}
	return r.Dependencies.Validate()
}

func (r WorkflowTaskListItem) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateProjectLabels("labels", r.Labels); err != nil {
		return err
	}
	if r.DependencyProgress != nil {
		return r.DependencyProgress.Validate()
	}
	return nil
}

func (r WorkflowTaskCreateRequest) ValidateRPC() error {
	if err := validateLabelIDs("label_ids", r.LabelIDs); err != nil {
		return workflowLabelRPCValidationError(err, r.ProjectID, "", false)
	}
	return r.Validate()
}

func (r WorkflowTaskListResponse) Validate() error {
	for index, task := range r.Tasks {
		if err := task.Validate(); err != nil {
			return prefixWorkflowProjectionValidationField("tasks", index, err)
		}
	}
	return nil
}

func (r WorkflowProjectTaskGroupCountsRequest) Validate() error {
	return validateRequired("project_id", r.ProjectID)
}

func (r WorkflowProjectTaskGroupCountsRequest) ValidateRPC() error {
	return r.Validate()
}

func (r WorkflowProjectTaskGroupCountsResponse) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	expectedDefinitions := WorkflowProjectTaskGroupDefinitions()
	if len(r.Definitions) != len(expectedDefinitions) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "definitions", "definitions must contain every Project Task group")
	}
	for index, definition := range r.Definitions {
		expected := expectedDefinitions[index]
		if definition.Group != expected.Group || !slices.Equal(definition.StatusKinds, expected.StatusKinds) {
			return workflowRequestError(
				WorkflowRequestErrorInvalidValue,
				fmt.Sprintf("definitions[%d]", index),
				"Project Task group definition is invalid",
			)
		}
	}
	for _, count := range []struct {
		field string
		value int
	}{
		{field: "counts.active", value: r.Counts.Active},
		{field: "counts.backlog", value: r.Counts.Backlog},
		{field: "counts.done", value: r.Counts.Done},
	} {
		if count.value < 0 {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, count.field, count.field+" must not be negative")
		}
	}
	return nil
}

func (r WorkflowBoardTaskCard) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateLabelIDs("label_ids", r.LabelIDs); err != nil {
		return err
	}
	if r.DependencyProgress != nil {
		return r.DependencyProgress.Validate()
	}
	return nil
}

func (r WorkflowBoardNodeCardsListResponse) Validate() error {
	for index, card := range r.Cards {
		if err := card.Validate(); err != nil {
			return prefixWorkflowProjectionValidationField("cards", index, err)
		}
	}
	return nil
}

func (r WorkflowAttentionItem) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateRequired("task_short_id", r.TaskShortID); err != nil {
		return err
	}
	if err := validateRequired("task_title", r.TaskTitle); err != nil {
		return err
	}
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	switch r.Kind {
	case "question":
		if err := validateRequiredAttentionString("message", r.Message); err != nil {
			return err
		}
		if r.CurrentNode == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "current_node", "current_node is required")
		}
		if err := validateWorkflowTaskCurrentNode(*r.CurrentNode); err != nil {
			return err
		}
		if r.CurrentNode.SessionID != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "current_node.session_id", "current_node.session_id is not allowed for kind question")
		}
		if err := validateOptionalAttentionString("session_name", r.SessionName); err != nil {
			return err
		}
		if r.Question == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "question", "question is required")
		}
		if err := r.Question.Validate(); err != nil {
			return err
		}
		return validateWorkflowAttentionFieldsAbsent(r.Kind,
			workflowAttentionFieldPresence{name: "approval_id", present: r.ApprovalID != nil},
			workflowAttentionFieldPresence{name: "approval_snapshot", present: r.ApprovalSnapshot != nil},
			workflowAttentionFieldPresence{name: "detail_json", present: r.DetailJSON != nil},
			workflowAttentionFieldPresence{name: "session_id", present: r.SessionID != nil},
		)
	case "approval":
		if err := validateOptionalAttentionString("message", r.Message); err != nil {
			return err
		}
		if err := validateRequiredAttentionString("approval_id", r.ApprovalID); err != nil {
			return err
		}
		if r.ApprovalSnapshot == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "approval_snapshot", "approval_snapshot is required")
		}
		if err := r.ApprovalSnapshot.Validate(); err != nil {
			return err
		}
		return validateWorkflowAttentionFieldsAbsent(r.Kind,
			workflowAttentionFieldPresence{name: "session_id", present: r.SessionID != nil},
			workflowAttentionFieldPresence{name: "session_name", present: r.SessionName != nil},
			workflowAttentionFieldPresence{name: "current_node", present: r.CurrentNode != nil},
			workflowAttentionFieldPresence{name: "question", present: r.Question != nil},
			workflowAttentionFieldPresence{name: "detail_json", present: r.DetailJSON != nil},
		)
	case "interrupted_current_node":
		if err := validateOptionalAttentionString("message", r.Message); err != nil {
			return err
		}
		if r.CurrentNode == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "current_node", "current_node is required")
		}
		if err := validateWorkflowTaskCurrentNode(*r.CurrentNode); err != nil {
			return err
		}
		if err := validateOptionalAttentionString("session_id", r.SessionID); err != nil {
			return err
		}
		if err := validateOptionalAttentionInterruptionDetailJSON("detail_json", r.DetailJSON); err != nil {
			return err
		}
		return validateWorkflowAttentionFieldsAbsent(r.Kind,
			workflowAttentionFieldPresence{name: "approval_id", present: r.ApprovalID != nil},
			workflowAttentionFieldPresence{name: "session_name", present: r.SessionName != nil},
			workflowAttentionFieldPresence{name: "question", present: r.Question != nil},
			workflowAttentionFieldPresence{name: "approval_snapshot", present: r.ApprovalSnapshot != nil},
		)
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "kind", "kind must be question, approval, or interrupted_current_node")
	}
}

func (s WorkflowAttentionApprovalSnapshot) Validate() error {
	if err := validateRequired("approval_snapshot.source_node_display_name", s.SourceNodeDisplayName); err != nil {
		return err
	}
	if s.Targets == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "approval_snapshot.targets", "targets is required")
	}
	for index, target := range s.Targets {
		if err := target.Validate(); err != nil {
			return prefixWorkflowProjectionValidationField("approval_snapshot.targets", index, err)
		}
	}
	if s.OutputValues == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "approval_snapshot.output_values", "output_values is required")
	}
	if s.WorkflowRevisionSeen < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "approval_snapshot.workflow_revision_seen", "workflow_revision_seen must be non-negative")
	}
	return nil
}

func (t WorkflowAttentionApprovalTarget) Validate() error {
	return validateRequired("display_name", t.DisplayName)
}

func (p WorkflowAttentionQuestionPrompt) Validate() error {
	if err := validateObservationPromptIdentity(p.PromptID, p.SessionID, p.StepID); err != nil {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "question.identity", err.Error())
	}
	switch p.Kind {
	case WorkflowAttentionQuestionKindOrdinary:
		if p.ApprovalDecisions != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "question.approval_decisions", "approval_decisions is not allowed for an ordinary question")
		}
		return validateWorkflowAttentionRecommendation(p.Suggestions, p.RecommendedOptionIndex)
	case WorkflowAttentionQuestionKindApproval:
		if p.Suggestions != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "question.suggestions", "suggestions is not allowed for an approval question")
		}
		if p.RecommendedOptionIndex != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "question.recommended_option_index", "recommended_option_index is not allowed for an approval question")
		}
		if len(p.ApprovalDecisions) == 0 {
			return workflowRequestError(WorkflowRequestErrorRequired, "question.approval_decisions", "approval_decisions is required for an approval question")
		}
		for _, decision := range p.ApprovalDecisions {
			if err := validateWorkflowApprovalDecision(decision); err != nil {
				return err
			}
		}
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "question.kind", "question kind must be ordinary or approval")
	}
}

func validateRequiredAttentionString(field string, value *string) error {
	if value == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, field, field+" is required")
	}
	return validateRequired(field, *value)
}

func validateOptionalAttentionString(field string, value *string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" must be non-blank when present")
	}
	return nil
}

type workflowAttentionInterruptionDetailSchema struct {
	Code                                 string             `json:"code"`
	Fields                               map[string]*string `json:"fields"`
	ConfiguredExecutionTargetUnavailable *struct {
		Mode         WorkflowExecutionTargetMode             `json:"mode"`
		RequestedRef *string                                 `json:"requested_ref,omitempty"`
		Cause        WorkflowExecutionTargetUnavailableCause `json:"cause"`
	} `json:"configured_execution_target_unavailable,omitempty"`
	SetupRecovery *worktreecontract.SetupRecoveryDetail[
		WorktreeSetupOperationID,
		WorkflowExecutionTargetSelection,
	] `json:"setup_recovery,omitempty"`
}

func validateOptionalAttentionInterruptionDetailJSON(field string, value *string) error {
	if err := validateOptionalAttentionString(field, value); err != nil || value == nil {
		return err
	}
	var detail workflowAttentionInterruptionDetailSchema
	if err := protocol.DecodeStrictJSON([]byte(*value), &detail); err != nil {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" must match the current Node interruption detail schema")
	}
	if strings.TrimSpace(detail.Code) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" code must be non-blank")
	}
	for name, value := range detail.Fields {
		if strings.TrimSpace(name) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" field names must be non-blank")
		}
		if value == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" values must be strings")
		}
	}
	if unavailable := detail.ConfiguredExecutionTargetUnavailable; unavailable != nil {
		requirement := WorkflowExecutionTargetSelectionRequirement{
			Reason: WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable,
			ConfiguredTarget: &WorkflowExecutionTargetConfiguredTarget{
				Mode:         unavailable.Mode,
				RequestedRef: unavailable.RequestedRef,
			},
			UnavailableCause: unavailable.Cause,
		}
		if err := requirement.Validate(); err != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" configured execution target metadata is invalid")
		}
	}
	if recovery := detail.SetupRecovery; recovery != nil {
		if err := recovery.Validate(); err != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" setup recovery facts are invalid")
		}
	}
	return nil
}

func validateWorkflowTaskCurrentNode(node WorkflowTaskCurrentNode) error {
	if err := validateRequired("node_id", node.NodeID); err != nil {
		return err
	}
	if node.TransitionBranchKey != nil {
		if err := validateRequired("transition_branch_key", *node.TransitionBranchKey); err != nil {
			return err
		}
	}
	if err := validateOptionalAttentionString("session_id", node.SessionID); err != nil {
		return err
	}
	if err := validateOptionalAttentionString("effective_assignee", node.EffectiveAssignee); err != nil {
		return err
	}
	return validateOptionalAttentionString("effective_thinking", node.EffectiveThinking)
}

func validateWorkflowAttentionRecommendation(suggestions []string, index *int) error {
	if index == nil {
		return nil
	}
	if *index < 1 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "recommended_option_index", "recommended_option_index must be positive when present")
	}
	if len(suggestions) == 0 || *index > len(suggestions) {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "recommended_option_index", "recommended_option_index must refer to a suggestion")
	}
	return nil
}

type workflowAttentionFieldPresence struct {
	name    string
	present bool
}

func validateWorkflowAttentionFieldsAbsent(kind string, fields ...workflowAttentionFieldPresence) error {
	for _, field := range fields {
		if field.present {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field.name, field.name+" is not allowed for kind "+kind)
		}
	}
	return nil
}

func (r WorkflowAttentionListResponse) Validate() error {
	return validateWorkflowAttentionItems(r.Items)
}

func (r WorkflowTaskAttentionListResponse) Validate() error {
	return validateWorkflowAttentionItems(r.Items)
}

func validateWorkflowAttentionItems(items []WorkflowAttentionItem) error {
	for index, item := range items {
		if err := item.Validate(); err != nil {
			return prefixWorkflowProjectionValidationField("items", index, err)
		}
	}
	return nil
}

func (r WorkflowTaskAttentionListResponse) ValidateForTask(taskID string) error {
	return validateWorkflowTaskBoundResponse(taskID, r.Validate, r.Items, func(item WorkflowAttentionItem) string {
		return item.TaskID
	})
}

func (r WorkflowTaskActivityListResponse) Validate() error {
	for index, item := range r.Items {
		switch item.Type {
		case "comment":
			if item.Comment == nil || item.SessionStarted != nil {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].type", index), "comment activity requires only comment")
			}
		case "session_started":
			if item.Comment != nil || item.SessionStarted == nil ||
				strings.TrimSpace(item.SessionStarted.SessionID) == "" ||
				strings.TrimSpace(item.SessionStarted.Name) == "" {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].type", index), "session_started activity requires a session")
			}
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].type", index), "activity type is invalid")
		}
	}
	return nil
}

func (r WorkflowTaskActivityListResponse) ValidateForTask(taskID string) error {
	return validateWorkflowTaskBoundResponse(taskID, r.Validate, r.Items, func(item WorkflowTaskActivityItem) string {
		return item.TaskID
	})
}

func validateWorkflowTaskBoundResponse[T any](taskID string, validate func() error, items []T, itemTaskID func(T) string) error {
	if err := validateRequired("task_id", taskID); err != nil {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	for index, item := range items {
		if itemTaskID(item) != taskID {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].task_id", index), "task_id must match request task_id")
		}
	}
	return nil
}

func prefixWorkflowProjectionValidationField(field string, index int, err error) error {
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	return workflowRequestError(
		validationErr.Code,
		fmt.Sprintf("%s[%d].%s", field, index, validationErr.Field),
		validationErr.Message,
	)
}

func (r WorkflowTaskStartRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateWorkflowTaskInvokingSession(r.InvokingSessionID); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	if err := validateWorkflowTaskInitialBranchName(r.BranchName); err != nil {
		return err
	}
	if r.ExecutionTarget != nil {
		return r.ExecutionTarget.Validate()
	}
	return nil
}

func (r WorkflowTaskResumeRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateWorkflowTaskInvokingSession(r.InvokingSessionID); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	if err := validateWorkflowTaskInitialBranchName(r.BranchName); err != nil {
		return err
	}
	if r.ExecutionTarget != nil {
		return r.ExecutionTarget.Validate()
	}
	return nil
}

func (r WorkflowTaskApproveRequest) Validate() error {
	if err := validateRequired("approval_id", r.ApprovalID); err != nil {
		return err
	}
	return validateWorkflowTaskInvokingSession(r.InvokingSessionID)
}

func (r WorkflowTaskMoveRequest) Validate() error {
	if err := validateRequiredFields(requiredField("task_id", r.TaskID), requiredField("target_node_id", r.TargetNodeID)); err != nil {
		return err
	}
	if err := validateWorkflowTaskInvokingSession(r.InvokingSessionID); err != nil {
		return err
	}
	if r.TransitionKey != nil && strings.TrimSpace(*r.TransitionKey) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "transition_key", "transition_key must be non-blank when present")
	}
	if err := validateWorkflowTaskInitialBranchName(r.BranchName); err != nil {
		return err
	}
	for nodeKey, outputs := range r.Values {
		if strings.TrimSpace(nodeKey) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "values", "values node keys must be non-blank")
		}
		if outputs == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "values", "values node entries must not be null")
		}
		for outputName, value := range outputs {
			if strings.TrimSpace(outputName) == "" {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, "values", "values output names must be non-blank")
			}
			if strings.TrimSpace(value) == "" {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, "values", "values must be non-blank")
			}
			if len(value) > workflowcontract.MaxOutputValueBytes {
				return workflowRequestError(WorkflowRequestErrorInvalidValue, "values", "values must not exceed the maximum output value size")
			}
		}
	}
	if r.ExecutionTarget != nil {
		return r.ExecutionTarget.Validate()
	}
	return nil
}

func validateWorkflowTaskInitialBranchName(branchName *string) error {
	if branchName != nil && strings.TrimSpace(*branchName) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "branch_name", "branch_name must be non-blank when present")
	}
	return nil
}

func validateWorkflowTaskInvokingSession(sessionID *runtimeids.SessionID) error {
	if sessionID != nil && sessionID.IsZero() {
		return workflowRequestError(WorkflowRequestErrorRequired, "invoking_session_id", "invoking_session_id is required when supplied")
	}
	return nil
}

func (r WorkflowTaskCompleteRequest) Validate() error {
	if err := validateWorkflowTaskCompleteActor(r); err != nil {
		return err
	}
	for _, field := range []requiredWorkflowField{
		requiredField("session_id", r.SessionID),
		requiredField("task_id", r.TaskID),
		requiredField("transition_id", r.TransitionID),
		requiredField("agent_session_id", r.AgentSessionID),
	} {
		if field.value != "" && strings.TrimSpace(field.value) != field.value {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, field.name, field.name+" must not have leading or trailing whitespace")
		}
	}
	selectorCount := workflowTaskCompleteSelectorCount(r)
	if selectorCount > 1 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "selector", "at most one completion target selector is allowed")
	}
	if strings.TrimSpace(r.ActorKind) == WorkflowTaskCompleteActorAgent {
		if strings.TrimSpace(r.AgentSessionID) == "" {
			return workflowRequestError(WorkflowRequestErrorRequired, "agent_session_id", "agent_session_id is required for agent completion")
		}
		return nil
	}
	if !r.Force {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "force", "force is required for non-agent completion")
	}
	if selectorCount != 1 {
		return workflowRequestError(WorkflowRequestErrorRequired, "selector", "one completion target selector is required")
	}
	return nil
}

func (r WorkflowTaskDeleteRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func (r WorkflowTaskInterruptRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	return validateWorkflowTaskInvokingSession(r.InvokingSessionID)
}

func validateWorkflowTaskCompleteActor(r WorkflowTaskCompleteRequest) error {
	actor := strings.TrimSpace(r.ActorKind)
	if actor != r.ActorKind {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "actor_kind", "actor_kind must not have leading or trailing whitespace")
	}
	switch actor {
	case WorkflowTaskCompleteActorAgent:
		if r.Force {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "force", "force is not allowed for agent completion")
		}
		return nil
	case WorkflowTaskCompleteActorUser:
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "actor_kind", "actor_kind must be agent or user")
	}
}

func workflowTaskCompleteSelectorCount(r WorkflowTaskCompleteRequest) int {
	count := 0
	for _, value := range []string{r.SessionID, r.TaskID} {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func (r WorkflowAttentionListRequest) Validate() error {
	if r.PageSize < 0 {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "page_size", Message: "page_size must be non-negative"}
	}
	if strings.TrimSpace(r.PageToken) != r.PageToken {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
	}
	return nil
}

func (r WorkflowTaskAttentionListRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func validateWorkflowApprovalDecision(decision clientui.ApprovalDecision) error {
	switch decision {
	case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
		return nil
	default:
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidValue, Field: "approval.decision", Message: "approval.decision is invalid"}
	}
}

func (r WorkflowTaskCommentAddRequest) Validate() error {
	if err := validateRequiredFields(requiredField("task_id", r.TaskID), requiredField("body", r.Body), requiredField("author", r.Author)); err != nil {
		return err
	}
	return validateWorkflowTaskCommentAuthorKind(r.Author)
}

func validateWorkflowTaskCommentAuthorKind(author string) error {
	switch strings.TrimSpace(author) {
	case "user", "agent":
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "author", "author must be user or agent")
	}
}

func (r WorkflowTaskCommentReplaceRequest) Validate() error {
	return validateRequiredFields(requiredField("comment_id", r.CommentID), requiredField("body", r.Body))
}

func (r WorkflowTaskCommentDeleteRequest) Validate() error {
	return validateRequired("comment_id", r.CommentID)
}

func (r WorkflowBoardRequest) Validate() error {
	if err := r.validateScope(); err != nil {
		return err
	}
	return r.LabelFilter.Validate()
}

func (r WorkflowBoardRequest) ValidateRPC() error {
	if err := r.validateScope(); err != nil {
		return err
	}
	return r.LabelFilter.ValidateRPC()
}

func (r WorkflowBoardRequest) validateScope() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if err := validateOptionalWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	return nil
}

func (r WorkflowTaskListRequest) Validate() error {
	if err := r.validateBeforeLabelFilter(); err != nil {
		return err
	}
	if err := r.LabelFilter.Validate(); err != nil {
		return err
	}
	return r.validateAfterLabelFilter()
}

func (r WorkflowTaskListRequest) ValidateRPC() error {
	if err := r.validateBeforeLabelFilter(); err != nil {
		return err
	}
	if err := r.LabelFilter.ValidateRPC(); err != nil {
		return err
	}
	return r.validateAfterLabelFilter()
}

func (r WorkflowTaskListRequest) validateBeforeLabelFilter() error {
	if r.ProjectID == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "project_id", "project_id is required")
	}
	for _, scope := range []struct {
		field string
		value *string
	}{
		{field: "project_id", value: r.ProjectID},
	} {
		if err := validateOptionalNonBlank(scope.field, scope.value); err != nil {
			return err
		}
	}
	if err := validateOptionalWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	if _, err := ResolveWorkflowOffsetWindow(r.Offset, r.Limit); err != nil {
		return err
	}
	return nil
}

func (r WorkflowTaskListRequest) validateAfterLabelFilter() error {
	if len(r.Sort) > WorkflowTaskListMaxSortSelectors {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "sort", fmt.Sprintf("sort must include at most %d fields", WorkflowTaskListMaxSortSelectors))
	}
	for index, columnKey := range r.ColumnKeys {
		if !workflowkey.Valid(columnKey) {
			return workflowRequestError(WorkflowRequestErrorInvalidKey, fmt.Sprintf("column_keys[%d]", index), fmt.Sprintf("column_keys[%d] must %s", index, workflowkey.Description))
		}
	}
	for index, status := range r.StatusKinds {
		if _, valid := status.NativeState(); !valid {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("status_kinds[%d]", index), "status kind is invalid")
		}
	}
	if r.Group != nil {
		if r.Group.StatusKinds() == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "group", "group is invalid")
		}
		if len(r.StatusKinds) > 0 {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "group", "group cannot be combined with status_kinds")
		}
	}
	for index, attention := range r.AttentionKinds {
		switch attention {
		case WorkflowTaskAttentionKindQuestion, WorkflowTaskAttentionKindApproval, WorkflowTaskAttentionKindInterrupted:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("attention_kinds[%d]", index), "attention kind is invalid")
		}
	}
	seenSortFields := map[WorkflowTaskListSortField]bool{}
	for index, sortSelector := range r.Sort {
		switch sortSelector.Field {
		case WorkflowTaskListSortFieldCreated, WorkflowTaskListSortFieldUpdated, WorkflowTaskListSortFieldStatus, WorkflowTaskListSortFieldColumn, WorkflowTaskListSortFieldTitle, WorkflowTaskListSortFieldLabels, WorkflowTaskListSortFieldShortID:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("sort[%d].field", index), "sort field must be created, updated, status, column, title, labels, or short_id")
		}
		if seenSortFields[sortSelector.Field] {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("sort[%d].field", index), "sort field must not be duplicated")
		}
		seenSortFields[sortSelector.Field] = true
		switch sortSelector.Direction {
		case WorkflowTaskListSortDirectionAsc, WorkflowTaskListSortDirectionDesc:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("sort[%d].direction", index), "sort direction must be asc or desc")
		}
	}
	return nil
}

func (r WorkflowBoardNodeCardsListRequest) Validate() error {
	if err := r.validateScopeAndPage(); err != nil {
		return err
	}
	return r.LabelFilter.Validate()
}

func (r WorkflowBoardNodeCardsListRequest) ValidateRPC() error {
	if err := r.validateScopeAndPage(); err != nil {
		return err
	}
	return r.LabelFilter.ValidateRPC()
}

func (r WorkflowBoardNodeCardsListRequest) validateScopeAndPage() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if err := validateRequiredWorkflowID(r.WorkflowID); err != nil {
		return err
	}
	if err := validateRequired("node_id", r.NodeID); err != nil {
		return err
	}
	if err := validateGraphEntityID("node_id", r.NodeID); err != nil {
		return err
	}
	if r.PageSize < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", "page_size must be non-negative")
	}
	if r.PageSize > WorkflowBoardNodeCardsMaxPageSize {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", fmt.Sprintf("page_size must be <= %d", WorkflowBoardNodeCardsMaxPageSize))
	}
	if r.Offset != nil && *r.Offset < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "offset", "offset must be non-negative")
	}
	if r.Sort != nil {
		switch r.Sort.Field {
		case WorkflowTaskListSortFieldCreated, WorkflowTaskListSortFieldUpdated, WorkflowTaskListSortFieldLabels, WorkflowTaskListSortFieldShortID:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "sort.field", "sort field must be created, updated, labels, or short_id")
		}
		switch r.Sort.Direction {
		case WorkflowTaskListSortDirectionAsc, WorkflowTaskListSortDirectionDesc:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "sort.direction", "sort direction must be asc or desc")
		}
	}
	return nil
}

func (r WorkflowProjectSubscribeRequest) Validate() error {
	if strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "project_id", "project_id must not have leading or trailing whitespace")
	}
	return nil
}

func (r WorkflowSubscribeRequest) Validate() error {
	return validateRequiredWorkflowID(r.WorkflowID)
}

func (r WorkflowTaskGetRequest) Validate() error {
	taskID := strings.TrimSpace(r.TaskID)
	projectID := strings.TrimSpace(r.ProjectID)
	shortID := strings.TrimSpace(r.ShortID)
	if r.TaskID != "" && taskID != r.TaskID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "task_id", "task_id must not have leading or trailing whitespace")
	}
	if r.ProjectID != "" && projectID != r.ProjectID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "project_id", "project_id must not have leading or trailing whitespace")
	}
	if r.ShortID != "" && shortID != r.ShortID {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "short_id", "short_id must not have leading or trailing whitespace")
	}
	if taskID != "" {
		return nil
	}
	if projectID == "" && shortID == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, "task_id", "task_id or short_id is required")
	}
	if shortID == "" {
		return validateRequired("short_id", r.ShortID)
	}
	return nil
}

func validateRequiredWorkflowID(value runtimeids.WorkflowID) error {
	if value.IsZero() {
		return workflowRequestError(WorkflowRequestErrorRequired, "workflow_id", "workflow_id is required")
	}
	return nil
}

func validateOptionalWorkflowID(value *runtimeids.WorkflowID) error {
	if value == nil {
		return nil
	}
	return validateRequiredWorkflowID(*value)
}

func validateRequired(name string, value string) error {
	if strings.TrimSpace(value) == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, name, name+" is required")
	}
	return nil
}

func validateTaskID(field string, value string) error {
	if strings.TrimSpace(value) == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, field, field+" is required")
	}
	if _, err := runtimeids.ParseTaskID(value); err != nil {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, field, err.Error())
	}
	return nil
}

func validateOptionalNonBlank(name string, value *string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, name, name+" must not be blank")
	}
	if strings.TrimSpace(*value) != *value {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, name, name+" must not have leading or trailing whitespace")
	}
	return nil
}

type requiredWorkflowField struct {
	name  string
	value string
}

func requiredField(name string, value string) requiredWorkflowField {
	return requiredWorkflowField{name: name, value: value}
}

func validateRequiredFields(fields ...requiredWorkflowField) error {
	for _, field := range fields {
		if err := validateRequired(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowProjectLinkDefaultMode(mode WorkflowProjectLinkDefaultMode) error {
	switch mode {
	case "", WorkflowProjectLinkDefaultNever, WorkflowProjectLinkDefaultAlways, WorkflowProjectLinkDefaultIfProjectHasNone:
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "default_policy", "default_policy must be never, always, or if_project_has_none")
	}
}

func validateDisplayName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, "display_name", "display_name is required")
	}
	if len([]rune(trimmed)) > 120 {
		return workflowRequestError(WorkflowRequestErrorTooLong, "display_name", "display_name must be <= 120 characters")
	}
	return nil
}

func validateModelKey(name string, value string) error {
	if !workflowkey.Valid(value) {
		return workflowRequestError(WorkflowRequestErrorInvalidKey, name, fmt.Sprintf("%s must %s", name, workflowkey.Description))
	}
	return nil
}

func workflowRequestError(code string, field string, message string) error {
	return WorkflowRequestValidationError{Code: code, Field: field, Message: message}
}
