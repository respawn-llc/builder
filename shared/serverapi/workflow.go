package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/workflowkey"
)

const (
	WorkflowRequestErrorRequired     = "workflow.request.required"
	WorkflowRequestErrorInvalidKey   = "workflow.request.invalid_key"
	WorkflowRequestErrorInvalidValue = "workflow.request.invalid_value"
	WorkflowRequestErrorInvalidMode  = "workflow.request.invalid_mode"
	WorkflowRequestErrorTooLong      = "workflow.request.too_long"
)

const WorkflowListMaxPageSize = 100
const WorkflowTaskListMaxPageSize = 100
const WorkflowTaskListMaxSortSelectors = 5
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
	ID                    string                               `json:"id"`
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
	WorkflowID         string                      `json:"workflow_id"`
	Key                string                      `json:"key"`
	Kind               string                      `json:"kind"`
	DisplayName        string                      `json:"display_name"`
	GroupID            string                      `json:"group_id,omitempty"`
	GroupKey           string                      `json:"group_key,omitempty"`
	SubagentRole       string                      `json:"subagent_role,omitempty"`
	PromptTemplate     string                      `json:"prompt_template,omitempty"`
	CompletionMode     string                      `json:"completion_mode,omitempty"`
	ScriptPath         *string                     `json:"script_path,omitempty"`
	InputFields        []WorkflowInputField        `json:"input_fields,omitempty"`
	JoinInputProviders []WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
	OutputFields       []WorkflowOutputField       `json:"output_fields,omitempty"`
}

type WorkflowNodeGroup struct {
	GroupID     string `json:"group_id"`
	WorkflowID  string `json:"workflow_id"`
	GroupKey    string `json:"group_key"`
	DisplayName string `json:"display_name"`
	SortOrder   int    `json:"sort_order"`
}

type WorkflowTransitionGroup struct {
	ID           string `json:"id"`
	WorkflowID   string `json:"workflow_id"`
	SourceNodeID string `json:"source_node_id"`
	TransitionID string `json:"transition_id"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description,omitempty"`
}

type WorkflowEdge struct {
	ID                 string                      `json:"id"`
	WorkflowID         string                      `json:"workflow_id"`
	TransitionGroupID  string                      `json:"transition_group_id"`
	Key                string                      `json:"key"`
	TargetNodeID       string                      `json:"target_node_id"`
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
// output snapshots, board summaries, and derived provision fields; writable graph contracts use
// WorkflowInputField on consuming nodes instead of user-authored source output fields.
type WorkflowOutputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type WorkflowInputField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type WorkflowParameter struct {
	Key         string `json:"key"`
	Description string `json:"description"`
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
	EdgeID                  string                 `json:"edge_id"`
	InputBindings           []WorkflowInputBinding `json:"input_bindings,omitempty"`
	RequiredProvisionFields []WorkflowOutputField  `json:"required_provision_fields,omitempty"`
	RequiredProviderFields  []WorkflowOutputField  `json:"required_provider_fields,omitempty"`
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
	GroupID            string                      `json:"group_id,omitempty"`
	GroupKey           string                      `json:"group_key,omitempty"`
	SubagentRole       string                      `json:"subagent_role,omitempty"`
	PromptTemplate     string                      `json:"prompt_template,omitempty"`
	CompletionMode     string                      `json:"completion_mode,omitempty"`
	ScriptPath         *string                     `json:"script_path,omitempty"`
	InputFields        []WorkflowInputField        `json:"input_fields,omitempty"`
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
	RequiresApproval  bool                  `json:"requires_approval"`
	ContextMode       string                `json:"context_mode"`
	ContextSource     WorkflowContextSource `json:"context_source"`
	PromptTemplate    string                `json:"prompt_template,omitempty"`
	Parameters        []WorkflowParameter   `json:"parameters,omitempty"`
}

type WorkflowGraphValidateDraftRequest struct {
	WorkflowID string                   `json:"workflow_id"`
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
	WorkflowID string             `json:"workflow_id"`
	Graph      WorkflowGraphDraft `json:"graph"`
}

type WorkflowGraphDeriveWiringResponse struct {
	DerivedWiring WorkflowDerivedWiring `json:"derived_wiring"`
}

type WorkflowGraphSavePreviewRequest struct {
	WorkflowID      string                 `json:"workflow_id"`
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
	ExpectedRemovedNodeCount            int64 `json:"expected_removed_node_count"`
	ExpectedRemovedTransitionGroupCount int64 `json:"expected_removed_transition_group_count"`
	ExpectedRemovedEdgeCount            int64 `json:"expected_removed_edge_count"`
	ExpectedNodeTaskReferenceCount      int64 `json:"expected_node_task_reference_count"`
	ExpectedEdgeTaskReferenceCount      int64 `json:"expected_edge_task_reference_count"`
}

type WorkflowGraphSaveRequest struct {
	WorkflowID      string                         `json:"workflow_id"`
	ExpectedVersion int64                          `json:"expected_version"`
	Metadata        *WorkflowGraphMetadata         `json:"metadata,omitempty"`
	Graph           WorkflowGraphDraft             `json:"graph"`
	Confirmation    *WorkflowGraphSaveConfirmation `json:"confirmation,omitempty"`
}

type WorkflowGraphSavePreviewResponse struct {
	CurrentVersion       int64                                               `json:"current_version"`
	ValidationResults    map[WorkflowValidationMode]WorkflowValidateResponse `json:"validation_results"`
	Impact               WorkflowGraphSaveImpact                             `json:"impact"`
	Blockers             []WorkflowGraphSaveBlocker                          `json:"blockers,omitempty"`
	CanSave              bool                                                `json:"can_save"`
	ConfirmationRequired bool                                                `json:"confirmation_required"`
}

type WorkflowGraphSaveResponse struct {
	Saved                bool                                                `json:"saved"`
	Definition           *WorkflowDefinition                                 `json:"definition,omitempty"`
	CurrentVersion       int64                                               `json:"current_version"`
	ValidationResults    map[WorkflowValidationMode]WorkflowValidateResponse `json:"validation_results"`
	Impact               WorkflowGraphSaveImpact                             `json:"impact"`
	Blockers             []WorkflowGraphSaveBlocker                          `json:"blockers,omitempty"`
	CanSave              bool                                                `json:"can_save"`
	ConfirmationRequired bool                                                `json:"confirmation_required"`
}

type WorkflowGraphSaveImpact struct {
	RemovedNodeCount                  int64 `json:"removed_node_count"`
	RemovedTransitionGroupCount       int64 `json:"removed_transition_group_count"`
	RemovedEdgeCount                  int64 `json:"removed_edge_count"`
	NodeTaskReferenceCount            int64 `json:"node_task_reference_count"`
	EdgeTaskReferenceCount            int64 `json:"edge_task_reference_count"`
	ActiveNodePlacementCount          int64 `json:"active_node_placement_count"`
	PendingApprovalCount              int64 `json:"pending_approval_count"`
	ActiveRunCount                    int64 `json:"active_run_count"`
	RunnableRunCount                  int64 `json:"runnable_run_count"`
	StartNodeChangeCount              int64 `json:"start_node_change_count"`
	LastTerminalChangeCount           int64 `json:"last_terminal_change_count"`
	TaskReferencedNodeKindChangeCount int64 `json:"task_referenced_node_kind_change_count"`
}

type WorkflowGraphSaveBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int64  `json:"count"`
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
	WorkflowID  string `json:"workflow_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type WorkflowListRequest struct {
	PageSize   int     `json:"page_size,omitempty"`
	PageToken  string  `json:"page_token,omitempty"`
	Query      string  `json:"query,omitempty"`
	ProjectID  *string `json:"project_id,omitempty"`
	WorkflowID *string `json:"workflow_id,omitempty"`
}

type WorkflowListResponse struct {
	Workflows     []WorkflowRecord `json:"workflows"`
	ProjectID     *string          `json:"project_id,omitempty"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

type WorkflowGetRequest struct {
	WorkflowID string `json:"workflow_id"`
}

type WorkflowGetResponse struct {
	Definition WorkflowDefinition `json:"definition"`
}

type WorkflowNodeAddRequest struct {
	WorkflowID         string                      `json:"workflow_id"`
	NodeID             string                      `json:"node_id,omitempty"`
	Key                string                      `json:"key"`
	Kind               string                      `json:"kind"`
	DisplayName        string                      `json:"display_name"`
	GroupKey           string                      `json:"group_key,omitempty"`
	SubagentRole       string                      `json:"subagent_role,omitempty"`
	PromptTemplate     string                      `json:"prompt_template,omitempty"`
	CompletionMode     string                      `json:"completion_mode,omitempty"`
	ScriptPath         *string                     `json:"script_path,omitempty"`
	InputFields        []WorkflowInputField        `json:"input_fields,omitempty"`
	JoinInputProviders []WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
}

type WorkflowNodeAddResponse struct {
	Version int64 `json:"version"`
}

type WorkflowNodeUpdateRequest struct {
	WorkflowID         string                      `json:"workflow_id"`
	NodeID             string                      `json:"node_id"`
	Key                string                      `json:"key"`
	Kind               string                      `json:"kind"`
	DisplayName        string                      `json:"display_name"`
	GroupKey           string                      `json:"group_key,omitempty"`
	SubagentRole       string                      `json:"subagent_role,omitempty"`
	PromptTemplate     string                      `json:"prompt_template,omitempty"`
	CompletionMode     string                      `json:"completion_mode,omitempty"`
	ScriptPath         *string                     `json:"script_path,omitempty"`
	InputFields        []WorkflowInputField        `json:"input_fields,omitempty"`
	JoinInputProviders []WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
}

type WorkflowNodeUpdateResponse struct {
	Version int64 `json:"version"`
}

type WorkflowNodeGroupAddRequest struct {
	WorkflowID  string `json:"workflow_id"`
	GroupID     string `json:"group_id,omitempty"`
	GroupKey    string `json:"group_key"`
	DisplayName string `json:"display_name"`
	SortOrder   int    `json:"sort_order"`
}

type WorkflowNodeGroupUpdateRequest struct {
	WorkflowID  string `json:"workflow_id"`
	GroupID     string `json:"group_id"`
	GroupKey    string `json:"group_key"`
	DisplayName string `json:"display_name"`
	SortOrder   int    `json:"sort_order"`
}

type WorkflowNodeGroupDeleteRequest struct {
	WorkflowID string `json:"workflow_id"`
	GroupID    string `json:"group_id"`
}

type WorkflowNodeGroupResponse struct {
	Group   WorkflowNodeGroup `json:"group"`
	Version int64             `json:"version"`
}

type WorkflowTransitionGroupAddRequest struct {
	WorkflowID   string `json:"workflow_id"`
	GroupID      string `json:"group_id,omitempty"`
	SourceNodeID string `json:"source_node_id"`
	TransitionID string `json:"transition_id"`
	DisplayName  string `json:"display_name,omitempty"`
	Description  string `json:"description,omitempty"`
}

type WorkflowTransitionGroupAddResponse struct {
	Version int64 `json:"version"`
}

type WorkflowTransitionGroupUpdateRequest struct {
	WorkflowID   string `json:"workflow_id"`
	GroupID      string `json:"group_id"`
	SourceNodeID string `json:"source_node_id"`
	TransitionID string `json:"transition_id"`
	DisplayName  string `json:"display_name,omitempty"`
	Description  string `json:"description,omitempty"`
}

type WorkflowTransitionGroupUpdateResponse struct {
	Version int64 `json:"version"`
}

type WorkflowEdgeAddRequest struct {
	WorkflowID        string                `json:"workflow_id"`
	EdgeID            string                `json:"edge_id,omitempty"`
	TransitionGroupID string                `json:"transition_group_id"`
	Key               string                `json:"key"`
	TargetNodeID      string                `json:"target_node_id"`
	ContextMode       string                `json:"context_mode"`
	ContextSource     WorkflowContextSource `json:"context_source"`
	RequiresApproval  bool                  `json:"requires_approval"`
	PromptTemplate    string                `json:"prompt_template,omitempty"`
	Parameters        []WorkflowParameter   `json:"parameters,omitempty"`
}

type WorkflowEdgeAddResponse struct {
	Version int64 `json:"version"`
}

type WorkflowEdgeUpdateRequest struct {
	WorkflowID        string                `json:"workflow_id"`
	EdgeID            string                `json:"edge_id"`
	TransitionGroupID string                `json:"transition_group_id"`
	Key               string                `json:"key"`
	TargetNodeID      string                `json:"target_node_id"`
	ContextMode       string                `json:"context_mode"`
	ContextSource     WorkflowContextSource `json:"context_source"`
	RequiresApproval  bool                  `json:"requires_approval"`
	PromptTemplate    string                `json:"prompt_template,omitempty"`
	Parameters        []WorkflowParameter   `json:"parameters,omitempty"`
}

type WorkflowEdgeUpdateResponse struct {
	Version int64 `json:"version"`
}

type WorkflowLinkProjectRequest struct {
	ProjectID     string                         `json:"project_id"`
	WorkflowID    string                         `json:"workflow_id"`
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
	ProjectID  string `json:"project_id"`
	WorkflowID string `json:"workflow_id"`
}

type WorkflowSetDefaultProjectLinkResponse struct {
	Link ProjectWorkflowLink `json:"link"`
}

type ProjectWorkflowLink struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	WorkflowID string `json:"workflow_id"`
	Default    bool   `json:"default"`
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
	WorkflowID string `json:"workflow_id"`
}

