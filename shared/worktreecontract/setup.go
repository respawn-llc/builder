package worktreecontract

type SetupFailureKind string

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
