package serverapi

import (
	"encoding/json"
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
	Root string                    `json:"root,omitempty"`
	Ref  string                    `json:"ref,omitempty"`
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

type WorktreeBranchCleanupPolicy string

const (
	WorktreeBranchCleanupPolicyRetain            WorktreeBranchCleanupPolicy = "retain"
	WorktreeBranchCleanupPolicyAutoIfKentCreated WorktreeBranchCleanupPolicy = "auto_if_kent_created"
	WorktreeBranchCleanupPolicyDeleteSafe        WorktreeBranchCleanupPolicy = "delete_safe"
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

type WorktreeOperationID uuid.UUID

func NewWorktreeOperationID() WorktreeOperationID {
	return WorktreeOperationID(uuid.New())
}

func ParseWorktreeOperationID(value string) (WorktreeOperationID, error) {
	parsed, err := parseWorktreeUUIDV4(value, "operation_id")
	if err != nil {
		return WorktreeOperationID{}, err
	}
	return WorktreeOperationID(parsed), nil
}

func (id WorktreeOperationID) String() string {
	value := uuid.UUID(id)
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func (id WorktreeOperationID) Validate() error {
	return validateWorktreeUUIDV4(uuid.UUID(id), "operation_id")
}

func (id WorktreeOperationID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.String())
}

func (id *WorktreeOperationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := ParseWorktreeOperationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type WorktreeOperationKind string

const (
	WorktreeOperationKindEnter  WorktreeOperationKind = "enter"
	WorktreeOperationKindLeave  WorktreeOperationKind = "leave"
	WorktreeOperationKindDelete WorktreeOperationKind = "delete"
)

const WorktreeOperationPayloadVersion1 int64 = 1

// WorktreeOperationPayload is the immutable, normalized client behavior used
// as the durable operation's idempotency payload.
type WorktreeOperationPayload struct {
	Version             int64                       `json:"version"`
	SessionID           string                      `json:"session_id"`
	Kind                WorktreeOperationKind       `json:"kind"`
	Selector            *string                     `json:"selector,omitempty"`
	ForceFolderRemoval  bool                        `json:"force_folder_removal"`
	BranchCleanupPolicy WorktreeBranchCleanupPolicy `json:"branch_cleanup_policy"`
}

type WorktreeSelectorPreviewRequest struct {
	SessionID string `json:"session_id"`
	Selector  string `json:"selector"`
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

// WorktreeDeleteOperationRequest is the typed transition request introduced
// alongside the legacy delete request. The service cutover replaces the
// legacy request after all clients move to this contract.
type WorktreeDeleteOperationRequest struct {
	OperationID         WorktreeOperationID         `json:"operation_id"`
	SessionID           string                      `json:"session_id"`
	Selector            string                      `json:"selector"`
	ForceFolderRemoval  bool                        `json:"force_folder_removal"`
	BranchCleanupPolicy WorktreeBranchCleanupPolicy `json:"branch_cleanup_policy"`
}

type WorktreeScheduledAcknowledgement struct {
	OperationID WorktreeOperationID `json:"operation_id"`
}

type WorktreeOperationExecutionMode string

const (
	WorktreeOperationExecutionModeSynchronous         WorktreeOperationExecutionMode = "synchronous"
	WorktreeOperationExecutionModeScheduledTransition WorktreeOperationExecutionMode = "scheduled_transition"
)

type WorktreeOperationLifecycleState string

const (
	WorktreeOperationLifecycleStateQueued    WorktreeOperationLifecycleState = "queued"
	WorktreeOperationLifecycleStateRunning   WorktreeOperationLifecycleState = "running"
	WorktreeOperationLifecycleStateCompleted WorktreeOperationLifecycleState = "completed"
	WorktreeOperationLifecycleStateFailed    WorktreeOperationLifecycleState = "failed"
)

func (mode WorktreeOperationExecutionMode) Validate() error {
	switch mode {
	case WorktreeOperationExecutionModeSynchronous, WorktreeOperationExecutionModeScheduledTransition:
		return nil
	default:
		return errors.New("worktree operation execution mode is invalid")
	}
}

func (state WorktreeOperationLifecycleState) Validate() error {
	switch state {
	case WorktreeOperationLifecycleStateQueued,
		WorktreeOperationLifecycleStateRunning,
		WorktreeOperationLifecycleStateCompleted,
		WorktreeOperationLifecycleStateFailed:
		return nil
	default:
		return errors.New("worktree operation lifecycle state is invalid")
	}
}

type WorktreeOperationExpectedTarget struct {
	WorktreeID    *WorktreeOperationID `json:"worktree_id,omitempty"`
	CanonicalRoot string               `json:"canonical_root"`
}

func (target WorktreeOperationExpectedTarget) Validate() error {
	if strings.TrimSpace(target.CanonicalRoot) == "" {
		return errors.New("worktree operation expected canonical_root is required")
	}
	if target.WorktreeID != nil {
		return target.WorktreeID.Validate()
	}
	return nil
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

type WorktreeOperationEventKind string

const (
	WorktreeOperationEventKindAccepted  WorktreeOperationEventKind = "accepted"
	WorktreeOperationEventKindCompleted WorktreeOperationEventKind = "completed"
	WorktreeOperationEventKindFailed    WorktreeOperationEventKind = "failed"
)

type WorktreeOperationResult struct {
	Target *clientui.SessionExecutionTarget `json:"target,omitempty"`
	Delete *WorktreeDeleteCompletedResult   `json:"delete,omitempty"`
}

type WorktreeOperationFailureKind string

const (
	WorktreeOperationFailureKindExecutionFailed        WorktreeOperationFailureKind = "execution_failed"
	WorktreeOperationFailureKindExecutionIndeterminate WorktreeOperationFailureKind = "execution_indeterminate"
)

type WorktreeOperationFailure struct {
	Kind       WorktreeOperationFailureKind `json:"kind"`
	Diagnostic string                       `json:"diagnostic"`
}

type WorktreeOperationEvent struct {
	OperationID WorktreeOperationID        `json:"operation_id"`
	Version     int64                      `json:"version"`
	Kind        WorktreeOperationEventKind `json:"kind"`
	Result      *WorktreeOperationResult   `json:"result,omitempty"`
	Failure     *WorktreeOperationFailure  `json:"failure,omitempty"`
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
		if strings.TrimSpace(problem.Root) == "" || strings.TrimSpace(problem.Ref) != "" {
			return errors.New("root status problem requires root only")
		}
	case WorktreeStatusProblemRecordedRefMissing:
		if strings.TrimSpace(problem.Ref) == "" || strings.TrimSpace(problem.Root) != "" {
			return errors.New("recorded ref status problem requires ref only")
		}
	default:
		return errors.New("worktree status problem kind is invalid")
	}
	return nil
}

func (policy WorktreeBranchCleanupPolicy) Validate() error {
	switch policy {
	case WorktreeBranchCleanupPolicyRetain,
		WorktreeBranchCleanupPolicyAutoIfKentCreated,
		WorktreeBranchCleanupPolicyDeleteSafe:
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

func (payload WorktreeOperationPayload) Validate() error {
	if payload.Version != WorktreeOperationPayloadVersion1 {
		return errors.New("worktree operation payload version is invalid")
	}
	if err := validateRequiredSessionID(payload.SessionID); err != nil {
		return err
	}
	if err := payload.BranchCleanupPolicy.Validate(); err != nil {
		return err
	}
	if payload.Selector != nil && strings.TrimSpace(*payload.Selector) == "" {
		return errors.New("selector must not be empty")
	}
	switch payload.Kind {
	case WorktreeOperationKindEnter:
		if payload.Selector == nil {
			return errors.New("enter operation requires selector")
		}
		if payload.ForceFolderRemoval || payload.BranchCleanupPolicy != WorktreeBranchCleanupPolicyRetain {
			return errors.New("enter operation cannot contain delete behavior")
		}
	case WorktreeOperationKindLeave:
		if payload.Selector != nil || payload.ForceFolderRemoval || payload.BranchCleanupPolicy != WorktreeBranchCleanupPolicyRetain {
			return errors.New("leave operation cannot contain target or delete behavior")
		}
	case WorktreeOperationKindDelete:
		if payload.Selector == nil {
			return errors.New("delete operation requires selector")
		}
	default:
		return errors.New("worktree operation kind is invalid")
	}
	return nil
}

func (payload WorktreeOperationPayload) Equal(other WorktreeOperationPayload) bool {
	if payload.Version != other.Version ||
		payload.SessionID != other.SessionID ||
		payload.Kind != other.Kind ||
		payload.ForceFolderRemoval != other.ForceFolderRemoval ||
		payload.BranchCleanupPolicy != other.BranchCleanupPolicy {
		return false
	}
	if payload.Selector == nil || other.Selector == nil {
		return payload.Selector == other.Selector
	}
	return *payload.Selector == *other.Selector
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

func (request WorktreeDeleteOperationRequest) Validate() error {
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

func (event WorktreeOperationEvent) Validate() error {
	if err := event.OperationID.Validate(); err != nil {
		return err
	}
	if event.Version <= 0 {
		return errors.New("worktree operation lifecycle version must be positive")
	}
	switch event.Kind {
	case WorktreeOperationEventKindAccepted:
		if event.Result != nil || event.Failure != nil {
			return errors.New("accepted worktree operation event cannot contain terminal facts")
		}
	case WorktreeOperationEventKindCompleted:
		if event.Result == nil || event.Failure != nil {
			return errors.New("completed worktree operation event requires result only")
		}
		if err := event.Result.Validate(); err != nil {
			return err
		}
	case WorktreeOperationEventKindFailed:
		if event.Result != nil || event.Failure == nil {
			return errors.New("failed worktree operation event requires failure only")
		}
		if err := event.Failure.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("worktree operation event kind is invalid")
	}
	return nil
}

func (result WorktreeOperationResult) Validate() error {
	payloadCount := 0
	if result.Target != nil {
		payloadCount++
	}
	if result.Delete != nil {
		payloadCount++
	}
	if payloadCount != 1 {
		return errors.New("worktree operation result requires exactly one payload")
	}
	if result.Delete != nil {
		return result.Delete.Validate()
	}
	return nil
}

func (failure WorktreeOperationFailure) Validate() error {
	switch failure.Kind {
	case WorktreeOperationFailureKindExecutionFailed, WorktreeOperationFailureKindExecutionIndeterminate:
	default:
		return errors.New("worktree operation failure kind is invalid")
	}
	if strings.TrimSpace(failure.Diagnostic) == "" {
		return errors.New("worktree operation failure requires diagnostic")
	}
	return nil
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

type WorktreeView struct {
	WorktreeID      string `json:"worktree_id"`
	DisplayName     string `json:"display_name"`
	CanonicalRoot   string `json:"canonical_root"`
	Availability    string `json:"availability"`
	BranchRef       string `json:"branch_ref,omitempty"`
	BranchName      string `json:"branch_name,omitempty"`
	Detached        bool   `json:"detached,omitempty"`
	LockedReason    string `json:"locked_reason,omitempty"`
	PrunableReason  string `json:"prunable_reason,omitempty"`
	DirtyFileCount  int    `json:"dirty_file_count,omitempty"`
	IsMain          bool   `json:"is_main,omitempty"`
	IsCurrent       bool   `json:"is_current,omitempty"`
	Managed         bool   `json:"managed,omitempty"`
	CreatedBranch   bool   `json:"created_branch,omitempty"`
	OriginSessionID string `json:"origin_session_id,omitempty"`
}

type WorktreeListRequest struct {
	SessionID         string `json:"session_id"`
	IncludeDirtyCount bool   `json:"include_dirty_count,omitempty"`
}

type WorktreeListResponse struct {
	Target    clientui.SessionExecutionTarget `json:"target"`
	Worktrees []WorktreeView                  `json:"worktrees"`
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
	Target        clientui.SessionExecutionTarget `json:"target"`
	Worktree      WorktreeView                    `json:"worktree"`
	CreatedBranch bool                            `json:"created_branch"`
}

type WorktreeSwitchRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	WorktreeID      string `json:"worktree_id"`
}

type WorktreeSwitchResponse struct {
	Target   clientui.SessionExecutionTarget `json:"target"`
	Worktree WorktreeView                    `json:"worktree"`
}

type WorktreeDeleteRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	WorktreeID      string `json:"worktree_id"`
	DeleteBranch    bool   `json:"delete_branch,omitempty"`
}

type WorktreeDeleteResponse struct {
	Target               clientui.SessionExecutionTarget `json:"target"`
	Worktree             WorktreeView                    `json:"worktree"`
	BranchDeleted        bool                            `json:"branch_deleted,omitempty"`
	BranchCleanupMessage string                          `json:"branch_cleanup_message,omitempty"`
}

func (r WorktreeListRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
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

func (r WorktreeSwitchRequest) Validate() error {
	if err := validateClientRequestID(r.ClientRequestID); err != nil {
		return err
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorktreeID) == "" {
		return errors.New("worktree_id is required")
	}
	return nil
}

func (r WorktreeDeleteRequest) Validate() error {
	if err := validateClientRequestID(r.ClientRequestID); err != nil {
		return err
	}
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorktreeID) == "" {
		return errors.New("worktree_id is required")
	}
	return nil
}