type WorkflowDeletePreviewResponse struct {
	Impact WorkflowDeleteImpact `json:"impact"`
}

type WorkflowDeleteRequest struct {
	WorkflowID           string `json:"workflow_id"`
	Confirmed            bool   `json:"confirmed"`
	ExpectedVersion      int64  `json:"expected_version"`
	ExpectedProjectCount int64  `json:"expected_project_count"`
	ExpectedLinkCount    int64  `json:"expected_link_count"`
	ExpectedTaskCount    int64  `json:"expected_task_count"`
	CleanupArtifacts     bool   `json:"cleanup_artifacts,omitempty"`
}

type WorkflowDeleteResponse struct {
	Deleted  bool                    `json:"deleted"`
	Impact   WorkflowDeleteImpact    `json:"impact"`
	Blockers []WorkflowDeleteBlocker `json:"blockers,omitempty"`
}

type WorkflowDeleteImpact struct {
	WorkflowID                     string `json:"workflow_id"`
	Version                        int64  `json:"version"`
	ProjectCount                   int64  `json:"project_count"`
	LinkCount                      int64  `json:"link_count"`
	DefaultReplacementProjectCount int64  `json:"default_replacement_project_count"`
	TaskCount                      int64  `json:"task_count"`
	ActiveRunCount                 int64  `json:"active_run_count"`
	RunnableRunCount               int64  `json:"runnable_run_count"`
	BlockedTaskCount               int64  `json:"blocked_task_count"`
}

type WorkflowDeleteBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

type WorkflowValidateRequest struct {
	WorkflowID string                 `json:"workflow_id"`
	Mode       WorkflowValidationMode `json:"mode"`
}

type WorkflowScriptPathValidateRequest struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	ScriptPath string `json:"script_path"`
}

type WorkflowValidateResponse struct {
	Valid  bool                      `json:"valid"`
	Errors []WorkflowValidationError `json:"errors"`
}

type WorkflowValidationError struct {
	Code              string                          `json:"code"`
	Message           string                          `json:"message"`
	WorkflowID        *string                         `json:"workflow_id,omitempty"`
	NodeID            string                          `json:"node_id,omitempty"`
	TransitionGroupID string                          `json:"transition_group_id,omitempty"`
	EdgeID            string                          `json:"edge_id,omitempty"`
	Details           *WorkflowValidationErrorDetails `json:"details,omitempty"`
	RelatedIDs        []string                        `json:"related_ids,omitempty"`
	BlocksContext     bool                            `json:"blocks_context"`
}

type WorkflowValidationErrorDetails struct {
	FieldName      string  `json:"field_name,omitempty"`
	InputName      string  `json:"input_name,omitempty"`
	Placeholder    string  `json:"placeholder,omitempty"`
	ProviderEdgeID string  `json:"provider_edge_id,omitempty"`
	Role           *string `json:"role,omitempty"`
	RequiredTool   *string `json:"required_tool,omitempty"`
}

