package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
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
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Description     string                  `json:"description"`
	ExecutionPolicy WorkflowExecutionPolicy `json:"execution_policy"`
	Version         int64                   `json:"version"`
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
	Name            string                   `json:"name"`
	Description     string                   `json:"description"`
	ExecutionPolicy *WorkflowExecutionPolicy `json:"execution_policy,omitempty"`
}

type WorkflowExecutionPolicyMode string

const (
	WorkflowExecutionPolicyNone          WorkflowExecutionPolicyMode = "none"
	WorkflowExecutionPolicyHead          WorkflowExecutionPolicyMode = "head"
	WorkflowExecutionPolicyDefaultBranch WorkflowExecutionPolicyMode = "default_branch"
	WorkflowExecutionPolicyCustomRef     WorkflowExecutionPolicyMode = "custom_ref"
	WorkflowExecutionPolicyAsk           WorkflowExecutionPolicyMode = "ask"
)

type WorkflowExecutionPolicy struct {
	Mode      WorkflowExecutionPolicyMode `json:"mode"`
	CustomRef *string                     `json:"custom_ref,omitempty"`
}

func (p WorkflowExecutionPolicy) Validate() error {
	return validateWorkflowExecutionPolicy(p, "execution_policy")
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
	Name            string                   `json:"name"`
	Description     string                   `json:"description,omitempty"`
	ExecutionPolicy *WorkflowExecutionPolicy `json:"execution_policy,omitempty"`
}

type WorkflowCreateResponse struct {
	Workflow WorkflowRecord `json:"workflow"`
}

type WorkflowCreateAndLinkProjectRequest struct {
	Name            string                         `json:"name"`
	Description     string                         `json:"description,omitempty"`
	ExecutionPolicy *WorkflowExecutionPolicy       `json:"execution_policy,omitempty"`
	ProjectID       string                         `json:"project_id"`
	DefaultPolicy   WorkflowProjectLinkDefaultMode `json:"default_policy,omitempty"`
}

type WorkflowCreateAndLinkProjectResponse struct {
	Workflow WorkflowRecord      `json:"workflow"`
	Link     ProjectWorkflowLink `json:"link"`
}

type WorkflowUpdateRequest struct {
	WorkflowID      string                   `json:"workflow_id"`
	Name            string                   `json:"name"`
	Description     string                   `json:"description,omitempty"`
	ExecutionPolicy *WorkflowExecutionPolicy `json:"execution_policy,omitempty"`
}

type WorkflowListRequest struct {
	PageSize  int    `json:"page_size,omitempty"`
	PageToken string `json:"page_token,omitempty"`
	Query     string `json:"query,omitempty"`
	ExactName string `json:"exact_name,omitempty"`
}

type WorkflowListResponse struct {
	Workflows     []WorkflowRecord `json:"workflows"`
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
	WorkflowID        string                          `json:"workflow_id,omitempty"`
	NodeID            string                          `json:"node_id,omitempty"`
	TransitionGroupID string                          `json:"transition_group_id,omitempty"`
	EdgeID            string                          `json:"edge_id,omitempty"`
	Details           *WorkflowValidationErrorDetails `json:"details,omitempty"`
	RelatedIDs        []string                        `json:"related_ids,omitempty"`
	BlocksContext     bool                            `json:"blocks_context"`
}

type WorkflowValidationErrorDetails struct {
	FieldName      string `json:"field_name,omitempty"`
	InputName      string `json:"input_name,omitempty"`
	Placeholder    string `json:"placeholder,omitempty"`
	ProviderEdgeID string `json:"provider_edge_id,omitempty"`
}

type WorkflowTaskCreateRequest struct {
	ProjectID         string `json:"project_id"`
	WorkflowID        string `json:"workflow_id,omitempty"`
	Title             string `json:"title"`
	Body              string `json:"body,omitempty"`
	SourceURL         string `json:"source_url,omitempty"`
	SourceWorkspaceID string `json:"source_workspace_id,omitempty"`
}

type WorkflowTaskCreateResponse struct {
	Task WorkflowTaskSummary `json:"task"`
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
	TaskID              string                                `json:"task_id"`
	SetupOperationID    WorktreeSetupOperationID              `json:"setup_operation_id"`
	SelectionGeneration *string                               `json:"selection_generation,omitempty"`
	Selection           *WorkflowTaskExecutionTargetSelection `json:"selection,omitempty"`
}

type WorkflowTaskStartResponse struct {
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
	TaskTransitionID    string                                `json:"task_transition_id,omitempty"`
	TransitionID        string                                `json:"transition_id,omitempty"`
	SetupOperationID    WorktreeSetupOperationID              `json:"setup_operation_id"`
	SelectionGeneration *string                               `json:"selection_generation,omitempty"`
	Selection           *WorkflowTaskExecutionTargetSelection `json:"selection,omitempty"`
}

