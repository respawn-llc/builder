package tools

import (
	"context"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"fmt"
	"sync"
)

type Call struct {
	ID     string
	Name   toolspec.ID
	Input  json.RawMessage
	RunID  string
	StepID string
}

type Result struct {
	CallID        string                   `json:"call_id"`
	Name          toolspec.ID              `json:"name"`
	Output        json.RawMessage          `json:"output"`
	IsError       bool                     `json:"is_error"`
	Terminal      bool                     `json:"terminal,omitempty"`
	Summary       string                   `json:"summary,omitempty"`
	CondensedText string                   `json:"condensed_text,omitempty"`
	Presentation  *transcript.ToolCallMeta `json:"presentation,omitempty"`
}

type Definition struct {
	ID          toolspec.ID
	Description string
	Schema      json.RawMessage
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
	mu     sync.RWMutex
	byName map[toolspec.ID]Handler
	order  []toolspec.ID
}

func NewRegistry(handlers ...HandlerRegistration) *Registry {
	r := &Registry{}
	r.mustReplaceLocked(handlers)
	return r
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
		def := definitions[id]
		out = append(out, def)
	}
	return out
}

func (r *Registry) ReplaceHandlers(handlers ...HandlerRegistration) {
	if r == nil {
		panic("tool registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mustReplaceLocked(handlers)
}

func (r *Registry) mustReplaceLocked(handlers []HandlerRegistration) {
	m := make(map[toolspec.ID]Handler, len(handlers))
	order := make([]toolspec.ID, 0, len(handlers))
	for _, h := range handlers {
		id := h.ID
		if _, ok := definitions[id]; !ok {
			panic(fmt.Sprintf("tool %q is missing centralized definition", id))
		}
		if h.Handler == nil {
			panic(fmt.Sprintf("tool %q handler is required", id))
		}
		if _, exists := m[id]; exists {
			panic(fmt.Sprintf("duplicate tool handler registration for %q", id))
		}
		m[id] = h.Handler
		order = append(order, id)
	}
	r.byName = m
	r.order = order
}
