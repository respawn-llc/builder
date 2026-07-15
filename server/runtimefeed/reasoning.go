package runtimefeed

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type ReasoningStatus struct {
	Text string
}

type TranscriptReasoningUpdate struct {
	StepID        runtimeids.StepID
	Key           string
	Text          string
	CurrentStatus *ReasoningStatus
}

type TranscriptReasoningReset struct {
	StepID runtimeids.StepID
}

func (u TranscriptReasoningUpdate) Validate() error {
	if u.StepID.IsZero() {
		return fmt.Errorf("reasoning update step id is required")
	}
	if strings.TrimSpace(u.Key) == "" {
		return fmt.Errorf("reasoning update key is required")
	}
	if u.CurrentStatus != nil && strings.TrimSpace(u.CurrentStatus.Text) == "" {
		return fmt.Errorf("reasoning status text is required when status is present")
	}
	return nil
}

func (r TranscriptReasoningReset) Validate() error {
	if r.StepID.IsZero() {
		return fmt.Errorf("reasoning reset step id is required")
	}
	return nil
}
