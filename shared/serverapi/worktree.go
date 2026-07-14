package serverapi

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"

	"github.com/google/uuid"
)

var ErrWorktreeNotFound = errors.New("worktree not found")
var ErrWorktreeBlocked = errors.New("worktree is blocked")
var ErrSessionWorktreeDeleting = errors.New("session worktree is being deleted; try again once deletion finishes")

// WorktreeTopologyVariant describes the relationship between the live Git
// topology and Kent's persisted worktree metadata.
type WorktreeTopologyVariant string

const (
	WorktreeTopologyVariantRegistered WorktreeTopologyVariant = "registered"
	WorktreeTopologyVariantExternal   WorktreeTopologyVariant = "external"
	WorktreeTopologyVariantMissing    WorktreeTopologyVariant = "missing"
)

type WorktreeGitFacts struct {
	CanonicalRoot  string  `json:"canonical_root"`
	HeadObject     string  `json:"head_object"`
	BranchRef      *string `json:"branch_ref,omitempty"`
	BranchName     *string `json:"branch_name,omitempty"`
	Detached       bool    `json:"detached"`
	Bare           bool    `json:"bare"`
	LockedReason   *string `json:"locked_reason,omitempty"`
	PrunableReason *string `json:"prunable_reason,omitempty"`
	IsMain         bool    `json:"is_main"`
	PathAvailable  bool    `json:"path_available"`
}

type WorktreeKentFacts struct {
	WorktreeID      string  `json:"worktree_id"`
	CanonicalRoot   string  `json:"canonical_root"`
	DisplayName     string  `json:"display_name"`
	Managed         bool    `json:"managed"`
	CreatedBranch   bool    `json:"created_branch"`
	OriginSessionID *string `json:"origin_session_id,omitempty"`
}

type WorktreePathAvailability string

const (
	WorktreePathAvailabilityAvailable    WorktreePathAvailability = "available"
	WorktreePathAvailabilityMissing      WorktreePathAvailability = "missing"
	WorktreePathAvailabilityInaccessible WorktreePathAvailability = "inaccessible"
)

type WorktreeRegisteredFacts struct {
	Git  WorktreeGitFacts  `json:"git"`
	Kent WorktreeKentFacts `json:"kent"`
}

type WorktreeExternalFacts struct {
	Git WorktreeGitFacts `json:"git"`
}

type WorktreeMissingFacts struct {
	Kent WorktreeKentFacts `json:"kent"`
}

// WorktreeTopologyEntry is an exhaustive discriminated union. Exactly the
// payload corresponding to Variant must be present.
type WorktreeTopologyEntry struct {
	Variant    WorktreeTopologyVariant  `json:"variant"`
	Registered *WorktreeRegisteredFacts `json:"registered,omitempty"`
	External   *WorktreeExternalFacts   `json:"external,omitempty"`
	Missing    *WorktreeMissingFacts    `json:"missing,omitempty"`
}

type WorktreeListProjection struct {
	Selector  string `json:"selector"`
	IsCurrent bool   `json:"is_current"`
}

type WorktreeListEntry struct {
	Topology   WorktreeTopologyEntry  `json:"topology"`
	Projection WorktreeListProjection `json:"projection"`
}

type WorktreeStatusProblemKind string

const (
	WorktreeStatusProblemRootMissing          WorktreeStatusProblemKind = "root_missing"
	WorktreeStatusProblemRootInaccessible     WorktreeStatusProblemKind = "root_inaccessible"
	WorktreeStatusProblemGitBindingMissing    WorktreeStatusProblemKind = "git_binding_missing"
	WorktreeStatusProblemGitBindingMismatched WorktreeStatusProblemKind = "git_binding_mismatched"
	WorktreeStatusProblemRecordedRefMissing   WorktreeStatusProblemKind = "recorded_ref_missing"
)

type WorktreeStatusProblem struct {
	Kind WorktreeStatusProblemKind `json:"kind"`
	Root *string                   `json:"root,omitempty"`
	Ref  *string                   `json:"ref,omitempty"`
}

