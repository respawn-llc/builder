package workflow

import (
	"errors"
	"fmt"
	"strings"
)

type ExecutionTargetSourceKind string

const (
	ExecutionTargetSourceNamedRef       ExecutionTargetSourceKind = "named_ref"
	ExecutionTargetSourceDetachedCommit ExecutionTargetSourceKind = "detached_commit"
)

type ExecutionTargetState string

const (
	ExecutionTargetStateInitialProvisioning  ExecutionTargetState = "initial_provisioning"
	ExecutionTargetStateLocked               ExecutionTargetState = "locked"
	ExecutionTargetStateLockedReprovisioning ExecutionTargetState = "locked_reprovisioning"
)

type ExecutionTargetSetupState string

const (
	ExecutionTargetSetupNotApplicable ExecutionTargetSetupState = "not_applicable"
	ExecutionTargetSetupPending       ExecutionTargetSetupState = "pending"
	ExecutionTargetSetupRunning       ExecutionTargetSetupState = "running"
	ExecutionTargetSetupSucceeded     ExecutionTargetSetupState = "succeeded"
	ExecutionTargetSetupFailed        ExecutionTargetSetupState = "failed"
)

type ExecutionTargetClaimPhase string

const (
	ExecutionTargetClaimMaterializing  ExecutionTargetClaimPhase = "materializing"
	ExecutionTargetClaimRecoveryQueued ExecutionTargetClaimPhase = "recovery_queued"
	ExecutionTargetClaimRecovering     ExecutionTargetClaimPhase = "recovering"
)

type ExecutionTargetRecoveryDisposition string

const (
	ExecutionTargetRecoveryAvailable      ExecutionTargetRecoveryDisposition = "available"
	ExecutionTargetRecoveryManualRecovery ExecutionTargetRecoveryDisposition = "manual_recovery"
)

type ExecutionTargetRecoveryCause string

const (
	ExecutionTargetRecoveryCauseAmbiguousProvisioning ExecutionTargetRecoveryCause = "ambiguous_provisioning"
	ExecutionTargetRecoveryCauseAmbiguousWorktree     ExecutionTargetRecoveryCause = "ambiguous_worktree"
	ExecutionTargetRecoveryCauseInspectionFailed      ExecutionTargetRecoveryCause = "inspection_failed"
	ExecutionTargetRecoveryCauseDeadlineExceeded      ExecutionTargetRecoveryCause = "deadline_exceeded"
	ExecutionTargetRecoveryCauseMissingManagedRoot    ExecutionTargetRecoveryCause = "missing_managed_root"
	ExecutionTargetRecoveryCauseUnsupportedState      ExecutionTargetRecoveryCause = "unsupported_recovery_state"
)

type ExecutionTargetResolvedSource struct {
	Kind     ExecutionTargetSourceKind
	NamedRef *string
	Commit   string
}

type ExecutionTargetClaim struct {
	Generation string
	Phase      ExecutionTargetClaimPhase
}

func (claim ExecutionTargetClaim) Validate() error {
	return validateExecutionTargetClaim(&claim)
}

type ExecutionTargetLinkedWorktreeOwnership struct {
	CommonDir  string
	AdminEntry string
	GitDir     string
	HeadRef    string
}

type ExecutionTarget struct {
	TaskID                      TaskID
	Policy                      ExecutionPolicyMode
	RequestedCustomRef          *string
	ResolvedSource              *ExecutionTargetResolvedSource
	State                       ExecutionTargetState
	IntendedWorktreeRoot        *string
	ProvisioningGeneration      *string
	SetupProvisioningGeneration *string
	SetupState                  ExecutionTargetSetupState
	ActiveClaim                 *ExecutionTargetClaim
	RecoveryDisposition         ExecutionTargetRecoveryDisposition
	RecoveryCause               *ExecutionTargetRecoveryCause
	ExactBranchObservation      *string
	LinkedWorktreeOwnership     *ExecutionTargetLinkedWorktreeOwnership
	ExpectedDetachmentCommit    *string
}

