package runtimewire

import (
	"sync"

	"core/server/runtime"
)

type RuntimeEventPublisher interface {
	PublishRuntimeEventForEngine(sessionID string, engine *runtime.Engine, evt runtime.Event)
}

type OrderedRuntimeEventPublisher struct {
	sessionID string
	publisher RuntimeEventPublisher

	mu       sync.Mutex
	engine   *runtime.Engine
	resolved bool
	flushing bool
	pending  []runtime.Event
}

func NewOrderedRuntimeEventPublisher(sessionID string, publisher RuntimeEventPublisher) *OrderedRuntimeEventPublisher {
	return &OrderedRuntimeEventPublisher{
		sessionID: sessionID,
		publisher: publisher,
	}
}

func (p *OrderedRuntimeEventPublisher) Publish(evt runtime.Event) {
	if p == nil || p.publisher == nil {
		return
	}
	p.mu.Lock()
	engine := p.engine
	if engine == nil || !p.resolved || p.flushing {
		p.pending = append(p.pending, evt)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	p.publisher.PublishRuntimeEventForEngine(p.sessionID, engine, evt)
}

func (p *OrderedRuntimeEventPublisher) BindEngine(engine *runtime.Engine) {
	if p == nil || p.publisher == nil {
		return
	}
	p.mu.Lock()
	p.engine = engine
	p.mu.Unlock()
}

func (p *OrderedRuntimeEventPublisher) FlushAfterResolve() {
	if p == nil || p.publisher == nil {
		return
	}
	for {
		p.mu.Lock()
		if len(p.pending) == 0 {
			p.resolved = true
			p.flushing = false
			p.mu.Unlock()
			return
		}
		engine := p.engine
		p.flushing = true
		pending := append([]runtime.Event(nil), p.pending...)
		p.pending = nil
		p.mu.Unlock()

		for _, evt := range pending {
			p.publisher.PublishRuntimeEventForEngine(p.sessionID, engine, evt)
		}
	}
}