type WorkflowTaskApproveResponse struct {
	TransitionID string   `json:"transition_id"`
	TaskID       string   `json:"task_id"`
	State        string   `json:"state"`
	PlacementIDs []string `json:"placement_ids,omitempty"`
	RunIDs       []string `json:"run_ids,omitempty"`
}

type WorkflowTaskMoveRequest struct {
	TaskID              string                                `json:"task_id"`
	TargetNodeID        string                                `json:"target_node_id"`
	OutputValues        map[string]string                     `json:"output_values,omitempty"`
	Commentary          string                                `json:"commentary,omitempty"`
	AllowMissingEdge    bool                                  `json:"allow_missing_edge,omitempty"`
	AutoApprove         bool                                  `json:"auto_approve,omitempty"`
	SetupOperationID    WorktreeSetupOperationID              `json:"setup_operation_id"`
	SelectionGeneration *string                               `json:"selection_generation,omitempty"`
	Selection           *WorkflowTaskExecutionTargetSelection `json:"selection,omitempty"`
}

type WorkflowTaskExecutionTargetSelectionMode string

const (
	WorkflowTaskExecutionTargetSelectionNone          WorkflowTaskExecutionTargetSelectionMode = "none"
	WorkflowTaskExecutionTargetSelectionHead          WorkflowTaskExecutionTargetSelectionMode = "head"
	WorkflowTaskExecutionTargetSelectionDefaultBranch WorkflowTaskExecutionTargetSelectionMode = "default_branch"
	WorkflowTaskExecutionTargetSelectionCustomRef     WorkflowTaskExecutionTargetSelectionMode = "custom_ref"
)

type WorkflowTaskExecutionTargetSelection struct {
	Mode      WorkflowTaskExecutionTargetSelectionMode `json:"mode"`
	CustomRef *string                                  `json:"custom_ref,omitempty"`
}

type WorkflowTaskExecutionTargetSourceKind string

const (
	WorkflowTaskExecutionTargetSourceNonGit         WorkflowTaskExecutionTargetSourceKind = "non_git"
	WorkflowTaskExecutionTargetSourceNamedRef       WorkflowTaskExecutionTargetSourceKind = "named_ref"
	WorkflowTaskExecutionTargetSourceDetachedCommit WorkflowTaskExecutionTargetSourceKind = "detached_commit"
	WorkflowTaskExecutionTargetSourceUnavailable    WorkflowTaskExecutionTargetSourceKind = "unavailable"
)

type WorkflowTaskExecutionTargetSource struct {
	Kind     WorkflowTaskExecutionTargetSourceKind `json:"kind"`
	NamedRef *string                               `json:"named_ref,omitempty"`
	Commit   *string                               `json:"commit,omitempty"`
}

type WorkflowTaskExecutionTargetRecoveryCause string

type WorkflowTaskExecutionTargetSelectionRequired struct {
	TaskID              string                                     `json:"task_id"`
	Generation          string                                     `json:"generation"`
	SourceWorkspaceID   string                                     `json:"source_workspace_id"`
	Source              WorkflowTaskExecutionTargetSource          `json:"source"`
	SupportedSelections []WorkflowTaskExecutionTargetSelectionMode `json:"supported_selections"`
	ConfiguredPolicy    WorkflowExecutionPolicy                    `json:"configured_policy"`
	RecoveryCause       *WorkflowTaskExecutionTargetRecoveryCause  `json:"recovery_cause,omitempty"`
}

type WorkflowTaskExecutionTargetMaterializationPhase string

const (
	WorkflowTaskExecutionTargetMaterializationPhaseMaterializing  WorkflowTaskExecutionTargetMaterializationPhase = "materializing"
	WorkflowTaskExecutionTargetMaterializationPhaseRecoveryQueued WorkflowTaskExecutionTargetMaterializationPhase = "recovery_queued"
	WorkflowTaskExecutionTargetMaterializationPhaseRecovering     WorkflowTaskExecutionTargetMaterializationPhase = "recovering"
)

type WorkflowTaskExecutionTargetMaterializationProgress struct {
	TaskID string                                          `json:"task_id"`
	Phase  WorkflowTaskExecutionTargetMaterializationPhase `json:"phase"`
}

// WorkflowTaskExecutionTargetNegotiationConflict tells the caller that its
// initiating action lost the durable negotiation fence. The caller must
// refetch the task before attempting another initiating action.
type WorkflowTaskExecutionTargetNegotiationConflict struct {
	TaskID string `json:"task_id"`
}