func (target ExecutionTarget) Validate() error {
	if strings.TrimSpace(string(target.TaskID)) == "" {
		return errors.New("execution target task id is required")
	}
	if err := validateExecutionTargetRecovery(target); err != nil {
		return err
	}
	if err := validateExecutionTargetClaim(target.ActiveClaim); err != nil {
		return err
	}
	if err := validateExecutionTargetOwnership(target.LinkedWorktreeOwnership); err != nil {
		return err
	}
	if err := validateOptionalExecutionTargetString("exact branch observation", target.ExactBranchObservation); err != nil {
		return err
	}
	if err := validateOptionalExecutionTargetString("expected detachment commit", target.ExpectedDetachmentCommit); err != nil {
		return err
	}
	if target.ExpectedDetachmentCommit != nil {
		if target.ExactBranchObservation == nil || *target.ExpectedDetachmentCommit != *target.ExactBranchObservation {
			return errors.New("expected detachment commit must equal the exact branch observation")
		}
	}

	switch target.Policy {
	case ExecutionPolicyNone:
		return validateNoneExecutionTarget(target)
	case ExecutionPolicyHead, ExecutionPolicyDefaultBranch, ExecutionPolicyCustomRef:
		return validateManagedExecutionTarget(target)
	default:
		return fmt.Errorf("invalid materialized execution target policy %q", target.Policy)
	}
}

func validateNoneExecutionTarget(target ExecutionTarget) error {
	if target.RequestedCustomRef != nil ||
		target.ResolvedSource != nil ||
		target.State != ExecutionTargetStateLocked ||
		target.ProvisioningGeneration != nil ||
		target.SetupProvisioningGeneration != nil ||
		target.SetupState != ExecutionTargetSetupNotApplicable ||
		target.ActiveClaim != nil ||
		target.RecoveryDisposition != ExecutionTargetRecoveryAvailable ||
		target.RecoveryCause != nil ||
		target.ExactBranchObservation != nil ||
		target.LinkedWorktreeOwnership != nil ||
		target.ExpectedDetachmentCommit != nil {
		return errors.New("none execution target cannot carry Git, provisioning, claim, or recovery facts")
	}
	return nil
}

func validateManagedExecutionTarget(target ExecutionTarget) error {
	if target.Policy == ExecutionPolicyCustomRef {
		if err := validateRequiredExecutionTargetString("custom execution target ref", target.RequestedCustomRef); err != nil {
			return err
		}
	} else if target.RequestedCustomRef != nil {
		return fmt.Errorf("execution target policy %q cannot have a custom ref", target.Policy)
	}
	if target.ResolvedSource == nil {
		return errors.New("managed execution target requires a resolved source")
	}
	if err := target.ResolvedSource.Validate(); err != nil {
		return err
	}
	switch target.State {
	case ExecutionTargetStateInitialProvisioning, ExecutionTargetStateLockedReprovisioning:
		if err := validateRequiredExecutionTargetString("intended worktree root", target.IntendedWorktreeRoot); err != nil {
			return err
		}
	case ExecutionTargetStateLocked:
		if target.IntendedWorktreeRoot != nil {
			return errors.New("locked execution target cannot retain an intended worktree root")
		}
	default:
		return fmt.Errorf("invalid execution target state %q", target.State)
	}
	if err := validateRequiredExecutionTargetString("provisioning generation", target.ProvisioningGeneration); err != nil {
		return err
	}
	if err := validateRequiredExecutionTargetString("setup provisioning generation", target.SetupProvisioningGeneration); err != nil {
		return err
	}
	if *target.ProvisioningGeneration != *target.SetupProvisioningGeneration {
		return errors.New("setup provisioning generation must equal provisioning generation")
	}
	switch target.SetupState {
	case ExecutionTargetSetupPending, ExecutionTargetSetupRunning, ExecutionTargetSetupSucceeded, ExecutionTargetSetupFailed:
	default:
		return fmt.Errorf("managed execution target has invalid setup state %q", target.SetupState)
	}
	return nil
}

func (source ExecutionTargetResolvedSource) Validate() error {
	if strings.TrimSpace(source.Commit) == "" {
		return errors.New("resolved execution target commit is required")
	}
	switch source.Kind {
	case ExecutionTargetSourceNamedRef:
		return validateRequiredExecutionTargetString("resolved named ref", source.NamedRef)
	case ExecutionTargetSourceDetachedCommit:
		if source.NamedRef != nil {
			return errors.New("detached execution target source cannot have a named ref")
		}
		return nil
	default:
		return fmt.Errorf("invalid execution target source kind %q", source.Kind)
	}
}