// WorktreeStatusTarget intentionally has no selector. Its display and branch
// facts describe the recorded target only; they are not topology selectors.
type WorktreeStatusTarget struct {
	RecordedRoot      string  `json:"recorded_root"`
	ObservedRoot      *string `json:"observed_root,omitempty"`
	DisplayName       *string `json:"display_name,omitempty"`
	RecordedBranchRef *string `json:"recorded_branch_ref,omitempty"`
	ObservedBranchRef *string `json:"observed_branch_ref,omitempty"`
}

type WorktreeStatusRequest struct {
	SessionID string `json:"session_id"`
}

type WorktreeStatusResponse struct {
	Target   clientui.SessionExecutionTarget `json:"target"`
	Worktree WorktreeStatusTarget            `json:"worktree"`
	Problems []WorktreeStatusProblem         `json:"problems"`
}

type WorktreeBranchCleanupMode string

const (
	WorktreeBranchCleanupModeRetain            WorktreeBranchCleanupMode = "retain"
	WorktreeBranchCleanupModeAutoIfKentCreated WorktreeBranchCleanupMode = "auto_if_kent_created"
	WorktreeBranchCleanupModeDeleteSafe        WorktreeBranchCleanupMode = "delete_safe"
)

type WorktreeBranchCleanupOutcomeKind string

const (
	WorktreeBranchCleanupOutcomeNotRequested  WorktreeBranchCleanupOutcomeKind = "not_requested"
	WorktreeBranchCleanupOutcomeNotApplicable WorktreeBranchCleanupOutcomeKind = "not_applicable"
	WorktreeBranchCleanupOutcomeDeleted       WorktreeBranchCleanupOutcomeKind = "deleted"
	WorktreeBranchCleanupOutcomeRetained      WorktreeBranchCleanupOutcomeKind = "retained"
)

type WorktreeBranchCleanupOutcome struct {
	Kind       WorktreeBranchCleanupOutcomeKind `json:"kind"`
	BranchName *string                          `json:"branch_name,omitempty"`
	Diagnostic *string                          `json:"diagnostic,omitempty"`
}

type WorktreeOperationID = clientui.WorktreeTransitionID

func NewWorktreeOperationID() WorktreeOperationID {
	return clientui.NewWorktreeTransitionID()
}

func ParseWorktreeOperationID(value string) (WorktreeOperationID, error) {
	return clientui.ParseWorktreeTransitionID(value)
}

type WorktreeSelectorPreviewRequest struct {
	SessionID string `json:"session_id"`
	Selector  string `json:"selector"`
}

type WorktreeSelectorPreviewResponse struct {
	Worktree WorktreeTopologyEntry `json:"worktree"`
	Selector string                `json:"selector"`
}

type WorktreeEnterRequest struct {
	OperationID WorktreeOperationID `json:"operation_id"`
	SessionID   string              `json:"session_id"`
	Selector    string              `json:"selector"`
}

type WorktreeLeaveRequest struct {
	OperationID WorktreeOperationID `json:"operation_id"`
	SessionID   string              `json:"session_id"`
}

type WorktreeDeleteRequest struct {
	OperationID         WorktreeOperationID       `json:"operation_id"`
	SessionID           string                    `json:"session_id"`
	Selector            string                    `json:"selector"`
	ForceFolderRemoval  bool                      `json:"force_folder_removal"`
	BranchCleanupPolicy WorktreeBranchCleanupMode `json:"branch_cleanup_policy"`
}

type WorktreeScheduledAcknowledgement struct {
	OperationID WorktreeOperationID `json:"operation_id"`
}

type WorktreeDeleteResultKind string

const (
	WorktreeDeleteResultKindCompleted WorktreeDeleteResultKind = "completed"
	WorktreeDeleteResultKindScheduled WorktreeDeleteResultKind = "scheduled"
)

type WorktreeDeleteCompletedResult struct {
	Cleanup      WorktreeBranchCleanupOutcome `json:"cleanup"`
	LeftoverRoot *string                      `json:"leftover_root,omitempty"`
}

