package worktreecontract

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"core/shared/runtimeids"
)

var ErrWorktreeNotFound = errors.New("worktree not found")
var ErrWorktreeBlocked = errors.New("worktree is blocked")
var ErrSessionWorktreeDeleting = errors.New("session worktree is being deleted; try again once deletion finishes")

func validateRequiredSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session_id is required")
	}
	return nil
}

// TopologyVariant describes the relationship between the live Git
// topology and Kent's persisted worktree metadata.
type TopologyVariant string

const (
	TopologyVariantRegistered TopologyVariant = "registered"
	TopologyVariantExternal   TopologyVariant = "external"
	TopologyVariantMissing    TopologyVariant = "missing"
)

type GitFacts struct {
	CanonicalRoot  string
	HeadObject     string
	BranchRef      *string
	BranchName     *string
	Detached       bool
	Bare           bool
	LockedReason   *string
	PrunableReason *string
	IsMain         bool
	PathAvailable  bool
}

type KentFacts struct {
	WorktreeID      string
	CanonicalRoot   string
	DisplayName     string
	Managed         bool
	CreatedBranch   bool
	OriginSessionID *string
}

type PathAvailability string

const (
	PathAvailabilityAvailable    PathAvailability = "available"
	PathAvailabilityMissing      PathAvailability = "missing"
	PathAvailabilityInaccessible PathAvailability = "inaccessible"
)

type RegisteredFacts struct {
	Git  GitFacts
	Kent KentFacts
}

type ExternalFacts struct {
	Git GitFacts
}

type MissingFacts struct {
	Kent KentFacts
}

// TopologyEntry is an exhaustive discriminated union. Exactly the
// payload corresponding to Variant must be present.
type TopologyEntry struct {
	Variant    TopologyVariant
	Registered *RegisteredFacts
	External   *ExternalFacts
	Missing    *MissingFacts
}

func (entry TopologyEntry) DeletionSelector() (string, error) {
	if err := entry.Validate(); err != nil {
		return "", err
	}
	switch entry.Variant {
	case TopologyVariantRegistered:
		if entry.Registered.Git.IsMain {
			return "", ErrWorktreeBlocked
		}
		return entry.Registered.Kent.WorktreeID, nil
	case TopologyVariantExternal:
		if entry.External.Git.IsMain {
			return "", ErrWorktreeBlocked
		}
		return entry.External.Git.CanonicalRoot, nil
	case TopologyVariantMissing:
		return entry.Missing.Kent.WorktreeID, nil
	default:
		return "", errors.New("worktree topology variant is invalid")
	}
}

type ListProjection struct {
	Selector         string
	IsCurrent        bool
	Switch           *SwitchOperation
	DeletePreview    *DeletePreviewOperation
	FallbackIdentity *string
}

type ListEntry struct {
	Topology   TopologyEntry
	Projection ListProjection
}

type SwitchOperationKind string

const (
	SwitchOperationEnter     SwitchOperationKind = "enter"
	SwitchOperationLeaveMain SwitchOperationKind = "leave"
)

type SwitchOperation struct {
	Kind     SwitchOperationKind
	Selector *string
}

type DeletePreviewOperation struct {
	Selector string
}

func ProjectListEntry(
	topology TopologyEntry,
	selector string,
	isCurrent bool,
	sessionScoped bool,
) (ListEntry, error) {
	projection, err := projectListProjection(topology, selector, isCurrent, sessionScoped)
	if err != nil {
		return ListEntry{}, err
	}
	return ListEntry{Topology: topology, Projection: projection}, nil
}

type StatusProblemKind string

const (
	StatusProblemRootMissing          StatusProblemKind = "root_missing"
	StatusProblemRootInaccessible     StatusProblemKind = "root_inaccessible"
	StatusProblemGitBindingMissing    StatusProblemKind = "git_binding_missing"
	StatusProblemGitBindingMismatched StatusProblemKind = "git_binding_mismatched"
	StatusProblemRecordedRefMissing   StatusProblemKind = "recorded_ref_missing"
)

type StatusProblem struct {
	Kind StatusProblemKind
	Root *string
	Ref  *string
}

// StatusTarget intentionally has no selector. Its display and branch
// facts describe the recorded target only; they are not topology selectors.
type StatusTarget struct {
	RecordedRoot      string
	ObservedRoot      *string
	DisplayName       *string
	RecordedBranchRef *string
	ObservedBranchRef *string
}

type StatusRequest struct {
	SessionID string
}

type StatusResponse struct {
	Target   SessionExecutionTarget
	Worktree StatusTarget
	Problems []StatusProblem
}