type WorkflowTaskCreateRequest struct {
	ProjectID         string   `json:"project_id"`
	WorkflowID        *string  `json:"workflow_id,omitempty"`
	Title             string   `json:"title"`
	Body              string   `json:"body,omitempty"`
	SourceURL         string   `json:"source_url,omitempty"`
	SourceWorkspaceID string   `json:"source_workspace_id,omitempty"`
	LabelIDs          []string `json:"label_ids"`
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
	WorkflowID *string
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
		WorkflowID *string                           `json:"workflow_id,omitempty"`
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
		WorkflowID *string                           `json:"workflow_id,omitempty"`
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

func validWorkflowTaskCreateSelectionError(reason WorkflowTaskCreateSelectionReason, workflowID *string) bool {
	switch reason {
	case WorkflowTaskCreateSelectionReasonNoLinkedWorkflows,
		WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault:
		return workflowID == nil
	case WorkflowTaskCreateSelectionReasonWorkflowNotLinked:
		if workflowID == nil {
			return false
		}
		_, err := runtimeids.ParseCanonicalPrefixedUUIDv4(*workflowID, "workflow-", "workflow id")
		return err == nil
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
	TaskID           string                            `json:"task_id"`
	SetupOperationID WorktreeSetupOperationID          `json:"setup_operation_id"`
	ExecutionTarget  *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
}

type WorkflowTaskStartResponse struct {
	Outcome           WorkflowExecutionTargetActionOutcome         `json:"outcome,omitempty"`
	Applied           *WorkflowTaskStartApplied                    `json:"applied,omitempty"`
	SelectionRequired *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
}

type WorkflowTaskStartApplied struct {
	TransitionID string `json:"transition_id"`
	PlacementID  string `json:"placement_id"`
	RunID        string `json:"run_id"`
}

type WorkflowTaskResumeRequest struct {
	TaskID string `json:"task_id"`
}

type WorkflowTaskRunSummary struct {
	PlacementID string `json:"placement_id"`
	NodeID      string `json:"node_id"`
	Generation  int64  `json:"generation"`
	SessionID   string `json:"session_id,omitempty"`
}

type WorkflowTaskResumeResponse struct {
	Runs []WorkflowTaskRunSummary `json:"runs"`
}

type WorkflowTaskApproveRequest struct {
	TaskTransitionID string                            `json:"task_transition_id,omitempty"`
	TransitionID     string                            `json:"transition_id,omitempty"`
	SetupOperationID WorktreeSetupOperationID          `json:"setup_operation_id"`
	ExecutionTarget  *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
}

type WorkflowTaskApproveResponse struct {
	Outcome           WorkflowExecutionTargetActionOutcome         `json:"outcome,omitempty"`
	Applied           *WorkflowTaskApproveApplied                  `json:"applied,omitempty"`
	SelectionRequired *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
}

type WorkflowTaskApproveApplied struct {
	TransitionID string   `json:"transition_id"`
	TaskID       string   `json:"task_id"`
	State        string   `json:"state"`
	PlacementIDs []string `json:"placement_ids,omitempty"`
	RunIDs       []string `json:"run_ids,omitempty"`
}

type WorkflowTaskMoveRequest struct {
	TaskID           string                            `json:"task_id"`
	TargetNodeID     string                            `json:"target_node_id"`
	OutputValues     map[string]string                 `json:"output_values,omitempty"`
	Commentary       string                            `json:"commentary,omitempty"`
	AllowMissingEdge bool                              `json:"allow_missing_edge,omitempty"`
	AutoApprove      bool                              `json:"auto_approve,omitempty"`
	SetupOperationID WorktreeSetupOperationID          `json:"setup_operation_id"`
	ExecutionTarget  *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
}

type WorkflowTaskMoveResponse struct {
	Outcome           WorkflowExecutionTargetActionOutcome         `json:"outcome,omitempty"`
	Applied           *WorkflowTaskMoveApplied                     `json:"applied,omitempty"`
	SelectionRequired *WorkflowExecutionTargetSelectionRequirement `json:"selection_required,omitempty"`
}

type WorkflowTaskMoveApplied struct {
	TransitionID string   `json:"transition_id"`
	State        string   `json:"state"`
	PlacementIDs []string `json:"placement_ids,omitempty"`
	RunIDs       []string `json:"run_ids,omitempty"`
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
	RunID          string            `json:"run_id,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
	TaskID         string            `json:"task_id,omitempty"`
	ProjectID      string            `json:"project_id,omitempty"`
	ShortID        string            `json:"short_id,omitempty"`
	TransitionID   string            `json:"transition_id,omitempty"`
	OutputValues   map[string]string `json:"output_values,omitempty"`
	Commentary     string            `json:"commentary,omitempty"`
	ActorKind      string            `json:"actor_kind"`
	AgentSessionID string            `json:"agent_session_id,omitempty"`
	Force          bool              `json:"force,omitempty"`
}

type WorkflowTaskCompleteResponse struct {
	TransitionID string                        `json:"transition_id"`
	TaskID       string                        `json:"task_id"`
	RunID        string                        `json:"run_id"`
	State        string                        `json:"state"`
	PlacementIDs []string                      `json:"placement_ids,omitempty"`
	RunIDs       []string                      `json:"run_ids,omitempty"`
	Handoff      WorkflowTaskCompletionHandoff `json:"handoff"`
}

type WorkflowTaskCompletionHandoff struct {
	SourceNodeDisplayName  string `json:"source_node_display_name"`
	DestinationDisplayName string `json:"destination_display_name"`
}

type WorkflowTaskCancelRequest struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason,omitempty"`
}

type WorkflowTaskDeleteRequest struct {
	TaskID string `json:"task_id"`
}

type WorkflowTaskInterruptRequest struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type WorkflowTaskInterruptResponse struct {
	Runs []WorkflowTaskRunSummary `json:"runs"`
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
	ID                     string                             `json:"id"`
	Kind                   string                             `json:"kind"`
	ProjectID              string                             `json:"project_id,omitempty"`
	WorkflowID             *string                            `json:"workflow_id,omitempty"`
	TaskID                 string                             `json:"task_id,omitempty"`
	TaskShortID            string                             `json:"task_short_id,omitempty"`
	TaskTitle              string                             `json:"task_title,omitempty"`
	RunID                  *string                            `json:"run_id,omitempty"`
	SessionID              *string                            `json:"session_id,omitempty"`
	AskID                  *string                            `json:"ask_id,omitempty"`
	TaskTransitionID       *string                            `json:"task_transition_id,omitempty"`
	Message                string                             `json:"message"`
	DetailJSON             *string                            `json:"detail_json,omitempty"`
	Suggestions            []string                           `json:"suggestions,omitempty"`
	RecommendedOptionIndex *int                               `json:"recommended_option_index,omitempty"`
	Question               *WorkflowAttentionQuestionPrompt   `json:"question,omitempty"`
	ApprovalSnapshot       *WorkflowAttentionApprovalSnapshot `json:"approval_snapshot,omitempty"`
	OccurredAtUnixMs       int64                              `json:"occurred_at_unix_ms"`
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
	Kind                   WorkflowAttentionQuestionKind `json:"kind"`
	Suggestions            []string                      `json:"suggestions,omitempty"`
	RecommendedOptionIndex *int                          `json:"recommended_option_index,omitempty"`
	ApprovalDecisions      []clientui.ApprovalDecision   `json:"approval_decisions,omitempty"`
}

type WorkflowTaskQuestionApprovalAnswer struct {
	Decision   clientui.ApprovalDecision `json:"decision"`
	Commentary string                    `json:"commentary,omitempty"`
}

type WorkflowTaskQuestionAnswerRequest struct {
	ClientRequestID      string                              `json:"client_request_id"`
	TaskID               string                              `json:"task_id"`
	RunID                string                              `json:"run_id,omitempty"`
	AskID                string                              `json:"ask_id"`
	ErrorMessage         string                              `json:"error_message,omitempty"`
	Answer               string                              `json:"answer,omitempty"`
	SelectedOptionNumber *int                                `json:"selected_option_number"`
	FreeformAnswer       string                              `json:"freeform_answer,omitempty"`
	Approval             *WorkflowTaskQuestionApprovalAnswer `json:"approval,omitempty"`
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

type WorkflowTaskCommentListRequest struct {
	TaskID    string `json:"task_id"`
	PageSize  int    `json:"page_size,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

type WorkflowTaskCommentListResponse struct {
	Comments      []WorkflowTaskComment `json:"comments"`
	NextPageToken string                `json:"next_page_token,omitempty"`
}

type WorkflowTaskCommentReplaceRequest struct {
	CommentID string `json:"comment_id"`
	Body      string `json:"body"`
}

type WorkflowTaskCommentDeleteRequest struct {
	CommentID string `json:"comment_id"`
}

type WorkflowBoardRequest struct {
	ProjectID   string                  `json:"project_id"`
	WorkflowID  *string                 `json:"workflow_id,omitempty"`
	LabelFilter WorkflowTaskLabelFilter `json:"label_filter"`
}

type WorkflowTaskStatusKind string

const (
	WorkflowTaskStatusKindCanceled        WorkflowTaskStatusKind = "canceled"
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
	WorkflowTaskNativeStateCanceled        WorkflowTaskNativeState = "canceled"
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
	case WorkflowTaskStatusKindCanceled:
		return WorkflowTaskNativeStateCanceled, true
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
	WorkflowTaskListSortFieldCreated  WorkflowTaskListSortField = "created"
	WorkflowTaskListSortFieldUpdated  WorkflowTaskListSortField = "updated"
	WorkflowTaskListSortFieldStatus   WorkflowTaskListSortField = "status"
	WorkflowTaskListSortFieldColumn   WorkflowTaskListSortField = "column"
	WorkflowTaskListSortFieldRunCount WorkflowTaskListSortField = "run_count"
	WorkflowTaskListSortFieldTitle    WorkflowTaskListSortField = "title"
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
	ProjectID      *string                     `json:"project_id,omitempty"`
	WorkflowID     *string                     `json:"workflow_id,omitempty"`
	ColumnKeys     []string                    `json:"column_keys,omitempty"`
	StatusKinds    []WorkflowTaskStatusKind    `json:"status_kinds,omitempty"`
	AttentionKinds []WorkflowTaskAttentionKind `json:"attention_kinds,omitempty"`
	LabelFilter    WorkflowTaskLabelFilter     `json:"label_filter"`
	Sort           []WorkflowTaskListSort      `json:"sort,omitempty"`
	PageSize       int                         `json:"page_size"`
	PageToken      string                      `json:"page_token,omitempty"`
}

type WorkflowTaskListScope struct {
	ProjectID  string  `json:"project_id"`
	WorkflowID *string `json:"workflow_id,omitempty"`
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
	NextPageToken               *string                                     `json:"next_page_token,omitempty"`
	GeneratedAtUnixMs           int64                                       `json:"generated_at_unix_ms"`
	Tasks                       []WorkflowTaskListItem                      `json:"tasks"`
}

type WorkflowTaskListItem struct {
	TaskID          string             `json:"task_id"`
	ShortID         string             `json:"short_id"`
	WorkflowID      string             `json:"workflow_id"`
	WorkflowName    *string            `json:"workflow_name,omitempty"`
	Title           string             `json:"title"`
	CreatedAtUnixMs int64              `json:"created_at_unix_ms"`
	UpdatedAtUnixMs int64              `json:"updated_at_unix_ms"`
	ColumnKeys      *[]string          `json:"column_keys,omitempty"`
	Status          WorkflowTaskStatus `json:"status"`
	RunCount        int                `json:"run_count"`
	LabelIDs        []string           `json:"label_ids"`
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
	WorkflowID *string
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
		WorkflowID *string                          `json:"workflow_id,omitempty"`
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
		WorkflowID *string                          `json:"workflow_id,omitempty"`
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
		if _, err := runtimeids.ParseCanonicalPrefixedUUIDv4(*e.WorkflowID, "workflow-", "workflow id"); err != nil {
			return err
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
	ProjectID   string                  `json:"project_id"`
	WorkflowID  string                  `json:"workflow_id"`
	NodeID      string                  `json:"node_id"`
	LabelFilter WorkflowTaskLabelFilter `json:"label_filter"`
	PageSize    int                     `json:"page_size"`
	PageToken   *string                 `json:"page_token"`
}

type WorkflowBoardNodeCardsListResponse struct {
	ProjectID         string                  `json:"project_id"`
	WorkflowID        string                  `json:"workflow_id"`
	NodeID            string                  `json:"node_id"`
	Cards             []WorkflowBoardTaskCard `json:"cards"`
	PreviousPageToken *string                 `json:"previous_page_token"`
	NextPageToken     *string                 `json:"next_page_token"`
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
	WorkflowID           string                    `json:"workflow_id"`
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
	GroupID   string                   `json:"group_id,omitempty"`
	SortOrder int                      `json:"sort_order"`
	IsBacklog bool                     `json:"is_backlog"`
	IsDone    bool                     `json:"is_done"`
	TaskCount int                      `json:"task_count"`
}

type WorkflowBoardNodeSummary struct {
	NodeID                 string                `json:"node_id"`
	Key                    string                `json:"key"`
	Kind                   string                `json:"kind"`
	DisplayName            string                `json:"display_name"`
	AssigneeRole           string                `json:"assignee_role,omitempty"`
	SortOrder              int                   `json:"sort_order"`
	OutputFields           []WorkflowOutputField `json:"output_fields,omitempty"`
	TransitionOutputFields []WorkflowOutputField `json:"transition_output_fields,omitempty"`
}

type WorkflowBoardTaskCard struct {
	TaskID          string                  `json:"task_id"`
	ShortID         string                  `json:"short_id"`
	Title           string                  `json:"title"`
	Preview         MarkdownPreview         `json:"preview"`
	WorkflowID      string                  `json:"workflow_id"`
	ActiveNodeIDs   []string                `json:"active_node_ids,omitempty"`
	SourceWorkspace ProjectWorkspaceSummary `json:"source_workspace"`
	Status          WorkflowTaskStatus      `json:"status"`
	Actions         WorkflowTaskActions     `json:"actions"`
	LabelIDs        []string                `json:"label_ids"`
	UpdatedAtUnixMs int64                   `json:"updated_at_unix_ms"`
}

type MarkdownPreview struct {
	Markdown  string `json:"markdown"`
	Truncated bool   `json:"truncated"`
}

type WorkflowTaskStatus struct {
	Kind           WorkflowTaskStatusKind      `json:"kind"`
	NativeState    WorkflowTaskNativeState     `json:"native_state"`
	NodeIDs        []string                    `json:"node_ids,omitempty"`
	RunIDs         []string                    `json:"run_ids,omitempty"`
	AttentionTypes []WorkflowTaskAttentionKind `json:"attention_types,omitempty"`
}

type WorkflowTaskActions struct {
	CanStart                bool     `json:"can_start"`
	CanInterrupt            bool     `json:"can_interrupt"`
	CanResume               bool     `json:"can_resume"`
	CanCancel               bool     `json:"can_cancel"`
	ManualMoveTargetNodeIDs []string `json:"manual_move_target_node_ids,omitempty"`
}

type WorkflowProjectSubscribeRequest struct {
	ProjectID string `json:"project_id,omitempty"`
}

type WorkflowSubscribeRequest struct {
	WorkflowID string `json:"workflow_id"`
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
	WorkflowProjectEventActionCreated                = protocol.WorkflowProjectEventActionCreated
	WorkflowProjectEventActionUpdated                = protocol.WorkflowProjectEventActionUpdated
	WorkflowProjectEventActionRenamed                = protocol.WorkflowProjectEventActionRenamed
	WorkflowProjectEventActionDeleted                = protocol.WorkflowProjectEventActionDeleted
	WorkflowProjectEventActionNodeAdded              = protocol.WorkflowProjectEventActionNodeAdded
	WorkflowProjectEventActionNodeUpdated            = protocol.WorkflowProjectEventActionNodeUpdated
	WorkflowProjectEventActionNodeGroupAdded         = protocol.WorkflowProjectEventActionNodeGroupAdded
	WorkflowProjectEventActionNodeGroupUpdated       = protocol.WorkflowProjectEventActionNodeGroupUpdated
	WorkflowProjectEventActionNodeGroupDeleted       = protocol.WorkflowProjectEventActionNodeGroupDeleted
	WorkflowProjectEventActionTransitionGroupAdded   = protocol.WorkflowProjectEventActionTransitionGroupAdded
	WorkflowProjectEventActionTransitionGroupUpdated = protocol.WorkflowProjectEventActionTransitionGroupUpdated
	WorkflowProjectEventActionEdgeAdded              = protocol.WorkflowProjectEventActionEdgeAdded
	WorkflowProjectEventActionEdgeUpdated            = protocol.WorkflowProjectEventActionEdgeUpdated
	WorkflowProjectEventActionGraphSaved             = protocol.WorkflowProjectEventActionGraphSaved
	WorkflowProjectEventActionLinked                 = protocol.WorkflowProjectEventActionLinked
	WorkflowProjectEventActionDefaultChanged         = protocol.WorkflowProjectEventActionDefaultChanged
	WorkflowProjectEventActionUnlinked               = protocol.WorkflowProjectEventActionUnlinked
	WorkflowProjectEventActionStarted                = protocol.WorkflowProjectEventActionStarted
	WorkflowProjectEventActionInterrupted            = protocol.WorkflowProjectEventActionInterrupted
	WorkflowProjectEventActionResumed                = protocol.WorkflowProjectEventActionResumed
	WorkflowProjectEventActionApproved               = protocol.WorkflowProjectEventActionApproved
	WorkflowProjectEventActionMoved                  = protocol.WorkflowProjectEventActionMoved
	WorkflowProjectEventActionCanceled               = protocol.WorkflowProjectEventActionCanceled
	WorkflowProjectEventActionCompleted              = protocol.WorkflowProjectEventActionCompleted
	WorkflowProjectEventActionCommentAdded           = protocol.WorkflowProjectEventActionCommentAdded
	WorkflowProjectEventActionCommentUpdated         = protocol.WorkflowProjectEventActionCommentUpdated
	WorkflowProjectEventActionCommentDeleted         = protocol.WorkflowProjectEventActionCommentDeleted
	WorkflowProjectEventActionQuestionWaiting        = protocol.WorkflowProjectEventActionQuestionWaiting
	WorkflowProjectEventActionQuestionCleared        = protocol.WorkflowProjectEventActionQuestionCleared
	WorkflowProjectEventActionQuestionAnswered       = protocol.WorkflowProjectEventActionQuestionAnswered
	WorkflowProjectEventActionLabelsChanged          = protocol.WorkflowProjectEventActionLabelsChanged
)

type WorkflowProjectEvent struct {
	ProjectID        *string                      `json:"project_id,omitempty"`
	WorkflowID       *string                      `json:"workflow_id,omitempty"`
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
		if err := validateRequired("workflow_id", *e.WorkflowID); err != nil {
			return err
		}
		if strings.TrimSpace(*e.WorkflowID) != *e.WorkflowID {
			return workflowRequestError(WorkflowRequestErrorInvalidMode, "workflow_id", "workflow_id must not have leading or trailing whitespace")
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
			WorkflowProjectEventActionNodeAdded,
			WorkflowProjectEventActionNodeUpdated,
			WorkflowProjectEventActionNodeGroupAdded,
			WorkflowProjectEventActionNodeGroupUpdated,
			WorkflowProjectEventActionNodeGroupDeleted,
			WorkflowProjectEventActionTransitionGroupAdded,
			WorkflowProjectEventActionTransitionGroupUpdated,
			WorkflowProjectEventActionEdgeAdded,
			WorkflowProjectEventActionEdgeUpdated,
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
			WorkflowProjectEventActionCanceled,
			WorkflowProjectEventActionCompleted,
			WorkflowProjectEventActionCommentAdded,
			WorkflowProjectEventActionCommentUpdated,
			WorkflowProjectEventActionCommentDeleted,
			WorkflowProjectEventActionQuestionWaiting,
			WorkflowProjectEventActionQuestionCleared,
			WorkflowProjectEventActionQuestionAnswered,
			WorkflowProjectEventActionLabelsChanged:
			return true
		}
	case WorkflowProjectEventResourceLabel:
		switch action {
		case WorkflowProjectEventActionCreated,
			WorkflowProjectEventActionRenamed,
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

type WorkflowTaskActivityListRequest struct {
	TaskID    string `json:"task_id"`
	PageSize  int    `json:"page_size,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

type WorkflowTaskActivityListResponse struct {
	Items             []WorkflowTaskActivityItem `json:"items"`
	NextPageToken     string                     `json:"next_page_token,omitempty"`
	GeneratedAtUnixMs int64                      `json:"generated_at_unix_ms"`
}

type WorkflowTaskSummary struct {
	ID                string   `json:"id"`
	ProjectID         string   `json:"project_id"`
	WorkflowID        string   `json:"workflow_id"`
	ShortID           string   `json:"short_id"`
	Title             string   `json:"title"`
	BodyPreview       string   `json:"body_preview,omitempty"`
	SourceWorkspaceID string   `json:"source_workspace_id,omitempty"`
	CanceledAt        *int64   `json:"canceled_at_unix_ms,omitempty"`
	CancelReason      *string  `json:"cancel_reason,omitempty"`
	CreatedAtUnixMs   int64    `json:"created_at_unix_ms"`
	UpdatedAtUnixMs   int64    `json:"updated_at_unix_ms"`
	Done              bool     `json:"done"`
	ActiveNodeIDs     []string `json:"active_node_ids,omitempty"`
}

type WorkflowTaskDetail struct {
	Summary           WorkflowTaskSummary         `json:"summary"`
	Project           ProjectBoardProject         `json:"project"`
	Workflow          WorkflowTaskWorkflowSummary `json:"workflow"`
	Body              string                      `json:"body"`
	SourceURL         string                      `json:"source_url,omitempty"`
	SourceWorkspace   ProjectWorkspaceSummary     `json:"source_workspace"`
	ExecutionTarget   *WorkflowExecutionTarget    `json:"execution_target,omitempty"`
	WorktreePath      *string                     `json:"worktree_path"`
	CurrentSessionIDs []string                    `json:"current_session_ids"`
	CurrentScripts    []WorkflowTaskCurrentScript `json:"current_scripts"`
	Status            WorkflowTaskStatus          `json:"status"`
	Actions           WorkflowTaskActions         `json:"actions"`
	LabelIDs          []string                    `json:"label_ids"`
	AttentionCount    int                         `json:"attention_count"`
}

type WorkflowTaskWorkflowSummary struct {
	WorkflowID  string `json:"workflow_id"`
	DisplayName string `json:"display_name"`
	Version     int64  `json:"version"`
}

type WorkflowTaskCurrentScript struct {
	RunID string `json:"run_id"`
	Path  string `json:"path"`
}

type WorkflowPlacement struct {
	ID                        string `json:"id"`
	TaskID                    string `json:"task_id"`
	NodeID                    string `json:"node_id"`
	NodeKey                   string `json:"node_key,omitempty"`
	NodeDisplayName           string `json:"node_display_name,omitempty"`
	NodeKind                  string `json:"node_kind,omitempty"`
	State                     string `json:"state"`
	ParallelBatchTransitionID string `json:"parallel_batch_transition_id,omitempty"`
	ParallelBranchEdgeID      string `json:"parallel_branch_edge_id,omitempty"`
}

type WorkflowRun struct {
	ID                  string  `json:"id"`
	TaskID              string  `json:"task_id"`
	PlacementID         string  `json:"placement_id"`
	NodeID              string  `json:"node_id"`
	NodeKind            string  `json:"node_kind,omitempty"`
	ScriptPath          string  `json:"script_path,omitempty"`
	SessionID           string  `json:"session_id,omitempty"`
	SessionName         string  `json:"session_name,omitempty"`
	Role                string  `json:"role,omitempty"`
	Status              string  `json:"status"`
	Generation          int64   `json:"generation"`
	StartedAtUnixMs     *int64  `json:"started_at_unix_ms"`
	CompletedAtUnixMs   *int64  `json:"completed_at_unix_ms"`
	InterruptedAtUnixMs *int64  `json:"interrupted_at_unix_ms"`
	InterruptionReason  *string `json:"interruption_reason"`
	InterruptionDetail  string  `json:"interruption_detail_json,omitempty"`
	WaitingAskID        *string `json:"waiting_ask_id,omitempty"`
}

type WorkflowTaskTransition struct {
	ID                    string                   `json:"id"`
	TaskID                string                   `json:"task_id"`
	SourceRunID           string                   `json:"source_run_id,omitempty"`
	SourcePlacementID     string                   `json:"source_placement_id,omitempty"`
	SourceNodeID          string                   `json:"source_node_id,omitempty"`
	SourceNodeKey         string                   `json:"source_node_key,omitempty"`
	SourceNodeDisplayName string                   `json:"source_node_display_name,omitempty"`
	TransitionGroupID     string                   `json:"transition_group_id,omitempty"`
	TransitionID          string                   `json:"transition_id"`
	TransitionDisplayName string                   `json:"transition_display_name,omitempty"`
	WorkflowRevisionSeen  int64                    `json:"workflow_revision_seen"`
	Actor                 string                   `json:"actor,omitempty"`
	State                 string                   `json:"state"`
	Commentary            string                   `json:"commentary,omitempty"`
	OutputValues          map[string]string        `json:"output_values,omitempty"`
	CreatedAt             int64                    `json:"created_at_unix_ms"`
	AppliedAtUnixMs       *int64                   `json:"applied_at_unix_ms,omitempty"`
	Edges                 []WorkflowTransitionEdge `json:"edges,omitempty"`
}

type WorkflowTransitionEdge struct {
	ID                    string                      `json:"id"`
	TaskTransitionID      string                      `json:"task_transition_id"`
	WorkflowEdgeID        string                      `json:"workflow_edge_id,omitempty"`
	EdgeKey               string                      `json:"edge_key"`
	TargetNodeID          string                      `json:"target_node_id,omitempty"`
	TargetNodeKey         string                      `json:"target_node_key,omitempty"`
	TargetNodeDisplayName string                      `json:"target_node_display_name,omitempty"`
	TargetNodeKind        string                      `json:"target_node_kind,omitempty"`
	TargetPlacementID     string                      `json:"target_placement_id,omitempty"`
	State                 string                      `json:"state"`
	ContextMode           string                      `json:"context_mode,omitempty"`
	RequiresApproval      bool                        `json:"requires_approval"`
	InputBindings         []WorkflowInputBinding      `json:"input_bindings,omitempty"`
	OutputRequirements    []WorkflowOutputRequirement `json:"output_requirements,omitempty"`
	WorkflowRevisionSeen  int64                       `json:"workflow_revision_seen"`
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
	ActivityID       string                  `json:"activity_id"`
	Type             string                  `json:"type"`
	TaskID           string                  `json:"task_id"`
	OccurredAtUnixMs int64                   `json:"occurred_at_unix_ms"`
	UpdatedAtUnixMs  int64                   `json:"updated_at_unix_ms"`
	Actor            string                  `json:"actor,omitempty"`
	Summary          string                  `json:"summary"`
	Comment          *WorkflowTaskComment    `json:"comment,omitempty"`
	Transition       *WorkflowTaskTransition `json:"transition,omitempty"`
	Run              *WorkflowRun            `json:"run,omitempty"`
	Attention        *WorkflowAttentionItem  `json:"attention,omitempty"`
}

func (r WorkflowCreateRequest) Validate() error {
	return validateWorkflowName(r.Name)
}

func (r WorkflowCreateAndLinkProjectRequest) Validate() error {
	if err := validateWorkflowName(r.Name); err != nil {
		return err
	}
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	return validateWorkflowProjectLinkDefaultMode(r.DefaultPolicy)
}

func (r WorkflowUpdateRequest) Validate() error {
	return validateWorkflowIDAndName(r.WorkflowID, r.Name)
}

func (r WorkflowListRequest) Validate() error {
	if r.PageSize < 0 {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "page_size", Message: "page_size must be non-negative"}
	}
	if r.PageSize > WorkflowListMaxPageSize {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", fmt.Sprintf("page_size must be <= %d", WorkflowListMaxPageSize))
	}
	if r.PageToken != strings.TrimSpace(r.PageToken) {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
	}
	if r.ProjectID != nil {
		if err := validateRequired("project_id", *r.ProjectID); err != nil {
			return err
		}
	}
	if r.WorkflowID != nil {
		if err := validateRequired("workflow_id", *r.WorkflowID); err != nil {
			return err
		}
	}
	return nil
}

func (r WorkflowGetRequest) Validate() error {
	return validateRequired("workflow_id", r.WorkflowID)
}

func (r WorkflowNodeAddRequest) Validate() error {
	return validateWorkflowNodeFields(r.WorkflowID, "", r.Key, r.Kind, r.DisplayName, r.GroupKey, r.CompletionMode, r.ScriptPath, r.InputFields, r.JoinInputProviders)
}

func (r WorkflowNodeUpdateRequest) Validate() error {
	if err := validateRequired("node_id", r.NodeID); err != nil {
		return err
	}
	return validateWorkflowNodeFields(r.WorkflowID, r.NodeID, r.Key, r.Kind, r.DisplayName, r.GroupKey, r.CompletionMode, r.ScriptPath, r.InputFields, r.JoinInputProviders)
}

func validateWorkflowNodeFields(workflowID string, nodeID string, key string, kind string, displayName string, groupKey string, completionMode string, scriptPath *string, inputFields []WorkflowInputField, joinInputProviders []WorkflowJoinInputProvider) error {
	if err := validateRequired("workflow_id", workflowID); err != nil {
		return err
	}
	if err := validateModelKey("key", key); err != nil {
		return err
	}
	if err := validateRequired("kind", kind); err != nil {
		return err
	}
	if err := validateDisplayName(displayName); err != nil {
		return err
	}
	if err := validateWorkflowNodeCompletionMode(kind, completionMode); err != nil {
		return err
	}
	if scriptPath != nil && strings.TrimSpace(*scriptPath) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "script_path", "script_path must be null or a non-empty path")
	}
	if scriptPath != nil && strings.TrimSpace(kind) != "script" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "script_path", "script_path is only valid for script nodes")
	}
	if strings.TrimSpace(groupKey) != "" {
		if err := validateModelKey("group_key", groupKey); err != nil {
			return err
		}
	}
	for _, field := range inputFields {
		if err := validateModelKey("input_field.name", field.Name); err != nil {
			return err
		}
		if strings.TrimSpace(field.Description) == "" {
			return workflowRequestError(WorkflowRequestErrorRequired, "input_field.description", "input_field.description is required")
		}
	}
	for _, provider := range joinInputProviders {
		if err := validateModelKey("join_input_provider.input_name", provider.InputName); err != nil {
			return err
		}
		if err := validateRequired("join_input_provider.provider_edge_id", provider.ProviderEdgeID); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowNodeCompletionMode(kind string, completionMode string) error {
	trimmedMode := strings.TrimSpace(completionMode)
	if trimmedMode == "" {
		return nil
	}
	if strings.TrimSpace(kind) != "agent" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "completion_mode", "completion_mode is only valid for agent nodes")
	}
	switch trimmedMode {
	case "auto", "structured_output", "tool", "shell_command", "unstructured_output":
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "completion_mode", "completion_mode must be auto|structured_output|tool|shell_command|unstructured_output")
	}
}