type WorkflowTaskInitiatingActionOutcome string

const (
	WorkflowTaskInitiatingActionOutcomeStarted           WorkflowTaskInitiatingActionOutcome = "started"
	WorkflowTaskInitiatingActionOutcomeMoved             WorkflowTaskInitiatingActionOutcome = "moved"
	WorkflowTaskInitiatingActionOutcomeApproved          WorkflowTaskInitiatingActionOutcome = "approved"
	WorkflowTaskInitiatingActionOutcomeSelectionRequired WorkflowTaskInitiatingActionOutcome = "selection_required"
	WorkflowTaskInitiatingActionOutcomeInProgress        WorkflowTaskInitiatingActionOutcome = "in_progress"
	WorkflowTaskInitiatingActionOutcomeConflict          WorkflowTaskInitiatingActionOutcome = "conflict"
)

// WorkflowTaskInitiatingActionResult is a closed wire union. Its JSON form
// contains exactly one payload named by Outcome.
type WorkflowTaskInitiatingActionResult struct {
	Outcome           WorkflowTaskInitiatingActionOutcome                 `json:"outcome"`
	Started           *WorkflowTaskStartResponse                          `json:"started,omitempty"`
	Moved             *WorkflowTaskMoveResponse                           `json:"moved,omitempty"`
	Approved          *WorkflowTaskApproveResponse                        `json:"approved,omitempty"`
	SelectionRequired *WorkflowTaskExecutionTargetSelectionRequired       `json:"selection_required,omitempty"`
	InProgress        *WorkflowTaskExecutionTargetMaterializationProgress `json:"in_progress,omitempty"`
	Conflict          *WorkflowTaskExecutionTargetNegotiationConflict     `json:"conflict,omitempty"`
}

func (result WorkflowTaskInitiatingActionResult) Validate() error {
	payloads := 0
	if result.Started != nil {
		payloads++
	}
	if result.Moved != nil {
		payloads++
	}
	if result.Approved != nil {
		payloads++
	}
	if result.SelectionRequired != nil {
		payloads++
	}
	if result.InProgress != nil {
		payloads++
	}
	if result.Conflict != nil {
		payloads++
	}
	if payloads != 1 {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "result", "initiating action result must contain exactly one payload")
	}
	switch result.Outcome {
	case WorkflowTaskInitiatingActionOutcomeStarted:
		if result.Started == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "started", "started outcome requires a started payload")
		}
	case WorkflowTaskInitiatingActionOutcomeMoved:
		if result.Moved == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "moved", "moved outcome requires a moved payload")
		}
	case WorkflowTaskInitiatingActionOutcomeApproved:
		if result.Approved == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "approved", "approved outcome requires an approved payload")
		}
	case WorkflowTaskInitiatingActionOutcomeSelectionRequired:
		if result.SelectionRequired == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_required", "selection_required outcome requires a selection payload")
		}
		if err := result.SelectionRequired.Validate(); err != nil {
			return err
		}
	case WorkflowTaskInitiatingActionOutcomeInProgress:
		if result.InProgress == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "in_progress", "in_progress outcome requires a progress payload")
		}
		if err := result.InProgress.Validate(); err != nil {
			return err
		}
	case WorkflowTaskInitiatingActionOutcomeConflict:
		if result.Conflict == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "conflict", "conflict outcome requires a conflict payload")
		}
		if err := result.Conflict.Validate(); err != nil {
			return err
		}
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "outcome", "unknown initiating action outcome")
	}
	return nil
}

func (result WorkflowTaskInitiatingActionResult) MarshalJSON() ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	type wireResult struct {
		Outcome           WorkflowTaskInitiatingActionOutcome                 `json:"outcome"`
		Started           *WorkflowTaskStartResponse                          `json:"started,omitempty"`
		Moved             *WorkflowTaskMoveResponse                           `json:"moved,omitempty"`
		Approved          *WorkflowTaskApproveResponse                        `json:"approved,omitempty"`
		SelectionRequired *WorkflowTaskExecutionTargetSelectionRequired       `json:"selection_required,omitempty"`
		InProgress        *WorkflowTaskExecutionTargetMaterializationProgress `json:"in_progress,omitempty"`
		Conflict          *WorkflowTaskExecutionTargetNegotiationConflict     `json:"conflict,omitempty"`
	}
	return json.Marshal(wireResult{
		Outcome:           result.Outcome,
		Started:           result.Started,
		Moved:             result.Moved,
		Approved:          result.Approved,
		SelectionRequired: result.SelectionRequired,
		InProgress:        result.InProgress,
		Conflict:          result.Conflict,
	})
}