func validateExecutionTargetClaim(claim *ExecutionTargetClaim) error {
	if claim == nil {
		return nil
	}
	if strings.TrimSpace(claim.Generation) == "" {
		return errors.New("execution target claim generation is required")
	}
	switch claim.Phase {
	case ExecutionTargetClaimMaterializing, ExecutionTargetClaimRecoveryQueued, ExecutionTargetClaimRecovering:
		return nil
	default:
		return fmt.Errorf("invalid execution target claim phase %q", claim.Phase)
	}
}

func validateExecutionTargetRecovery(target ExecutionTarget) error {
	switch target.RecoveryDisposition {
	case ExecutionTargetRecoveryAvailable:
		if target.RecoveryCause != nil {
			return errors.New("available execution target cannot have a recovery cause")
		}
	case ExecutionTargetRecoveryManualRecovery:
		if target.RecoveryCause == nil || strings.TrimSpace(string(*target.RecoveryCause)) == "" {
			return errors.New("manual-recovery execution target requires a recovery cause")
		}
	default:
		return fmt.Errorf("invalid execution target recovery disposition %q", target.RecoveryDisposition)
	}
	return nil
}

func validateExecutionTargetOwnership(ownership *ExecutionTargetLinkedWorktreeOwnership) error {
	if ownership == nil {
		return nil
	}
	for name, value := range map[string]string{
		"linked worktree common directory":     ownership.CommonDir,
		"linked worktree administrative entry": ownership.AdminEntry,
		"linked worktree gitdir":               ownership.GitDir,
		"linked worktree head ref":             ownership.HeadRef,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required when linked worktree ownership is present", name)
		}
	}
	return nil
}

func validateRequiredExecutionTargetString(name string, value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func validateOptionalExecutionTargetString(name string, value *string) error {
	if value != nil && strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s cannot be empty when present", name)
	}
	return nil
}

type ExecutionWorkspace struct {
	ID   string
	Root string
}

type ExecutionWorktree struct {
	ID   string
	Root string
}

type ExecutionRoot struct {
	SourceWorkspace ExecutionWorkspace
	ManagedWorktree *ExecutionWorktree
	EffectiveRoot   string
}

func (root ExecutionRoot) Validate() error {
	if strings.TrimSpace(root.SourceWorkspace.ID) == "" || strings.TrimSpace(root.SourceWorkspace.Root) == "" {
		return errors.New("execution root source workspace is required")
	}
	if strings.TrimSpace(root.EffectiveRoot) == "" {
		return errors.New("execution root effective root is required")
	}
	if root.ManagedWorktree == nil {
		if root.EffectiveRoot != root.SourceWorkspace.Root {
			return errors.New("source execution root must use the source workspace root")
		}
		return nil
	}
	if strings.TrimSpace(root.ManagedWorktree.ID) == "" || strings.TrimSpace(root.ManagedWorktree.Root) == "" {
		return errors.New("execution root managed worktree is incomplete")
	}
	if root.EffectiveRoot != root.ManagedWorktree.Root {
		return errors.New("managed execution root must use the managed worktree root")
	}
	return nil
}

type ExecutionTargetNegotiationSourceKind string

const (
	ExecutionTargetNegotiationSourceNonGit         ExecutionTargetNegotiationSourceKind = "non_git"
	ExecutionTargetNegotiationSourceNamedRef       ExecutionTargetNegotiationSourceKind = "named_ref"
	ExecutionTargetNegotiationSourceDetachedCommit ExecutionTargetNegotiationSourceKind = "detached_commit"
	ExecutionTargetNegotiationSourceUnavailable    ExecutionTargetNegotiationSourceKind = "unavailable"
)

type ExecutionTargetNegotiationSource struct {
	Kind     ExecutionTargetNegotiationSourceKind
	NamedRef *string
	Commit   *string
}

type ExecutionTargetNegotiationActionKind string