func (r WorkflowNodeGroupAddRequest) Validate() error {
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
		return err
	}
	if err := validateModelKey("group_key", r.GroupKey); err != nil {
		return err
	}
	if err := validateDisplayName(r.DisplayName); err != nil {
		return err
	}
	if r.SortOrder < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "sort_order", "sort_order must be non-negative")
	}
	return nil
}

func (r WorkflowNodeGroupUpdateRequest) Validate() error {
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
		return err
	}
	if err := validateRequired("group_id", r.GroupID); err != nil {
		return err
	}
	if err := validateModelKey("group_key", r.GroupKey); err != nil {
		return err
	}
	if err := validateDisplayName(r.DisplayName); err != nil {
		return err
	}
	if r.SortOrder < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "sort_order", "sort_order must be non-negative")
	}
	return nil
}

func (r WorkflowNodeGroupDeleteRequest) Validate() error {
	return validateRequiredFields(requiredField("workflow_id", r.WorkflowID), requiredField("group_id", r.GroupID))
}

func (r WorkflowTransitionGroupAddRequest) Validate() error {
	return validateWorkflowTransitionGroupFields(r.WorkflowID, "", r.SourceNodeID, r.TransitionID, r.DisplayName, r.Description)
}

func (r WorkflowTransitionGroupUpdateRequest) Validate() error {
	if err := validateRequired("group_id", r.GroupID); err != nil {
		return err
	}
	return validateWorkflowTransitionGroupFields(r.WorkflowID, r.GroupID, r.SourceNodeID, r.TransitionID, r.DisplayName, r.Description)
}

