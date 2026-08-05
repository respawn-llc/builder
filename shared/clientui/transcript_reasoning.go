package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type TranscriptThinkingStatusUpdate struct {
	StepID runtimeids.StepID
	Text   string
}

type TranscriptProviderReasoningTraceIdentity struct {
	ItemID       string
	SummaryIndex *int64
}

type TranscriptReasoningTraceIdentity struct {
	Provider *TranscriptProviderReasoningTraceIdentity
	Kent     *runtimeids.ReasoningTraceID
}

type TranscriptReasoningTraceUpdate struct {
	StepID      runtimeids.StepID
	Identity    TranscriptReasoningTraceIdentity
	CompactText string
	Text        string
}

type TranscriptReasoningTraceReset struct {
	StepID runtimeids.StepID
}

func (i TranscriptReasoningTraceIdentity) String() string {
	if i.Provider != nil {
		index := "<nil>"
		if i.Provider.SummaryIndex != nil {
			index = fmt.Sprintf("%d", *i.Provider.SummaryIndex)
		}
		return "provider:" + strings.TrimSpace(i.Provider.ItemID) + ":" + index
	}
	if i.Kent != nil {
		return "kent:" + i.Kent.String()
	}
	return "<invalid>"
}

func (s TranscriptThinkingStatusUpdate) Validate() error {
	if s.StepID.IsZero() {
		return fmt.Errorf("thinking status update step id is required")
	}
	if strings.TrimSpace(s.Text) == "" {
		return fmt.Errorf("thinking status update text is required")
	}
	return nil
}

func (i TranscriptProviderReasoningTraceIdentity) Validate() error {
	if strings.TrimSpace(i.ItemID) == "" {
		return fmt.Errorf("reasoning trace provider item id is required")
	}
	if i.SummaryIndex == nil {
		return fmt.Errorf("reasoning trace provider summary index is required")
	}
	if *i.SummaryIndex < 0 {
		return fmt.Errorf("reasoning trace provider summary index must be nonnegative")
	}
	return nil
}

func (i TranscriptReasoningTraceIdentity) Validate() error {
	if (i.Provider == nil) == (i.Kent == nil) {
		return fmt.Errorf("reasoning trace identity requires exactly one provider or Kent branch")
	}
	if i.Provider != nil {
		return i.Provider.Validate()
	}
	if i.Kent.IsZero() {
		return fmt.Errorf("reasoning trace Kent identity is required")
	}
	return nil
}

func (u TranscriptReasoningTraceUpdate) Validate() error {
	if u.StepID.IsZero() {
		return fmt.Errorf("reasoning trace update step id is required")
	}
	if err := u.Identity.Validate(); err != nil {
		return fmt.Errorf("validate reasoning trace update identity: %w", err)
	}
	if strings.TrimSpace(u.CompactText) == "" {
		return fmt.Errorf("reasoning trace update compact text is required")
	}
	if strings.TrimSpace(u.Text) == "" {
		return fmt.Errorf("reasoning trace update text is required")
	}
	return nil
}

func (r TranscriptReasoningTraceReset) Validate() error {
	if r.StepID.IsZero() {
		return fmt.Errorf("reasoning trace reset step id is required")
	}
	return nil
}
