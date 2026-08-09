package worktreecontract

type SetupFailureKind string

type SetupRequirement string

const (
	SetupRequirementRequired         SetupRequirement = "required"
	SetupRequirementAlreadyCompleted SetupRequirement = "already_completed"
)

const (
	SetupFailureProcessExit             SetupFailureKind = "process_exit"
	SetupFailureTimeout                 SetupFailureKind = "timeout"
	SetupFailureTargetPreparation       SetupFailureKind = "target_preparation"
	SetupFailureInterruptionPersistence SetupFailureKind = "interruption_persistence"
	SetupFailureCanceled                SetupFailureKind = "canceled"
	SetupFailureControllerShutdown      SetupFailureKind = "controller_shutdown"
	SetupFailureOperational             SetupFailureKind = "operational"
)

func IsRetryReadySetupFailure(kind SetupFailureKind) bool {
	switch kind {
	case SetupFailureProcessExit, SetupFailureTimeout, SetupFailureTargetPreparation, SetupFailureOperational:
		return true
	default:
		return false
	}
}

func IsNonRetryableSetupFailure(kind SetupFailureKind) bool {
	switch kind {
	case SetupFailureInterruptionPersistence, SetupFailureCanceled, SetupFailureControllerShutdown:
		return true
	default:
		return false
	}
}

func HasFixedRetryReadiness(kind SetupFailureKind) bool {
	return kind != SetupFailureOperational
}

func IsValidSetupRequirement(requirement SetupRequirement) bool {
	switch requirement {
	case SetupRequirementRequired, SetupRequirementAlreadyCompleted:
		return true
	default:
		return false
	}
}