func validateWorkflowTransitionGroupFields(workflowID string, groupID string, sourceNodeID string, transitionID string, displayName string, description string) error {
	_ = groupID
	if err := validateRequired("workflow_id", workflowID); err != nil {
		return err
	}
	if err := validateRequired("source_node_id", sourceNodeID); err != nil {
		return err
	}
	if err := validateModelKey("transition_id", transitionID); err != nil {
		return err
	}
	if strings.TrimSpace(displayName) != "" {
		if err := validateDisplayName(displayName); err != nil {
			return err
		}
	}
	if len([]rune(description)) > 1000 {
		return workflowRequestError(WorkflowRequestErrorTooLong, "description", "description must be <= 1000 characters")
	}
	return nil
}

func (r WorkflowEdgeAddRequest) Validate() error {
	return validateWorkflowEdgeFields(r.WorkflowID, "", r.TransitionGroupID, r.Key, r.TargetNodeID, r.ContextMode, r.ContextSource, r.Parameters)
}

func (r WorkflowEdgeUpdateRequest) Validate() error {
	if err := validateRequired("edge_id", r.EdgeID); err != nil {
		return err
	}
	return validateWorkflowEdgeFields(r.WorkflowID, r.EdgeID, r.TransitionGroupID, r.Key, r.TargetNodeID, r.ContextMode, r.ContextSource, r.Parameters)
}