const (
	ExecutionTargetNegotiationActionStart      ExecutionTargetNegotiationActionKind = "start"
	ExecutionTargetNegotiationActionManualMove ExecutionTargetNegotiationActionKind = "manual_move"
	ExecutionTargetNegotiationActionApproval   ExecutionTargetNegotiationActionKind = "approval"
)

type ExecutionTargetNegotiationAction struct {
	Kind                  ExecutionTargetNegotiationActionKind
	StartPlacementID      *PlacementID
	MoveSourcePlacementID *PlacementID
	MoveTargetNodeID      *NodeID
	ApprovalTransitionID  *TransitionID
}

type ExecutionTargetNegotiation struct {
	TaskID            TaskID
	Generation        string
	WorkflowID        WorkflowID
	SourceWorkspaceID string
	Source            ExecutionTargetNegotiationSource
	RecoveryCause     *ExecutionTargetRecoveryCause
	Action            ExecutionTargetNegotiationAction
}

func (negotiation ExecutionTargetNegotiation) Validate() error {
	if strings.TrimSpace(string(negotiation.TaskID)) == "" ||
		strings.TrimSpace(negotiation.Generation) == "" ||
		strings.TrimSpace(string(negotiation.WorkflowID)) == "" ||
		strings.TrimSpace(negotiation.SourceWorkspaceID) == "" {
		return errors.New("execution target negotiation identity is required")
	}
	if negotiation.RecoveryCause != nil && strings.TrimSpace(string(*negotiation.RecoveryCause)) == "" {
		return errors.New("execution target negotiation recovery cause cannot be empty")
	}
	if err := negotiation.Source.Validate(); err != nil {
		return err
	}
	return negotiation.Action.Validate()
}

func (source ExecutionTargetNegotiationSource) Validate() error {
	switch source.Kind {
	case ExecutionTargetNegotiationSourceNonGit, ExecutionTargetNegotiationSourceUnavailable:
		if source.NamedRef != nil || source.Commit != nil {
			return fmt.Errorf("execution target negotiation source %q cannot have Git facts", source.Kind)
		}
	case ExecutionTargetNegotiationSourceNamedRef:
		if err := validateRequiredExecutionTargetString("execution target negotiation named ref", source.NamedRef); err != nil {
			return err
		}
		if err := validateRequiredExecutionTargetString("execution target negotiation commit", source.Commit); err != nil {
			return err
		}
	case ExecutionTargetNegotiationSourceDetachedCommit:
		if source.NamedRef != nil {
			return errors.New("detached execution target negotiation source cannot have a named ref")
		}
		if err := validateRequiredExecutionTargetString("execution target negotiation commit", source.Commit); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid execution target negotiation source kind %q", source.Kind)
	}
	return nil
}

func (action ExecutionTargetNegotiationAction) Validate() error {
	switch action.Kind {
	case ExecutionTargetNegotiationActionStart:
		if action.StartPlacementID == nil || strings.TrimSpace(string(*action.StartPlacementID)) == "" ||
			action.MoveSourcePlacementID != nil || action.MoveTargetNodeID != nil || action.ApprovalTransitionID != nil {
			return errors.New("start execution target negotiation requires only a start placement")
		}
	case ExecutionTargetNegotiationActionManualMove:
		if action.StartPlacementID != nil ||
			action.MoveSourcePlacementID == nil || strings.TrimSpace(string(*action.MoveSourcePlacementID)) == "" ||
			action.MoveTargetNodeID == nil || strings.TrimSpace(string(*action.MoveTargetNodeID)) == "" ||
			action.ApprovalTransitionID != nil {
			return errors.New("manual-move execution target negotiation requires only source placement and target node")
		}
	case ExecutionTargetNegotiationActionApproval:
		if action.StartPlacementID != nil || action.MoveSourcePlacementID != nil || action.MoveTargetNodeID != nil ||
			action.ApprovalTransitionID == nil || strings.TrimSpace(string(*action.ApprovalTransitionID)) == "" {
			return errors.New("approval execution target negotiation requires only an approval transition")
		}
	default:
		return fmt.Errorf("invalid execution target negotiation action kind %q", action.Kind)
	}
	return nil
}
