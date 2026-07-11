package registry

import (
	"fmt"
	"sync"

	"core/server/runtime"
	askquestion "core/server/tools"
	"core/shared/clientui"
)

type runtimeGuard struct {
	entry      *runtimeEntry
	engine     *runtime.Engine
	registry   *RuntimeRegistry
	sessionID  string
	generation uint64
	releaseMu  sync.Mutex
	released   bool
}

func (g *runtimeGuard) Engine() *runtime.Engine {
	if g == nil {
		return nil
	}
	return g.engine
}

func (g *runtimeGuard) Generation() uint64 {
	if g == nil {
		return 0
	}
	return g.generation
}

func (g *runtimeGuard) Rebind(workdir string) error {
	if g == nil || g.entry == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	return g.entry.rebindWorkdir(workdir)
}

func (g *runtimeGuard) Retire(reason runtime.QueuedUserMessageFailureReason) error {
	if g == nil || g.entry == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	if g.registry == nil {
		return fmt.Errorf("runtime registry is unavailable")
	}
	return g.registry.retireGuardedRuntime(g, reason)
}

func (g *runtimeGuard) SubmitPromptResponse(resp askquestion.AskQuestionResponse, err error) error {
	if g == nil || g.entry == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	return g.entry.pendingPrompts.Submit(resp, err, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
		g.entry.PublishPendingPrompt(g.sessionID, snapshot, eventType, g.entry.nextReadModelVersion(g.sessionID))
	})
}

func (e *runtimeEntry) nextReadModelVersion(sessionID string) clientui.ReadModelVersion {
	if e == nil || e.readModelVersion == nil {
		return clientui.ReadModelVersion{}
	}
	return e.readModelVersion(sessionID)
}

func (g *runtimeGuard) Release() {
	if g == nil || g.entry == nil {
		return
	}
	g.releaseMu.Lock()
	if g.released {
		g.releaseMu.Unlock()
		return
	}
	g.released = true
	g.releaseMu.Unlock()
	g.entry.mu.Lock()
	if g.entry.inFlight > 0 {
		g.entry.inFlight--
	}
	g.entry.cond.Broadcast()
	g.entry.mu.Unlock()
}
