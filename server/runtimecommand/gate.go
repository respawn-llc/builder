package runtimecommand

import "core/server/runtimegate"

type StartGate = runtimegate.Gate

var (
	ErrStartGateAborted = runtimegate.ErrAborted
	ErrStartGateSettled = runtimegate.ErrSettled
)

func NewStartGate() *StartGate {
	return runtimegate.New()
}