type BranchCleanupMode string

const (
	BranchCleanupModeRetain            BranchCleanupMode = "retain"
	BranchCleanupModeAutoIfKentCreated BranchCleanupMode = "auto_if_kent_created"
	BranchCleanupModeDeleteSafe        BranchCleanupMode = "delete_safe"
	BranchCleanupModeDeleteForce       BranchCleanupMode = "delete_force"
)

type BranchCleanupOutcomeKind string

const (
	BranchCleanupOutcomeNotRequested  BranchCleanupOutcomeKind = "not_requested"
	BranchCleanupOutcomeNotApplicable BranchCleanupOutcomeKind = "not_applicable"
	BranchCleanupOutcomeDeleted       BranchCleanupOutcomeKind = "deleted"
	BranchCleanupOutcomeRetained      BranchCleanupOutcomeKind = "retained"
)

type BranchCleanupOutcome struct {
	Kind       BranchCleanupOutcomeKind
	BranchName *string
	Diagnostic *string
}

type SelectorResolveRequest struct {
	SessionID string
	Selector  string
}

type SelectorResolveResponse struct {
	Worktree ListEntry
}

func (response SelectorResolveResponse) Validate() error {
	return response.Worktree.validateProjection(true)
}

type DeletePreviewRequest struct {
	SessionID string
	Selector  string
}

type DeletePreviewResponse struct {
	Worktree         TopologyEntry
	DeletionSelector string
	Cleanliness      DirtyState
}

// TransitionHeader is the shared execution identity for every
// operation that may switch a Session's worktree target.
type TransitionHeader struct {
	OperationID OperationID
	SessionID   string
	Origin      *RuntimeStepOrigin
}

type RuntimeStepOrigin struct {
	RunID  string
	StepID string
}

type EnterRequest struct {
	TransitionHeader
	Selector string
}

type LeaveRequest struct {
	TransitionHeader
}

type DeleteRequest struct {
	TransitionHeader
	Selector            string
	ForceFolderRemoval  bool
	BranchCleanupPolicy BranchCleanupMode
}

type ScheduledAcknowledgement struct {
	OperationID OperationID
}

type DeleteResultKind string

const (
	DeleteResultKindCompleted DeleteResultKind = "completed"
	DeleteResultKindScheduled DeleteResultKind = "scheduled"
)

type DeleteCompletedResult struct {
	Cleanup      BranchCleanupOutcome
	LeftoverRoot *string
}

type DeleteResult struct {
	Kind      DeleteResultKind
	Completed *DeleteCompletedResult
	Scheduled *ScheduledAcknowledgement
}

