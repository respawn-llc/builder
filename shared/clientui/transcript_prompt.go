package clientui

import (
	"fmt"
	"strings"
	"time"

	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type PromptID string

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

type ToolProvenance struct {
	ToolCallID ToolCallID
	ToolName   string
}

type TranscriptPrompt struct {
	Kind                   TranscriptPromptKind
	Status                 TranscriptPromptStatus `json:"State"`
	PromptID               PromptID
	SessionID              runtimeids.SessionID
	StepID                 runtimeids.StepID
	Question               string
	CreatedAt              time.Time
	Suggestions            []string
	RecommendedOptionIndex *int
	ApprovalOptions        []ApprovalDecision
	Tool                   *ToolProvenance
}

func (p TranscriptPrompt) Validate() error {
	if err := p.Kind.Validate(); err != nil {
		return err
	}
	if err := p.Status.Validate(); err != nil {
		return err
	}
	if err := p.PromptID.Validate(); err != nil {
		return err
	}
	if p.SessionID.IsZero() {
		return fmt.Errorf("pending prompt session id is required")
	}
	if p.StepID.IsZero() {
		return fmt.Errorf("pending prompt step id is required")
	}
	if strings.TrimSpace(p.Question) == "" {
		return fmt.Errorf("pending prompt question is required")
	}
	if p.CreatedAt.IsZero() {
		return fmt.Errorf("pending prompt creation time is required")
	}
	if err := p.Tool.Validate(); err != nil {
		return err
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
	return nil
}

func (p TranscriptPrompt) validateApproval() error {
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
	return nil
}

func (p *ToolProvenance) Validate() error {
	if p == nil {
		return nil
	}
	if err := p.ToolCallID.Validate(); err != nil {
		return fmt.Errorf("validate pending prompt tool provenance: %w", err)
	}
	if strings.TrimSpace(p.ToolName) == "" {
		return fmt.Errorf("pending prompt tool name is required when tool provenance is present")
	}
	return nil
}

func (id PromptID) Validate() error {
	raw := string(id)
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("pending prompt id is required")
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("pending prompt id must not have leading or trailing whitespace")
	}
	return nil
}