type WorktreeDeleteResult struct {
	Kind      WorktreeDeleteResultKind          `json:"kind"`
	Completed *WorktreeDeleteCompletedResult    `json:"completed,omitempty"`
	Scheduled *WorktreeScheduledAcknowledgement `json:"scheduled,omitempty"`
}

func (f WorktreeGitFacts) Validate() error {
	if strings.TrimSpace(f.CanonicalRoot) == "" {
		return errors.New("git canonical_root is required")
	}
	if strings.TrimSpace(f.HeadObject) == "" {
		return errors.New("git head_object is required")
	}
	for _, fact := range []*string{f.BranchRef, f.BranchName, f.LockedReason, f.PrunableReason} {
		if fact != nil && strings.TrimSpace(*fact) == "" {
			return errors.New("optional git facts must not be empty")
		}
	}
	return nil
}

func (f WorktreeKentFacts) Validate() error {
	if strings.TrimSpace(f.WorktreeID) == "" {
		return errors.New("kent worktree_id is required")
	}
	if strings.TrimSpace(f.CanonicalRoot) == "" || strings.TrimSpace(f.DisplayName) == "" {
		return errors.New("kent canonical_root and display_name are required")
	}
	if f.OriginSessionID != nil && strings.TrimSpace(*f.OriginSessionID) == "" {
		return errors.New("origin_session_id must not be empty")
	}
	return nil
}

func (f WorktreeRegisteredFacts) Validate() error {
	if err := f.Git.Validate(); err != nil {
		return err
	}
	if err := f.Kent.Validate(); err != nil {
		return err
	}
	if f.Git.CanonicalRoot != f.Kent.CanonicalRoot {
		return errors.New("registered git and kent canonical roots must match")
	}
	return nil
}

func (f WorktreeExternalFacts) Validate() error {
	return f.Git.Validate()
}

func (f WorktreeMissingFacts) Validate() error {
	return f.Kent.Validate()
}

func (entry WorktreeTopologyEntry) Validate() error {
	payloadCount := 0
	if entry.Registered != nil {
		payloadCount++
	}
	if entry.External != nil {
		payloadCount++
	}
	if entry.Missing != nil {
		payloadCount++
	}
	if payloadCount != 1 {
		return errors.New("worktree topology entry requires exactly one payload")
	}
	switch entry.Variant {
	case WorktreeTopologyVariantRegistered:
		if entry.Registered == nil {
			return errors.New("registered topology entry requires registered payload")
		}
		return entry.Registered.Validate()
	case WorktreeTopologyVariantExternal:
		if entry.External == nil {
			return errors.New("external topology entry requires external payload")
		}
		return entry.External.Validate()
	case WorktreeTopologyVariantMissing:
		if entry.Missing == nil {
			return errors.New("missing topology entry requires missing payload")
		}
		return entry.Missing.Validate()
	default:
		return errors.New("worktree topology variant is invalid")
	}
}

func (p WorktreeListProjection) Validate() error {
	if strings.TrimSpace(p.Selector) == "" {
		return errors.New("worktree list selector is required")
	}
	return nil
}

func (entry WorktreeListEntry) Validate() error {
	if err := entry.Topology.Validate(); err != nil {
		return err
	}
	return entry.Projection.Validate()
}

