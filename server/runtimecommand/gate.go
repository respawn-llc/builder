package runtimecommand

import "core/server/runtime"

type StartGate = runtime.StartGate

var (
	ErrStartGateAborted = runtime.ErrStartGateAborted
	ErrStartGateSettled = runtime.ErrStartGateSettled
)

func NewStartGate() *StartGate {
	return runtime.NewStartGate()
}
