package runtime

import (
	"fmt"
	"time"
)

type ResultGroupFlushReason uint8

const (
	ResultGroupFlushStepBoundary ResultGroupFlushReason = iota + 1
	ResultGroupFlushQuestion
	ResultGroupFlushApproval
	ResultGroupFlushCompleteNode
)

func (r ResultGroupFlushReason) String() string {
	switch r {
	case ResultGroupFlushStepBoundary:
		return "step_boundary"
	case ResultGroupFlushQuestion:
		return "question"
	case ResultGroupFlushApproval:
		return "approval"
	case ResultGroupFlushCompleteNode:
		return "complete_node"
	default:
		return fmt.Sprintf("unknown(%d)", r)
	}
}

type ResultGroupFlushObservation struct {
	Reason      ResultGroupFlushReason
	ResultCount int
	RecordCount int
	Latency     time.Duration
	Succeeded   bool
}

type ResultGroupDurabilityObserver interface {
	ObserveResultGroupFlush(ResultGroupFlushObservation)
}