func (r WorktreeStatusRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

func (response WorktreeStatusResponse) Validate() error {
	if err := response.Worktree.Validate(); err != nil {
		return err
	}
	for _, problem := range response.Problems {
		if err := problem.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (target WorktreeStatusTarget) Validate() error {
	if strings.TrimSpace(target.RecordedRoot) == "" {
		return errors.New("recorded_root is required")
	}
	for _, fact := range []*string{target.ObservedRoot, target.DisplayName, target.RecordedBranchRef, target.ObservedBranchRef} {
		if fact != nil && strings.TrimSpace(*fact) == "" {
			return errors.New("optional status facts must not be empty")
		}
	}
	return nil
}

func (problem WorktreeStatusProblem) Validate() error {
	switch problem.Kind {
	case WorktreeStatusProblemRootMissing,
		WorktreeStatusProblemRootInaccessible,
		WorktreeStatusProblemGitBindingMissing,
		WorktreeStatusProblemGitBindingMismatched:
		if problem.Root == nil || strings.TrimSpace(*problem.Root) == "" || problem.Ref != nil {
			return errors.New("root status problem requires root only")
		}
	case WorktreeStatusProblemRecordedRefMissing:
		if problem.Ref == nil || strings.TrimSpace(*problem.Ref) == "" || problem.Root != nil {
			return errors.New("recorded ref status problem requires ref only")
		}
	default:
		return errors.New("worktree status problem kind is invalid")
	}
	return nil
}

func (policy WorktreeBranchCleanupMode) Validate() error {
	switch policy {
	case WorktreeBranchCleanupModeRetain,
		WorktreeBranchCleanupModeAutoIfKentCreated,
		WorktreeBranchCleanupModeDeleteSafe:
		return nil
	default:
		return errors.New("worktree branch cleanup policy is invalid")
	}
}

func (outcome WorktreeBranchCleanupOutcome) Validate() error {
	switch outcome.Kind {
	case WorktreeBranchCleanupOutcomeNotRequested, WorktreeBranchCleanupOutcomeNotApplicable:
		if outcome.BranchName != nil || outcome.Diagnostic != nil {
			return errors.New("non-requested cleanup outcome cannot contain branch facts")
		}
	case WorktreeBranchCleanupOutcomeDeleted:
		if outcome.BranchName == nil || strings.TrimSpace(*outcome.BranchName) == "" || outcome.Diagnostic != nil {
			return errors.New("deleted cleanup outcome requires branch_name only")
		}
	case WorktreeBranchCleanupOutcomeRetained:
		if outcome.BranchName == nil || strings.TrimSpace(*outcome.BranchName) == "" {
			return errors.New("retained cleanup outcome requires branch_name")
		}
		if outcome.Diagnostic != nil && strings.TrimSpace(*outcome.Diagnostic) == "" {
			return errors.New("retained cleanup diagnostic must not be empty")
		}
	default:
		return errors.New("worktree branch cleanup outcome kind is invalid")
	}
	return nil
}

func (request WorktreeSelectorPreviewRequest) Validate() error {
	if err := validateRequiredSessionID(request.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(request.Selector) == "" {
		return errors.New("selector is required")
	}
	return nil
}

func (request WorktreeEnterRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	return (WorktreeSelectorPreviewRequest{
		SessionID: request.SessionID,
		Selector:  request.Selector,
	}).Validate()
}

func (request WorktreeLeaveRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	return validateRequiredSessionID(request.SessionID)
}

func (request WorktreeDeleteRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := (WorktreeSelectorPreviewRequest{
		SessionID: request.SessionID,
		Selector:  request.Selector,
	}).Validate(); err != nil {
		return err
	}
	return request.BranchCleanupPolicy.Validate()
}

func (ack WorktreeScheduledAcknowledgement) Validate() error {
	return ack.OperationID.Validate()
}

func (result WorktreeDeleteCompletedResult) Validate() error {
	if err := result.Cleanup.Validate(); err != nil {
		return err
	}
	if result.LeftoverRoot != nil && strings.TrimSpace(*result.LeftoverRoot) == "" {
		return errors.New("leftover_root must not be empty")
	}
	return nil
}

func (result WorktreeDeleteResult) Validate() error {
	payloadCount := 0
	if result.Completed != nil {
		payloadCount++
	}
	if result.Scheduled != nil {
		payloadCount++
	}
	if payloadCount != 1 {
		return errors.New("worktree delete result requires exactly one payload")
	}
	switch result.Kind {
	case WorktreeDeleteResultKindCompleted:
		if result.Completed == nil {
			return errors.New("completed delete result requires completed payload")
		}
		return result.Completed.Validate()
	case WorktreeDeleteResultKindScheduled:
		if result.Scheduled == nil {
			return errors.New("scheduled delete result requires scheduled payload")
		}
		return result.Scheduled.Validate()
	default:
		return errors.New("worktree delete result kind is invalid")
	}
}

func parseWorktreeUUIDV4(value string, field string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be a UUID v4: %w", field, err)
	}
	if err := validateWorktreeUUIDV4(parsed, field); err != nil {
		return uuid.Nil, err
	}
	return parsed, nil
}

func validateWorktreeUUIDV4(value uuid.UUID, field string) error {
	if value == uuid.Nil {
		return fmt.Errorf("%s is required", field)
	}
	if value.Version() != 4 {
		return fmt.Errorf("%s must be a UUID v4", field)
	}
	return nil
}

type WorktreeListRequest struct {
	SessionID string `json:"session_id"`
}

type WorktreeListResponse struct {
	Target    clientui.SessionExecutionTarget `json:"target"`
	Worktrees []WorktreeListEntry             `json:"worktrees"`
}

type WorktreeWorkspaceListRequest struct {
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
}

type WorktreeWorkspaceListResponse struct {
	WorkspaceID string              `json:"workspace_id"`
	Worktrees   []WorktreeListEntry `json:"worktrees"`
}

type WorktreeCreateTargetResolutionKind string

const (
	WorktreeCreateTargetResolutionKindNewBranch      WorktreeCreateTargetResolutionKind = "new_branch"
	WorktreeCreateTargetResolutionKindExistingBranch WorktreeCreateTargetResolutionKind = "existing_branch"
	WorktreeCreateTargetResolutionKindDetachedRef    WorktreeCreateTargetResolutionKind = "detached_ref"
)

type WorktreeCreateTargetResolution struct {
	Input       string                             `json:"input"`
	Kind        WorktreeCreateTargetResolutionKind `json:"kind"`
	ResolvedRef string                             `json:"resolved_ref,omitempty"`
}

type WorktreeCreateTargetResolveRequest struct {
	SessionID string `json:"session_id"`
	Target    string `json:"target"`
}

type WorktreeCreateTargetResolveResponse struct {
	Resolution WorktreeCreateTargetResolution `json:"resolution"`
}

type WorktreeCreateRequest struct {
	ClientRequestID  string                   `json:"client_request_id"`
	SetupOperationID WorktreeSetupOperationID `json:"setup_operation_id"`
	SessionID        string                   `json:"session_id"`
	BaseRef          string                   `json:"base_ref,omitempty"`
	CreateBranch     bool                     `json:"create_branch,omitempty"`
	BranchName       string                   `json:"branch_name,omitempty"`
	RootPath         string                   `json:"root_path,omitempty"`
}

type WorktreeCreateResponse struct {
	Target   clientui.SessionExecutionTarget `json:"target"`
	Worktree WorktreeListEntry               `json:"worktree"`
}

func (r WorktreeListRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	return nil
}

func (r WorktreeWorkspaceListRequest) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return errors.New("workspace_id is required")
	}
	return nil
}

func (r WorktreeCreateTargetResolveRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Target) == "" {
		return errors.New("target is required")
	}
	return nil
}

func (r WorktreeCreateRequest) Validate() error {
	if err := validateClientRequestID(r.ClientRequestID); err != nil {
		return err
	}
	if err := r.SetupOperationID.Validate(); err != nil {
		return err
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.CreateBranch {
		if strings.TrimSpace(r.BaseRef) == "" {
			return errors.New("base_ref is required when create_branch=true")
		}
		if strings.TrimSpace(r.BranchName) == "" {
			return errors.New("branch_name is required when create_branch=true")
		}
		return nil
	}
	if strings.TrimSpace(r.BaseRef) == "" {
		return errors.New("base_ref is required when create_branch=false")
	}
	if strings.TrimSpace(r.BranchName) != "" {
		return errors.New("branch_name must be empty when create_branch=false")
	}
	return nil
}