func validateWorkflowEdgeFields(workflowID string, edgeID string, transitionGroupID string, key string, targetNodeID string, contextMode string, contextSource WorkflowContextSource, parameters []WorkflowParameter) error {
	_ = edgeID
	for _, field := range []struct{ name, value string }{{"workflow_id", workflowID}, {"transition_group_id", transitionGroupID}, {"target_node_id", targetNodeID}, {"context_mode", contextMode}} {
		if err := validateRequired(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateModelKey("key", key); err != nil {
		return err
	}
	if err := validateWorkflowContextSource(contextSource); err != nil {
		return err
	}
	if len(parameters) > WorkflowGraphDraftMaxFieldsPerEntity {
		return workflowRequestError(WorkflowRequestErrorTooLong, "parameters", fmt.Sprintf("parameters must be <= %d", WorkflowGraphDraftMaxFieldsPerEntity))
	}
	return nil
}

func validateWorkflowContextSource(source WorkflowContextSource) error {
	switch strings.TrimSpace(source.Kind) {
	case "", "immediate_source":
		if strings.TrimSpace(source.NodeKey) != "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "context_source.node_key", "context_source.node_key must be empty for immediate_source")
		}
		return nil
	case "selected_node":
		if err := validateModelKey("context_source.node_key", source.NodeKey); err != nil {
			return err
		}
		return nil
	case "previous_target", "previous_target_or_new":
		if strings.TrimSpace(source.NodeKey) != "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "context_source.node_key", "context_source.node_key must be empty for target-derived context sources")
		}
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "context_source.kind", "context_source.kind is invalid")
	}
}