func (result *WorkflowTaskInitiatingActionResult) UnmarshalJSON(data []byte) error {
	if result == nil {
		return errors.New("workflow task initiating action result is required")
	}
	type wireResult struct {
		Outcome           WorkflowTaskInitiatingActionOutcome                 `json:"outcome"`
		Started           *WorkflowTaskStartResponse                          `json:"started"`
		Moved             *WorkflowTaskMoveResponse                           `json:"moved"`
		Approved          *WorkflowTaskApproveResponse                        `json:"approved"`
		SelectionRequired *WorkflowTaskExecutionTargetSelectionRequired       `json:"selection_required"`
		InProgress        *WorkflowTaskExecutionTargetMaterializationProgress `json:"in_progress"`
		Conflict          *WorkflowTaskExecutionTargetNegotiationConflict     `json:"conflict"`
	}
	var wire wireResult
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	decoded := WorkflowTaskInitiatingActionResult{
		Outcome:           wire.Outcome,
		Started:           wire.Started,
		Moved:             wire.Moved,
		Approved:          wire.Approved,
		SelectionRequired: wire.SelectionRequired,
		InProgress:        wire.InProgress,
		Conflict:          wire.Conflict,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*result = decoded
	return nil
}

type WorkflowTaskMoveResponse struct {
	TransitionID  string   `json:"transition_id"`
	State         string   `json:"state"`
	PlacementIDs  []string `json:"placement_ids,omitempty"`
	RunIDs        []string `json:"run_ids,omitempty"`
	ApprovalError string   `json:"approval_error,omitempty"`
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
	TransitionID string   `json:"transition_id"`
	TaskID       string   `json:"task_id"`
	RunID        string   `json:"run_id"`
	State        string   `json:"state"`
	PlacementIDs []string `json:"placement_ids,omitempty"`
	RunIDs       []string `json:"run_ids,omitempty"`
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
	ProjectID string `json:"project_id,omitempty"`
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
	ID                     string                           `json:"id"`
	Kind                   string                           `json:"kind"`
	ProjectID              string                           `json:"project_id,omitempty"`
	WorkflowID             string                           `json:"workflow_id,omitempty"`
	TaskID                 string                           `json:"task_id,omitempty"`
	TaskShortID            string                           `json:"task_short_id,omitempty"`
	TaskTitle              string                           `json:"task_title,omitempty"`
	RunID                  string                           `json:"run_id,omitempty"`
	SessionID              string                           `json:"session_id,omitempty"`
	AskID                  string                           `json:"ask_id,omitempty"`
	TaskTransitionID       string                           `json:"task_transition_id,omitempty"`
	Message                string                           `json:"message"`
	DetailJSON             string                           `json:"detail_json,omitempty"`
	Suggestions            []string                         `json:"suggestions,omitempty"`
	RecommendedOptionIndex int                              `json:"recommended_option_index,omitempty"`
	Question               *WorkflowAttentionQuestionPrompt `json:"question,omitempty"`
	OccurredAtUnixMs       int64                            `json:"occurred_at_unix_ms"`
}

type WorkflowAttentionQuestionKind string

const (
	WorkflowAttentionQuestionKindOrdinary WorkflowAttentionQuestionKind = "ordinary"
	WorkflowAttentionQuestionKindApproval WorkflowAttentionQuestionKind = "approval"
)

type WorkflowAttentionQuestionPrompt struct {
	Kind                   WorkflowAttentionQuestionKind `json:"kind"`
	Suggestions            []string                      `json:"suggestions,omitempty"`
	RecommendedOptionIndex int                           `json:"recommended_option_index,omitempty"`
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
	SelectedOptionNumber int                                 `json:"selected_option_number,omitempty"`
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
	ProjectID        string `json:"project_id"`
	WorkflowID       string `json:"workflow_id,omitempty"`
	DonePreviewLimit int    `json:"done_preview_limit"`
	PageSize         int    `json:"page_size"`
	PageToken        string `json:"page_token"`
}

type WorkflowTaskRunStatus string

const (
	WorkflowTaskRunStatusOpen        WorkflowTaskRunStatus = "open"
	WorkflowTaskRunStatusRunning     WorkflowTaskRunStatus = "running"
	WorkflowTaskRunStatusInterrupted WorkflowTaskRunStatus = "interrupted"
	WorkflowTaskRunStatusDone        WorkflowTaskRunStatus = "done"
	WorkflowTaskRunStatusCanceled    WorkflowTaskRunStatus = "canceled"
)

type WorkflowTaskListSortField string

const (
	WorkflowTaskListSortFieldCreated  WorkflowTaskListSortField = "created"
	WorkflowTaskListSortFieldUpdated  WorkflowTaskListSortField = "updated"
	WorkflowTaskListSortFieldStatus   WorkflowTaskListSortField = "status"
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
	ProjectID   string                  `json:"project_id"`
	WorkflowID  string                  `json:"workflow_id,omitempty"`
	StatusKeys  []string                `json:"status_keys,omitempty"`
	RunStatuses []WorkflowTaskRunStatus `json:"run_statuses,omitempty"`
	Sort        []WorkflowTaskListSort  `json:"sort,omitempty"`
	PageSize    int                     `json:"page_size"`
	PageToken   string                  `json:"page_token,omitempty"`
}

type WorkflowTaskListResponse struct {
	ProjectID         string                 `json:"project_id"`
	WorkflowID        string                 `json:"workflow_id"`
	SelectedWorkflow  *WorkflowPickerItem    `json:"selected_workflow,omitempty"`
	NextPageToken     string                 `json:"next_page_token,omitempty"`
	GeneratedAtUnixMs int64                  `json:"generated_at_unix_ms"`
	Tasks             []WorkflowTaskListItem `json:"tasks"`
}

type WorkflowTaskListItem struct {
	TaskID          string                `json:"task_id"`
	ShortID         string                `json:"short_id"`
	WorkflowID      string                `json:"workflow_id"`
	Title           string                `json:"title"`
	CreatedAtUnixMs int64                 `json:"created_at_unix_ms"`
	UpdatedAtUnixMs int64                 `json:"updated_at_unix_ms"`
	StatusKeys      []string              `json:"status_keys"`
	RunStatus       WorkflowTaskRunStatus `json:"run_status"`
	RunCount        int                   `json:"run_count"`
}

type WorkflowBoardResponse struct {
	Board WorkflowBoard `json:"board"`
}

type WorkflowBoardNodeCardsListRequest struct {
	ProjectID  string `json:"project_id"`
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	PageSize   int    `json:"page_size"`
	PageToken  string `json:"page_token"`
}

type WorkflowBoardNodeCardsListResponse struct {
	ProjectID         string                  `json:"project_id"`
	WorkflowID        string                  `json:"workflow_id"`
	NodeID            string                  `json:"node_id"`
	Cards             []WorkflowBoardTaskCard `json:"cards"`
	NextPageToken     string                  `json:"next_page_token"`
	GeneratedAtUnixMs int64                   `json:"generated_at_unix_ms"`
}

type WorkflowBoard struct {
	ProjectID          string                  `json:"project_id"`
	Project            ProjectBoardProject     `json:"project"`
	SelectedWorkflow   WorkflowPickerItem      `json:"selected_workflow"`
	WorkflowPicker     []WorkflowPickerItem    `json:"workflows"`
	Groups             []WorkflowBoardGroup    `json:"groups"`
	Columns            []WorkflowBoardColumn   `json:"columns"`
	Cards              []WorkflowBoardTaskCard `json:"cards"`
	DonePreview        []WorkflowBoardTaskCard `json:"done_preview"`
	HasHiddenDoneCards bool                    `json:"has_hidden_done_cards"`
	NextPageToken      string                  `json:"next_page_token"`
	GeneratedAtUnixMs  int64                   `json:"generated_at_unix_ms"`
}

type ProjectBoardProject struct {
	ProjectID   string `json:"project_id"`
	ProjectKey  string `json:"project_key"`
	DisplayName string `json:"display_name"`
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
	BodyPreview     string                  `json:"body_preview,omitempty"`
	WorkflowID      string                  `json:"workflow_id"`
	ActiveNodeIDs   []string                `json:"active_node_ids,omitempty"`
	SourceWorkspace ProjectWorkspaceSummary `json:"source_workspace"`
	Status          WorkflowTaskStatus      `json:"status"`
	Actions         WorkflowTaskActions     `json:"actions"`
	UpdatedAtUnixMs int64                   `json:"updated_at_unix_ms"`
}

type WorkflowTaskStatus struct {
	Kind           string   `json:"kind"`
	Label          string   `json:"label"`
	NativeState    string   `json:"native_state"`
	NodeIDs        []string `json:"node_ids,omitempty"`
	RunIDs         []string `json:"run_ids,omitempty"`
	AttentionTypes []string `json:"attention_types,omitempty"`
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

type WorkflowProjectEvent struct {
	ProjectID        string   `json:"project_id,omitempty"`
	WorkflowID       string   `json:"workflow_id,omitempty"`
	Resource         string   `json:"resource"`
	Action           string   `json:"action"`
	ChangedIDs       []string `json:"changed_ids,omitempty"`
	OccurredAtUnixMs int64    `json:"occurred_at_unix_ms"`
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
	CanceledAt        int64    `json:"canceled_at_unix_ms"`
	CancelReason      string   `json:"cancel_reason,omitempty"`
	CreatedAtUnixMs   int64    `json:"created_at_unix_ms"`
	UpdatedAtUnixMs   int64    `json:"updated_at_unix_ms"`
	Done              bool     `json:"done"`
	ActiveNodeIDs     []string `json:"active_node_ids,omitempty"`
}

type WorkflowTaskDetail struct {
	Summary         WorkflowTaskSummary          `json:"summary"`
	Project         ProjectBoardProject          `json:"project"`
	Workflow        WorkflowPickerItem           `json:"workflow"`
	Body            string                       `json:"body"`
	SourceURL       string                       `json:"source_url,omitempty"`
	SourceWorkspace ProjectWorkspaceSummary      `json:"source_workspace"`
	ManagedWorktree *WorktreeView                `json:"managed_worktree,omitempty"`
	ExecutionTarget *WorkflowTaskExecutionTarget `json:"execution_target,omitempty"`
	Status          WorkflowTaskStatus           `json:"status"`
	Actions         WorkflowTaskActions          `json:"actions"`
	Attention       []WorkflowAttentionItem      `json:"attention,omitempty"`
	Placements      []WorkflowPlacement          `json:"placements"`
	Runs            []WorkflowRun                `json:"runs"`
	Transitions     []WorkflowTaskTransition     `json:"transitions"`
	Comments        []WorkflowTaskComment        `json:"comments"`
}

// WorkflowTaskExecutionTarget is the immutable target facts locked for a task.
// It is projected only through task detail; board cards intentionally omit it.
type WorkflowTaskExecutionTarget struct {
	Policy    WorkflowExecutionPolicyMode        `json:"policy"`
	CustomRef *string                            `json:"custom_ref,omitempty"`
	Source    *WorkflowTaskExecutionTargetSource `json:"source,omitempty"`
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
	ID                  string `json:"id"`
	TaskID              string `json:"task_id"`
	PlacementID         string `json:"placement_id"`
	NodeID              string `json:"node_id"`
	NodeKind            string `json:"node_kind,omitempty"`
	ScriptPath          string `json:"script_path,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
	SessionName         string `json:"session_name,omitempty"`
	Role                string `json:"role,omitempty"`
	Status              string `json:"status"`
	Generation          int64  `json:"generation"`
	StartedAtUnixMs     int64  `json:"started_at_unix_ms"`
	CompletedAtUnixMs   int64  `json:"completed_at_unix_ms"`
	InterruptedAtUnixMs int64  `json:"interrupted_at_unix_ms"`
	InterruptionReason  string `json:"interruption_reason,omitempty"`
	InterruptionDetail  string `json:"interruption_detail_json,omitempty"`
	WaitingAskID        string `json:"waiting_ask_id,omitempty"`
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
	AppliedAtUnixMs       int64                    `json:"applied_at_unix_ms"`
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
	if err := validateWorkflowName(r.Name); err != nil {
		return err
	}
	return validateOptionalWorkflowExecutionPolicy(r.ExecutionPolicy, "execution_policy")
}

func (r WorkflowCreateAndLinkProjectRequest) Validate() error {
	if err := validateWorkflowName(r.Name); err != nil {
		return err
	}
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if err := validateOptionalWorkflowExecutionPolicy(r.ExecutionPolicy, "execution_policy"); err != nil {
		return err
	}
	return validateWorkflowProjectLinkDefaultMode(r.DefaultPolicy)
}

func (r WorkflowUpdateRequest) Validate() error {
	if err := validateWorkflowIDAndName(r.WorkflowID, r.Name); err != nil {
		return err
	}
	return validateOptionalWorkflowExecutionPolicy(r.ExecutionPolicy, "execution_policy")
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
	return validateOptionalWorkflowExecutionPolicy(metadata.ExecutionPolicy, "metadata.execution_policy")
}

func validateOptionalWorkflowExecutionPolicy(policy *WorkflowExecutionPolicy, field string) error {
	if policy == nil {
		return nil
	}
	return validateWorkflowExecutionPolicy(*policy, field)
}

func validateWorkflowExecutionPolicy(policy WorkflowExecutionPolicy, field string) error {
	customRefField := field + ".custom_ref"
	switch policy.Mode {
	case WorkflowExecutionPolicyNone, WorkflowExecutionPolicyHead, WorkflowExecutionPolicyDefaultBranch, WorkflowExecutionPolicyAsk:
		if policy.CustomRef != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, customRefField, "custom_ref is only valid for custom_ref execution policy")
		}
		return nil
	case WorkflowExecutionPolicyCustomRef:
		if policy.CustomRef == nil || strings.TrimSpace(*policy.CustomRef) == "" {
			return workflowRequestError(WorkflowRequestErrorRequired, customRefField, "custom_ref is required for custom_ref execution policy")
		}
		if *policy.CustomRef != strings.TrimSpace(*policy.CustomRef) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, customRefField, "custom_ref must not have leading or trailing whitespace")
		}
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, field+".mode", "execution policy mode is invalid")
	}
}

func validateWorkflowTaskExecutionTargetSelection(generation *string, selection *WorkflowTaskExecutionTargetSelection, allowUngeneratedSelection bool) error {
	if generation != nil {
		if strings.TrimSpace(*generation) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_generation", "selection_generation must be non-empty when present")
		}
		if *generation != strings.TrimSpace(*generation) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_generation", "selection_generation must not have leading or trailing whitespace")
		}
	}
	if selection == nil {
		if generation != nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "selection", "selection is required with selection_generation")
		}
		return nil
	}
	if generation == nil && !allowUngeneratedSelection {
		return workflowRequestError(WorkflowRequestErrorRequired, "selection_generation", "selection_generation is required with selection")
	}
	switch selection.Mode {
	case WorkflowTaskExecutionTargetSelectionNone, WorkflowTaskExecutionTargetSelectionHead, WorkflowTaskExecutionTargetSelectionDefaultBranch:
		if selection.CustomRef != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection.custom_ref", "custom_ref is only valid for custom_ref selection")
		}
		return nil
	case WorkflowTaskExecutionTargetSelectionCustomRef:
		if selection.CustomRef == nil || strings.TrimSpace(*selection.CustomRef) == "" {
			return workflowRequestError(WorkflowRequestErrorRequired, "selection.custom_ref", "custom_ref is required for custom_ref selection")
		}
		if *selection.CustomRef != strings.TrimSpace(*selection.CustomRef) {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection.custom_ref", "custom_ref must not have leading or trailing whitespace")
		}
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "selection.mode", "selection mode is invalid")
	}
}

func (requirement WorkflowTaskExecutionTargetSelectionRequired) Validate() error {
	if err := validateRequiredFields(
		requiredField("selection_required.task_id", requirement.TaskID),
		requiredField("selection_required.generation", requirement.Generation),
		requiredField("selection_required.source_workspace_id", requirement.SourceWorkspaceID),
	); err != nil {
		return err
	}
	if err := requirement.Source.Validate(); err != nil {
		return err
	}
	if err := requirement.ConfiguredPolicy.Validate(); err != nil {
		return err
	}
	if len(requirement.SupportedSelections) == 0 {
		return workflowRequestError(WorkflowRequestErrorRequired, "selection_required.supported_selections", "supported selections are required")
	}
	seen := make(map[WorkflowTaskExecutionTargetSelectionMode]struct{}, len(requirement.SupportedSelections))
	for _, mode := range requirement.SupportedSelections {
		if _, duplicate := seen[mode]; duplicate {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_required.supported_selections", "supported selections must not contain duplicates")
		}
		seen[mode] = struct{}{}
		if err := validateWorkflowTaskExecutionTargetSelection(nil, &WorkflowTaskExecutionTargetSelection{Mode: mode}, true); err != nil {
			if mode == WorkflowTaskExecutionTargetSelectionCustomRef {
				continue
			}
			return err
		}
	}
	if requirement.RecoveryCause != nil && strings.TrimSpace(string(*requirement.RecoveryCause)) == "" {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_required.recovery_cause", "recovery_cause must be non-empty when present")
	}
	return nil
}

func (source WorkflowTaskExecutionTargetSource) Validate() error {
	validateOptionalNonEmpty := func(field string, value *string) error {
		if value != nil && strings.TrimSpace(*value) == "" {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, field, field+" must be non-empty when present")
		}
		return nil
	}
	if err := validateOptionalNonEmpty("selection_required.source.named_ref", source.NamedRef); err != nil {
		return err
	}
	if err := validateOptionalNonEmpty("selection_required.source.commit", source.Commit); err != nil {
		return err
	}
	switch source.Kind {
	case WorkflowTaskExecutionTargetSourceNonGit:
		if source.NamedRef != nil || source.Commit != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_required.source", "non_git source cannot have Git facts")
		}
	case WorkflowTaskExecutionTargetSourceNamedRef:
		if source.NamedRef == nil || source.Commit == nil {
			return workflowRequestError(WorkflowRequestErrorRequired, "selection_required.source", "named_ref source requires named_ref and commit")
		}
	case WorkflowTaskExecutionTargetSourceDetachedCommit:
		if source.NamedRef != nil || source.Commit == nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_required.source", "detached_commit source requires only commit")
		}
	case WorkflowTaskExecutionTargetSourceUnavailable:
		if source.NamedRef != nil || source.Commit != nil {
			return workflowRequestError(WorkflowRequestErrorInvalidValue, "selection_required.source", "unavailable source cannot have Git facts")
		}
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "selection_required.source.kind", "source kind is invalid")
	}
	return nil
}

func (progress WorkflowTaskExecutionTargetMaterializationProgress) Validate() error {
	if err := validateRequired("in_progress.task_id", progress.TaskID); err != nil {
		return err
	}
	switch progress.Phase {
	case WorkflowTaskExecutionTargetMaterializationPhaseMaterializing, WorkflowTaskExecutionTargetMaterializationPhaseRecoveryQueued, WorkflowTaskExecutionTargetMaterializationPhaseRecovering:
		return nil
	default:
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "in_progress.phase", "materialization phase is invalid")
	}
}

func (conflict WorkflowTaskExecutionTargetNegotiationConflict) Validate() error {
	return validateRequired("conflict.task_id", conflict.TaskID)
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
	return validateRequiredFields(requiredField("project_id", r.ProjectID), requiredField("title", r.Title))
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

func (r WorkflowTaskStartRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	return validateWorkflowTaskExecutionTargetSelection(r.SelectionGeneration, r.Selection, true)
}

func (r WorkflowTaskResumeRequest) Validate() error {
	return validateRequired("task_id", r.TaskID)
}

func (r WorkflowTaskApproveRequest) Validate() error {
	if strings.TrimSpace(r.TaskTransitionID) != "" {
		if err := r.SetupOperationID.Validate(); err != nil {
			return err
		}
		return validateWorkflowTaskExecutionTargetSelection(r.SelectionGeneration, r.Selection, false)
	}
	if err := validateRequired("transition_id", r.TransitionID); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	return validateWorkflowTaskExecutionTargetSelection(r.SelectionGeneration, r.Selection, false)
}

func (r WorkflowTaskMoveRequest) Validate() error {
	if err := validateRequiredFields(requiredField("task_id", r.TaskID), requiredField("target_node_id", r.TargetNodeID)); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	return validateWorkflowTaskExecutionTargetSelection(r.SelectionGeneration, r.Selection, false)
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
	if r.SelectedOptionNumber < 0 {
		return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidMode, Field: "selected_option_number", Message: "selected_option_number must be non-negative"}
	}
	hasSelected := r.SelectedOptionNumber > 0
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
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if r.DonePreviewLimit < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "done_preview_limit", "done_preview_limit must be non-negative")
	}
	if r.PageSize < 0 {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_size", "page_size must be non-negative")
	}
	if strings.TrimSpace(r.PageToken) != r.PageToken {
		return workflowRequestError(WorkflowRequestErrorInvalidMode, "page_token", "page_token must not have leading or trailing whitespace")
	}
	return nil
}

func (r WorkflowTaskListRequest) Validate() error {
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
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
	if len(r.Sort) > WorkflowTaskListMaxSortSelectors {
		return workflowRequestError(WorkflowRequestErrorInvalidValue, "sort", fmt.Sprintf("sort must include at most %d fields", WorkflowTaskListMaxSortSelectors))
	}
	for index, statusKey := range r.StatusKeys {
		if !workflowkey.Valid(statusKey) {
			return workflowRequestError(WorkflowRequestErrorInvalidKey, fmt.Sprintf("status_keys[%d]", index), fmt.Sprintf("status_keys[%d] must %s", index, workflowkey.Description))
		}
	}
	for index, status := range r.RunStatuses {
		switch status {
		case WorkflowTaskRunStatusOpen, WorkflowTaskRunStatusRunning, WorkflowTaskRunStatusInterrupted, WorkflowTaskRunStatusDone, WorkflowTaskRunStatusCanceled:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("run_statuses[%d]", index), "run_status must be open, running, interrupted, done, or canceled")
		}
	}
	seenSortFields := map[WorkflowTaskListSortField]bool{}
	for index, sortSelector := range r.Sort {
		switch sortSelector.Field {
		case WorkflowTaskListSortFieldCreated, WorkflowTaskListSortFieldUpdated, WorkflowTaskListSortFieldStatus, WorkflowTaskListSortFieldRunCount, WorkflowTaskListSortFieldTitle:
		default:
			return workflowRequestError(WorkflowRequestErrorInvalidValue, fmt.Sprintf("sort[%d].field", index), "sort field must be created, updated, status, run_count, or title")
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
	if err := validateRequiredFields(requiredField("project_id", r.ProjectID), requiredField("workflow_id", r.WorkflowID), requiredField("node_id", r.NodeID)); err != nil {
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
