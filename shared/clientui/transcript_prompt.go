package clientui

import (
	"fmt"
	"strings"
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type TranscriptPromptKind string

const (
	TranscriptPromptKindQuestion TranscriptPromptKind = "question"
	TranscriptPromptKindApproval TranscriptPromptKind = "approval"
)

type TranscriptPromptStatus string

const (
	TranscriptPromptStatusPending  TranscriptPromptStatus = "pending"
	TranscriptPromptStatusResolved TranscriptPromptStatus = "resolved"
)

type TranscriptPrompt struct {
	Kind                   TranscriptPromptKind
	Status                 TranscriptPromptStatus `json:"State"`
	ToolCallID             ToolCallID
	SessionID              runtimeids.SessionID
	StepID                 runtimeids.StepID
	Question               string
	CreatedAt              time.Time
	Suggestions            []string
	RecommendedOptionIndex *int
	ApprovalOptions        []ApprovalDecision
	AccessTargets          []FileAccessTarget
}

func (p TranscriptPrompt) Validate() error {
	if err := p.Kind.Validate(); err != nil {
		return err
	}
	if err := p.Status.Validate(); err != nil {
		return err
	}
	if err := p.ToolCallID.Validate(); err != nil {
		return err
	}
	if p.SessionID.IsZero() {
		return fmt.Errorf("pending prompt session id is required")
	}
	if p.StepID.IsZero() {
		return fmt.Errorf("pending prompt step id is required")
	}
	if strings.TrimSpace(p.Question) == "" && len(p.AccessTargets) == 0 {
		return fmt.Errorf("pending prompt question is required")
	}
	if p.CreatedAt.IsZero() {
		return fmt.Errorf("pending prompt creation time is required")
	}
	switch p.Kind {
	case TranscriptPromptKindQuestion:
		return p.validateQuestion()
	case TranscriptPromptKindApproval:
		return p.validateApproval()
	default:
		panic(fmt.Sprintf("validated pending prompt kind %q is not handled", p.Kind))
	}
}

func (k TranscriptPromptKind) Validate() error {
	switch k {
	case TranscriptPromptKindQuestion, TranscriptPromptKindApproval:
		return nil
	default:
		return fmt.Errorf("unknown pending prompt kind %q", k)
	}
}

func (s TranscriptPromptStatus) Validate() error {
	switch s {
	case TranscriptPromptStatusPending, TranscriptPromptStatusResolved:
		return nil
	default:
		return fmt.Errorf("unknown pending prompt state %q", s)
	}
}

func (p TranscriptPrompt) validateQuestion() error {
	for index, suggestion := range p.Suggestions {
		if strings.TrimSpace(suggestion) == "" {
			return fmt.Errorf("pending prompt suggestion %d is empty", index)
		}
	}
	if p.RecommendedOptionIndex != nil {
		recommended := *p.RecommendedOptionIndex
		if recommended < 1 || recommended > len(p.Suggestions) {
			return fmt.Errorf("pending prompt recommended option index %d is outside 1..%d", recommended, len(p.Suggestions))
		}
	}
	if len(p.ApprovalOptions) > 0 {
		return fmt.Errorf("question prompt cannot carry approval options")
	}
	if len(p.AccessTargets) > 0 {
		return fmt.Errorf("question prompt cannot carry access targets")
	}
	return nil
}

func (p TranscriptPrompt) validateApproval() error {
	if len(p.AccessTargets) > 0 && strings.TrimSpace(p.Question) != "" {
		return fmt.Errorf("access approval prompt cannot carry question copy")
	}
	if len(p.Suggestions) > 0 {
		return fmt.Errorf("approval prompt cannot carry suggestions")
	}
	if p.RecommendedOptionIndex != nil {
		return fmt.Errorf("approval prompt cannot carry a recommended option")
	}
	if len(p.ApprovalOptions) == 0 {
		return fmt.Errorf("approval prompt requires approval options")
	}
	seen := make(map[ApprovalDecision]struct{}, len(p.ApprovalOptions))
	for index, decision := range p.ApprovalOptions {
		if err := sessioncontract.ValidatePromptApprovalDecision(decision); err != nil {
			return fmt.Errorf("pending prompt approval option %d: %w", index, err)
		}
		if _, exists := seen[decision]; exists {
			return fmt.Errorf("pending prompt approval decision %q is duplicated", decision)
		}
		seen[decision] = struct{}{}
	}
	for index, target := range p.AccessTargets {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("pending prompt access target %d: %w", index, err)
		}
	}
	return nil
}