func (r WorkflowLinkProjectRequest) Validate() error {
	if err := validateRequiredFields(requiredField("project_id", r.ProjectID), requiredField("workflow_id", r.WorkflowID)); err != nil {
		return err
	}
	return validateWorkflowProjectLinkDefaultMode(r.DefaultPolicy)
}

func (r WorkflowListProjectLinksRequest) Validate() error {
	return validateRequired("project_id", r.ProjectID)
}

func (r WorkflowSetDefaultProjectLinkRequest) Validate() error {
	return validateRequiredFields(requiredField("project_id", r.ProjectID), requiredField("workflow_id", r.WorkflowID))
}

func (r WorkflowUnlinkProjectRequest) Validate() error {
	return validateRequired("link_id", r.LinkID)
}

func (r WorkflowDeletePreviewRequest) Validate() error {
	return validateRequired("workflow_id", r.WorkflowID)
}

func (r WorkflowDeleteRequest) Validate() error {
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
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
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
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
	return validateRequiredFields(
		requiredField("workflow_id", r.WorkflowID),
		requiredField("node_id", r.NodeID),
	)
}

func (r WorkflowGraphValidateDraftRequest) Validate() error {
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
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
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
		return err
	}
	return validateWorkflowGraphDraftEnvelope(r.Graph)
}

func (r WorkflowGraphSavePreviewRequest) Validate() error {
	if err := validateRequired("workflow_id", r.WorkflowID); err != nil {
		return err
	}
	if err := validateWorkflowGraphMetadata(r.Metadata); err != nil {
		return err
	}
	if r.ExpectedVersion < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "expected_version", "expected_version must be non-negative")
	}
	return validateWorkflowGraphDraftEnvelope(r.Graph)
}

func (r WorkflowGraphSaveRequest) Validate() error {
	if err := (WorkflowGraphSavePreviewRequest{WorkflowID: r.WorkflowID, ExpectedVersion: r.ExpectedVersion, Metadata: r.Metadata, Graph: r.Graph}).Validate(); err != nil {
		return err
	}
	if r.Confirmation == nil {
		return nil
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"expected_removed_node_count", r.Confirmation.ExpectedRemovedNodeCount},
		{"expected_removed_transition_group_count", r.Confirmation.ExpectedRemovedTransitionGroupCount},
		{"expected_removed_edge_count", r.Confirmation.ExpectedRemovedEdgeCount},
		{"expected_node_task_reference_count", r.Confirmation.ExpectedNodeTaskReferenceCount},
		{"expected_edge_task_reference_count", r.Confirmation.ExpectedEdgeTaskReferenceCount},
	} {
		if field.value < 0 {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field.name, field.name+" must be non-negative")
		}
	}
	return nil
}

func validateWorkflowGraphMetadata(metadata *WorkflowGraphMetadata) error {
	if metadata == nil {
		return nil
	}
	name := strings.TrimSpace(metadata.Name)
	if name == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, "metadata.name", "metadata.name is required")
	}
	if name != metadata.Name {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "metadata.name", "metadata.name must not have leading or trailing whitespace")
	}
	if len([]rune(name)) > 120 {
		return workflowRequestError(WorkflowRequestErrorTooLong, "metadata.name", "metadata.name is too long")
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
	for _, node := range graph.Nodes {
		if err := validateWorkflowNodeCompletionMode(node.Kind, node.CompletionMode); err != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "graph.nodes.completion_mode", err.Error())
		}
		if node.ScriptPath != nil && strings.TrimSpace(node.Kind) != "script" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "graph.nodes.script_path", "script_path is only valid on script nodes")
		}
		if len(node.InputFields) > WorkflowGraphDraftMaxFieldsPerEntity {
			return workflowRequestError(WorkflowRequestErrorTooLong, "graph.nodes.input_fields", fmt.Sprintf("input_fields must be <= %d", WorkflowGraphDraftMaxFieldsPerEntity))
		}
		if len(node.JoinInputProviders) > WorkflowGraphDraftMaxFieldsPerEntity {
			return workflowRequestError(WorkflowRequestErrorTooLong, "graph.nodes.join_input_providers", fmt.Sprintf("join_input_providers must be <= %d", WorkflowGraphDraftMaxFieldsPerEntity))
		}
	}
	for _, edge := range graph.Edges {
		if err := validateWorkflowContextSource(edge.ContextSource); err != nil {
			return err
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

func (r WorkflowTaskCreateRequest) Validate() error {
	if err := validateRequiredFields(requiredField("project_id", r.ProjectID), requiredField("title", r.Title)); err != nil {
		return err
	}
	if err := validateLabelIDs("label_ids", r.LabelIDs); err != nil {
		return err
	}
	if r.WorkflowID != nil {
		return validateRequired("workflow_id", *r.WorkflowID)
	}
	return nil
}

func (r WorkflowTaskUpdateRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if r.Title != nil {
		return validateRequired("title", *r.Title)
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
	if r.Task.CurrentSessionIDs == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "task.current_session_ids", "current_session_ids is required")
	}
	for index, sessionID := range r.Task.CurrentSessionIDs {
		if strings.TrimSpace(sessionID) == "" || (index > 0 && r.Task.CurrentSessionIDs[index-1] >= sessionID) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.current_session_ids", "current_session_ids must contain sorted unique non-blank IDs")
		}
	}
	if r.Task.CurrentScripts == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "task.current_scripts", "current_scripts is required")
	}
	for index, script := range r.Task.CurrentScripts {
		if strings.TrimSpace(script.RunID) == "" ||
			strings.TrimSpace(script.Path) == "" ||
			(index > 0 && r.Task.CurrentScripts[index-1].RunID >= script.RunID) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "task.current_scripts", "current_scripts must contain sorted unique non-blank run targets")
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
	return validateLabelIDs("task.label_ids", r.LabelIDs)
}

func (r WorkflowTaskListItem) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	return validateLabelIDs("label_ids", r.LabelIDs)
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

