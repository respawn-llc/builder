package app

import "core/shared/clientui"

func capturePanic(action func()) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	action()
	return nil
}

func runtimeTupleTestRunningActivity() clientui.RuntimeActivity {
	return clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRunning,
		QueueAccepting: true,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID:      ongoingTestRunID(),
			StepID:     ongoingTestStepID(),
			ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		},
	}
}
