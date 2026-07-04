package registry

func init() {
	transcriptContractViolationsPanic = true
}

func withTranscriptContractViolationPanic(enabled bool) func() {
	previous := transcriptContractViolationsPanic
	transcriptContractViolationsPanic = enabled
	return func() {
		transcriptContractViolationsPanic = previous
	}
}