func (r WorkflowBoardTaskCard) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	return validateLabelIDs("label_ids", r.LabelIDs)
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
	if r.WorkflowID == nil {
		return workflowRequestError(WorkflowRequestErrorRequired, "workflow_id", "workflow_id is required")
	}
	if err := validateRequired("workflow_id", *r.WorkflowID); err != nil {
		return err
	}
	switch r.Kind {
	case "question":
		if err := validateRequiredAttentionString("run_id", r.RunID); err != nil {
			return err
		}
		if err := validateRequiredAttentionString("ask_id", r.AskID); err != nil {
			return err
		}
		if err := validateOptionalAttentionString("session_id", r.SessionID); err != nil {
			return err
		}
		if err := validateWorkflowAttentionRecommendation(r.Suggestions, r.RecommendedOptionIndex); err != nil {
			return err
		}
		if r.Question != nil {
			if err := r.Question.Validate(); err != nil {
				return err
			}
		}
		return validateWorkflowAttentionFieldsAbsent(r.Kind,
			workflowAttentionFieldPresence{name: "task_transition_id", present: r.TaskTransitionID != nil},
			workflowAttentionFieldPresence{name: "approval_snapshot", present: r.ApprovalSnapshot != nil},
			workflowAttentionFieldPresence{name: "detail_json", present: r.DetailJSON != nil},
		)
	case "approval":
		if err := validateRequiredAttentionString("task_transition_id", r.TaskTransitionID); err != nil {
			return err
		}
		if r.ApprovalSnapshot == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "approval_snapshot", "approval_snapshot is required")
		}
		if err := r.ApprovalSnapshot.Validate(); err != nil {
			return err
		}
		return validateWorkflowAttentionFieldsAbsent(r.Kind,
			workflowAttentionFieldPresence{name: "run_id", present: r.RunID != nil},
			workflowAttentionFieldPresence{name: "session_id", present: r.SessionID != nil},
			workflowAttentionFieldPresence{name: "ask_id", present: r.AskID != nil},
			workflowAttentionFieldPresence{name: "question", present: r.Question != nil},
			workflowAttentionFieldPresence{name: "detail_json", present: r.DetailJSON != nil},
			workflowAttentionFieldPresence{name: "suggestions", present: r.Suggestions != nil},
			workflowAttentionFieldPresence{name: "recommended_option_index", present: r.RecommendedOptionIndex != nil},
		)
	case "interrupted_run":
		if err := validateRequiredAttentionString("run_id", r.RunID); err != nil {
			return err
		}
		if err := validateOptionalAttentionString("session_id", r.SessionID); err != nil {
			return err
		}
		if err := validateOptionalAttentionString("detail_json", r.DetailJSON); err != nil {
			return err
		}
		return validateWorkflowAttentionFieldsAbsent(r.Kind,
			workflowAttentionFieldPresence{name: "ask_id", present: r.AskID != nil},
			workflowAttentionFieldPresence{name: "question", present: r.Question != nil},
			workflowAttentionFieldPresence{name: "task_transition_id", present: r.TaskTransitionID != nil},
			workflowAttentionFieldPresence{name: "approval_snapshot", present: r.ApprovalSnapshot != nil},
			workflowAttentionFieldPresence{name: "suggestions", present: r.Suggestions != nil},
			workflowAttentionFieldPresence{name: "recommended_option_index", present: r.RecommendedOptionIndex != nil},
		)
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "kind", "kind must be question, approval, or interrupted_run")
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
		if item.Attention == nil {
			continue
		}
		if item.Type != "run_interrupted" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].type", index), "attention is only allowed on run_interrupted activity")
		}
		if err := item.Attention.Validate(); err != nil {
			return prefixWorkflowProjectionValidationField("items", index, err)
		}
		if item.Attention.Kind != "interrupted_run" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].attention.kind", index), "attention must be interrupted_run")
		}
		if item.Attention.TaskID != item.TaskID {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("items[%d].attention.task_id", index), "attention task_id must match activity task_id")
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
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	if r.ExecutionTarget != nil {
		return r.ExecutionTarget.Validate()
	}
	return nil
}

func (r WorkflowTaskResumeRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func (r WorkflowTaskApproveRequest) Validate() error {
	if r.ExecutionTarget != nil {
		if err := r.ExecutionTarget.Validate(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(r.TaskTransitionID) != "" {
		return r.SetupOperationID.Validate()
	}
	if err := validateRequired("transition_id", r.TransitionID); err != nil {
		return err
	}
	return r.SetupOperationID.Validate()
}

func (r WorkflowTaskMoveRequest) Validate() error {
	if err := validateRequiredFields(requiredField("task_id", r.TaskID), requiredField("target_node_id", r.TargetNodeID)); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	if r.ExecutionTarget != nil {
		return r.ExecutionTarget.Validate()
	}
	return nil
}

func (r WorkflowTaskCompleteRequest) Validate() error {
	if err := validateWorkflowTaskCompleteActor(r); err != nil {
		return err
	}
	for _, field := range []requiredWorkflowField{
		requiredField("run_id", r.RunID),
		requiredField("session_id", r.SessionID),
		requiredField("task_id", r.TaskID),
		requiredField("project_id", r.ProjectID),
		requiredField("short_id", r.ShortID),
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

func (r WorkflowTaskCancelRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func (r WorkflowTaskDeleteRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func (r WorkflowTaskInterruptRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
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
	for _, value := range []string{r.RunID, r.SessionID, r.TaskID, r.ShortID} {
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

func (r WorkflowTaskQuestionAnswerRequest) Validate() error {
	if err := validateRequiredFields(requiredField("client_request_id", r.ClientRequestID), requiredField("task_id", r.TaskID), requiredField("ask_id", r.AskID)); err != nil {
		return err
	}
	hasTextAnswer := strings.TrimSpace(r.Answer) != ""
	hasFreeform := strings.TrimSpace(r.FreeformAnswer) != ""
	hasApproval := r.Approval != nil
	if r.SelectedOptionNumber != nil && *r.SelectedOptionNumber <= 0 {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "selected_option_number", Message: "selected_option_number must be positive when present"}
	}
	hasSelected := r.SelectedOptionNumber != nil
	hasAnswer := hasTextAnswer || hasFreeform || hasSelected || hasApproval
	hasError := strings.TrimSpace(r.ErrorMessage) != ""
	if hasAnswer && hasError {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "error_message", Message: "error_message cannot be combined with answer fields"}
	}
	if hasApproval {
		if hasTextAnswer || hasFreeform || hasSelected {
			return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "approval", Message: "approval cannot be combined with ordinary answer fields"}
		}
		if err := validateWorkflowApprovalDecision(r.Approval.Decision); err != nil {
			return err
		}
	}
	if hasTextAnswer && (hasFreeform || hasSelected) {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "answer", Message: "answer cannot be combined with selected_option_number or freeform_answer"}
	}
	if !hasAnswer && !hasError {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorRequired, Field: "answer", Message: "answer is required"}
	}
	return nil
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

// WorkflowTaskCommentListMaxPageSize bounds a single comment page so a
// client-supplied page_size cannot drive an oversized storage query/response.
const WorkflowTaskCommentListMaxPageSize = 100

func (r WorkflowTaskCommentListRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if r.PageSize < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", "page_size must be non-negative")
	}
	if r.PageSize > WorkflowTaskCommentListMaxPageSize {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", fmt.Sprintf("page_size must be <= %d", WorkflowTaskCommentListMaxPageSize))
	}
	if strings.TrimSpace(r.PageToken) != r.PageToken {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
	}
	return nil
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
	if err := validateOptionalNonBlank("workflow_id", r.WorkflowID); err != nil {
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
	if r.ProjectID == nil && strings.TrimSpace(r.PageToken) == "" {
		message := "project_id is required on the first page"
		if r.WorkflowID != nil {
			message = "project_id is required when workflow_id is selected"
		}
		return workflowRequestError(WorkflowRequestErrorRequired, "project_id", message)
	}
	for _, scope := range []struct {
		field string
		value *string
	}{
		{field: "project_id", value: r.ProjectID},
		{field: "workflow_id", value: r.WorkflowID},
	} {
		if err := validateOptionalNonBlank(scope.field, scope.value); err != nil {
			return err
		}
	}
	if r.PageSize < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", "page_size must be non-negative")
	}
	if r.PageSize > WorkflowTaskListMaxPageSize {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", fmt.Sprintf("page_size must be <= %d", WorkflowTaskListMaxPageSize))
	}
	if strings.TrimSpace(r.PageToken) != r.PageToken {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
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
		case WorkflowTaskListSortFieldCreated, WorkflowTaskListSortFieldUpdated, WorkflowTaskListSortFieldStatus, WorkflowTaskListSortFieldColumn, WorkflowTaskListSortFieldRunCount, WorkflowTaskListSortFieldTitle:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("sort[%d].field", index), "sort field must be created, updated, status, column, run_count, or title")
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
	if err := validateRequiredFields(requiredField("project_id", r.ProjectID), requiredField("workflow_id", r.WorkflowID), requiredField("node_id", r.NodeID)); err != nil {
		return err
	}
	if r.PageSize < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", "page_size must be non-negative")
	}
	if r.PageSize > WorkflowBoardNodeCardsMaxPageSize {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", fmt.Sprintf("page_size must be <= %d", WorkflowBoardNodeCardsMaxPageSize))
	}
	if r.PageToken != nil && strings.TrimSpace(*r.PageToken) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not be blank")
	}
	if r.PageToken != nil && strings.TrimSpace(*r.PageToken) != *r.PageToken {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
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
	return validateRequired("workflow_id", r.WorkflowID)
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

func (r WorkflowTaskActivityListRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if r.PageSize < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", "page_size must be non-negative")
	}
	if strings.TrimSpace(r.PageToken) != r.PageToken {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
	}
	return nil
}

func validateRequired(name string, value string) error {
	if strings.TrimSpace(value) == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, name, name+" is required")
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

func validateWorkflowIDAndName(workflowID string, name string) error {
	if err := validateRequired("workflow_id", workflowID); err != nil {
		return err
	}
	return validateWorkflowName(name)
}

func validateWorkflowName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return workflowRequestError(WorkflowRequestErrorRequired, "name", "name is required")
	}
	if len([]rune(trimmed)) > 120 {
		return workflowRequestError(WorkflowRequestErrorTooLong, "name", "name must be <= 120 characters")
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