func (f GitFacts) Validate() error {
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

func (f KentFacts) Validate() error {
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

func (f RegisteredFacts) Validate() error {
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

func (f ExternalFacts) Validate() error {
	return f.Git.Validate()
}

func (f MissingFacts) Validate() error {
	return f.Kent.Validate()
}

func (entry TopologyEntry) Validate() error {
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
	case TopologyVariantRegistered:
		if entry.Registered == nil {
			return errors.New("registered topology entry requires registered payload")
		}
		return entry.Registered.Validate()
	case TopologyVariantExternal:
		if entry.External == nil {
			return errors.New("external topology entry requires external payload")
		}
		return entry.External.Validate()
	case TopologyVariantMissing:
		if entry.Missing == nil {
			return errors.New("missing topology entry requires missing payload")
		}
		return entry.Missing.Validate()
	default:
		return errors.New("worktree topology variant is invalid")
	}
}

func (p ListProjection) Validate() error {
	if strings.TrimSpace(p.Selector) == "" {
		return errors.New("worktree list selector is required")
	}
	if p.FallbackIdentity != nil && strings.TrimSpace(*p.FallbackIdentity) == "" {
		return errors.New("worktree fallback_identity must not be empty")
	}
	return nil
}

func (entry ListEntry) Validate() error {
	if err := entry.Topology.Validate(); err != nil {
		return err
	}
	return entry.Projection.Validate()
}

// IsCurrent remains a server-owned fact; deriving it here would duplicate
// server/worktree target matching inside the wire-contract validator.
func (entry ListEntry) validateProjection(sessionScoped bool) error {
	expected, err := projectListProjection(
		entry.Topology,
		entry.Projection.Selector,
		entry.Projection.IsCurrent && sessionScoped,
		sessionScoped,
	)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(entry.Projection, expected) {
		return errors.New("worktree projection contradicts topology or scope")
	}
	return nil
}

func projectListProjection(
	topology TopologyEntry,
	selector string,
	isCurrent bool,
	sessionScoped bool,
) (ListProjection, error) {
	if err := topology.Validate(); err != nil {
		return ListProjection{}, err
	}
	projection := ListProjection{Selector: selector, IsCurrent: isCurrent}
	var git *GitFacts
	switch topology.Variant {
	case TopologyVariantRegistered:
		git = &topology.Registered.Git
	case TopologyVariantExternal:
		git = &topology.External.Git
		if git.BranchName == nil {
			value := filepath.Base(git.CanonicalRoot)
			projection.FallbackIdentity = &value
		}
	}
	if !sessionScoped {
		projection.IsCurrent = false
		return projection, projection.Validate()
	}
	if git != nil && !isCurrent && git.PathAvailable {
		projection.Switch = &SwitchOperation{Kind: SwitchOperationLeaveMain}
		if !git.IsMain {
			projection.Switch.Kind = SwitchOperationEnter
			projection.Switch.Selector = &projection.Selector
		}
	}
	if selector, err := topology.DeletionSelector(); err == nil {
		projection.DeletePreview = &DeletePreviewOperation{Selector: selector}
	} else if !errors.Is(err, ErrWorktreeBlocked) {
		return ListProjection{}, err
	}
	return projection, projection.Validate()
}

func (r StatusRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

func (response StatusResponse) Validate() error {
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

func (target StatusTarget) Validate() error {
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

func (problem StatusProblem) Validate() error {
	switch problem.Kind {
	case StatusProblemRootMissing,
		StatusProblemRootInaccessible,
		StatusProblemGitBindingMissing,
		StatusProblemGitBindingMismatched:
		if problem.Root == nil || strings.TrimSpace(*problem.Root) == "" || problem.Ref != nil {
			return errors.New("root status problem requires root only")
		}
	case StatusProblemRecordedRefMissing:
		if problem.Ref == nil || strings.TrimSpace(*problem.Ref) == "" || problem.Root != nil {
			return errors.New("recorded ref status problem requires ref only")
		}
	default:
		return errors.New("worktree status problem kind is invalid")
	}
	return nil
}

func (policy BranchCleanupMode) Validate() error {
	switch policy {
	case BranchCleanupModeRetain,
		BranchCleanupModeAutoIfKentCreated,
		BranchCleanupModeDeleteSafe,
		BranchCleanupModeDeleteForce:
		return nil
	default:
		return errors.New("worktree branch cleanup policy is invalid")
	}
}

func (outcome BranchCleanupOutcome) Validate() error {
	switch outcome.Kind {
	case BranchCleanupOutcomeNotRequested, BranchCleanupOutcomeNotApplicable:
		if outcome.BranchName != nil || outcome.Diagnostic != nil {
			return errors.New("non-requested cleanup outcome cannot contain branch facts")
		}
	case BranchCleanupOutcomeDeleted:
		if outcome.BranchName == nil || strings.TrimSpace(*outcome.BranchName) == "" || outcome.Diagnostic != nil {
			return errors.New("deleted cleanup outcome requires branch_name only")
		}
	case BranchCleanupOutcomeRetained:
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

func (request SelectorResolveRequest) Validate() error {
	return validateWorktreeSelectorPreview(request.SessionID, request.Selector)
}

func (request DeletePreviewRequest) Validate() error {
	return validateWorktreeSelectorPreview(request.SessionID, request.Selector)
}

func validateWorktreeSelectorPreview(sessionID string, selector string) error {
	if err := validateRequiredSessionID(sessionID); err != nil {
		return err
	}
	if strings.TrimSpace(selector) == "" {
		return errors.New("selector is required")
	}
	return nil
}

func (response DeletePreviewResponse) Validate() error {
	deletionSelector, err := response.Worktree.DeletionSelector()
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.DeletionSelector) == "" {
		return errors.New("deletion_selector is required")
	}
	if response.DeletionSelector != deletionSelector {
		return errors.New("deletion_selector does not match worktree")
	}
	if err := ValidateDirtyState(
		response.Cleanliness.Kind,
		response.Cleanliness.DirtyFileCount,
		response.Cleanliness.UnknownCause,
	); err != nil {
		return err
	}
	if response.Worktree.Variant == TopologyVariantMissing &&
		response.Cleanliness.Kind != DirtyStateClean {
		return errors.New("missing worktree deletion preview must be clean")
	}
	return nil
}

func (header TransitionHeader) Validate() error {
	if err := header.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateRequiredSessionID(header.SessionID); err != nil {
		return err
	}
	if header.Origin != nil {
		return header.Origin.Validate()
	}
	return nil
}

func (origin RuntimeStepOrigin) Validate() error {
	if err := runtimeids.ValidateUUIDv4(origin.RunID, "run_id"); err != nil {
		return err
	}
	return runtimeids.ValidateUUIDv4(origin.StepID, "step_id")
}

func (request EnterRequest) Validate() error {
	if err := request.TransitionHeader.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.Selector) == "" {
		return errors.New("selector is required")
	}
	return nil
}

func (request LeaveRequest) Validate() error {
	return request.TransitionHeader.Validate()
}

func (request DeleteRequest) Validate() error {
	if err := request.TransitionHeader.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.Selector) == "" {
		return errors.New("selector is required")
	}
	return request.BranchCleanupPolicy.Validate()
}

func (ack ScheduledAcknowledgement) Validate() error {
	return ack.OperationID.Validate()
}

func (result DeleteCompletedResult) Validate() error {
	if err := result.Cleanup.Validate(); err != nil {
		return err
	}
	if result.LeftoverRoot != nil && strings.TrimSpace(*result.LeftoverRoot) == "" {
		return errors.New("leftover_root must not be empty")
	}
	return nil
}

func (result DeleteResult) Validate() error {
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
	case DeleteResultKindCompleted:
		if result.Completed == nil {
			return errors.New("completed delete result requires completed payload")
		}
		return result.Completed.Validate()
	case DeleteResultKindScheduled:
		if result.Scheduled == nil {
			return errors.New("scheduled delete result requires scheduled payload")
		}
		return result.Scheduled.Validate()
	default:
		return errors.New("worktree delete result kind is invalid")
	}
}

type ListRequest struct {
	SessionID string
}

type ListResponse struct {
	Target    SessionExecutionTarget
	Worktrees []ListEntry
}

func (response ListResponse) Validate() error {
	return validateSessionWorktreeResponse(response.Target, response.Worktrees...)
}

type WorkspaceListRequest struct {
	ProjectID   string
	WorkspaceID string
}

type WorkspaceListResponse struct {
	WorkspaceID string
	Worktrees   []ListEntry
}

func (response WorkspaceListResponse) Validate() error {
	if strings.TrimSpace(response.WorkspaceID) == "" {
		return errors.New("workspace_id is required")
	}
	return validateWorktreeProjections(response.Worktrees, false)
}

func validateSessionWorktreeResponse(target SessionExecutionTarget, entries ...ListEntry) error {
	if SessionExecutionTargetIsZero(target) {
		return errors.New("worktree response target is required")
	}
	return validateWorktreeProjections(entries, true)
}

func validateWorktreeProjections(entries []ListEntry, sessionScoped bool) error {
	for _, entry := range entries {
		if err := entry.validateProjection(sessionScoped); err != nil {
			return err
		}
	}
	return nil
}

type CreateTargetResolutionKind string

const (
	CreateTargetResolutionKindNewBranch      CreateTargetResolutionKind = "new_branch"
	CreateTargetResolutionKindExistingBranch CreateTargetResolutionKind = "existing_branch"
	CreateTargetResolutionKindDetachedRef    CreateTargetResolutionKind = "detached_ref"
)

type CreateTargetResolution struct {
	Input       string
	Kind        CreateTargetResolutionKind
	ResolvedRef string
}

type CreateTargetResolveRequest struct {
	SessionID string
	Target    string
}

type CreateTargetResolveResponse struct {
	Resolution CreateTargetResolution
}

type CreateRequest struct {
	SetupOperationID SetupOperationID
	SessionID        string
	BaseRef          string
	CreateBranch     bool
	BranchName       string
	RootPath         string
}

type CreateResponse struct {
	Target   SessionExecutionTarget
	Worktree ListEntry
}

func (response CreateResponse) Validate() error {
	return validateSessionWorktreeResponse(response.Target, response.Worktree)
}

func (r ListRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	return nil
}

func (r WorkspaceListRequest) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return errors.New("workspace_id is required")
	}
	return nil
}

func (r CreateTargetResolveRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Target) == "" {
		return errors.New("target is required")
	}
	return nil
}

func (r CreateRequest) Validate() error {
	if err := r.SetupOperationID.Validate(); err != nil {
		return NewCreateError(CreateErrorOwnerForm, err.Error(), err)
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return NewCreateError(CreateErrorOwnerForm, err.Error(), err)
	}
	specErr := ValidateCreateSpec(r.BaseRef, r.CreateBranch, r.BranchName)
	if specErr == nil {
		return nil
	}
	owner := ProjectCreateValidationOwner(specErr, r.CreateBranch)
	return NewCreateError(owner, specErr.Error(), specErr)
}
