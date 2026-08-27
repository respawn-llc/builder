package tools

import (
	"context"
	"core/shared/jsoncontract"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"fmt"
	"sync"
)

type Call struct {
	ID                        string
	Name                      toolspec.ID
	Input                     json.RawMessage
	RunID                     string
	StepID                    string
	AskQuestionBatch          *AskQuestionBatchMetadata
	OnAskQuestionBatchSkipped func(AskQuestionBatchMetadata)
}

type AskQuestionOrigin string

const (
	AskQuestionOriginModelTool AskQuestionOrigin = "model_tool"
)

type AskQuestionBatchMetadata struct {
	Origin              AskQuestionOrigin
	RunID               string
	StepID              string
	PromptID            string
	BatchPromptIDs      []string
	CandidateOrdinal    int
	PreparedPromptCount int
}

type Result struct {
	CallID         string                   `json:"call_id"`
	Name           toolspec.ID              `json:"name"`
	Output         json.RawMessage          `json:"output"`
	IsError        bool                     `json:"is_error"`
	Terminal       bool                     `json:"terminal,omitempty"`
	Summary        *string                  `json:"summary,omitempty"`
	CondensedText  *string                  `json:"condensed_text,omitempty"`
	ModelWarnings  []ModelWarning           `json:"-"`
	Presentation   *transcript.ToolCallMeta `json:"presentation,omitempty"`
	QuestionAnswer *AskQuestionAnswer       `json:"question_answer,omitempty"`
	// PresentationDelta is transient handler output. Runtime consumes it before
	// persistence and materializes Presentation from authoritative call input.
	PresentationDelta *transcript.ToolResultPresentationDelta `json:"-"`

	BackgroundSessionID *string `json:"-"`
	OutputPath          *string `json:"-"`
}

type Definition struct {
	ID          toolspec.ID
	Description string
	Schema      jsoncontract.Function
	contract    Contract
}

type Handler interface {
	Call(ctx context.Context, c Call) (Result, error)
}

type HandlerRegistration struct {
	ID      toolspec.ID
	Handler Handler
}

type Registry struct {
	mu        sync.RWMutex
	contracts *StaticToolContracts
	byName    map[toolspec.ID]Handler
	order     []toolspec.ID
}

func NewRegistry() *Registry {
	return &Registry{byName: map[toolspec.ID]Handler{}}
}

func NewStaticToolRegistry(
	contracts StaticToolContracts,
	handlers ...HandlerRegistration,
) (*Registry, error) {
	r := &Registry{contracts: &contracts}
	if err := r.replaceLocked(handlers); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) Get(name toolspec.ID) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.byName[name]
	return h, ok
}

func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.byName))
	for _, id := range r.order {
		def, ok := r.contracts.definition(id)
		if !ok {
			continue
		}
		out = append(out, def)
	}
	return out
}

func (r *Registry) PrepareInput(id toolspec.ID, raw json.RawMessage) (json.RawMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.byName[id]; !ok {
		return nil, fmt.Errorf("tool %q is not registered", id)
	}
	if r.contracts == nil {
		return nil, fmt.Errorf("tool registry has no static contracts")
	}
	prepared, err := r.contracts.prepareInput(id, raw)
	if err != nil {
		return nil, fmt.Errorf("prepare %q input: %w", id, err)
	}
	return json.RawMessage(prepared), nil
}

func (r *Registry) ReplaceHandlers(handlers ...HandlerRegistration) error {
	if r == nil {
		return fmt.Errorf("tool registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.replaceLocked(handlers)
}

func (r *Registry) replaceLocked(handlers []HandlerRegistration) error {
	if len(handlers) > 0 && r.contracts == nil {
		return fmt.Errorf("empty tool registry cannot install ordinary handlers")
	}
	m := make(map[toolspec.ID]Handler, len(handlers))
	order := make([]toolspec.ID, 0, len(handlers))
	for _, h := range handlers {
		id := h.ID
		if _, ok := definitions[id]; !ok {
			return fmt.Errorf("tool %q is missing centralized definition", id)
		}
		if _, ok := r.contracts.contract(id); !ok {
			return fmt.Errorf("tool %q is missing prepared static contract", id)
		}
		if h.Handler == nil {
			return fmt.Errorf("tool %q handler is required", id)
		}
		if _, exists := m[id]; exists {
			return fmt.Errorf("duplicate tool handler registration for %q", id)
		}
		m[id] = h.Handler
		order = append(order, id)
	}
	r.byName = m
	r.order = order
	return nil
}
