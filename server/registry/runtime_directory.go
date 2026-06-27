package registry

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"core/server/runtime"
	askquestion "core/server/tools"
)

type runtimeDirectory struct {
	mu         sync.RWMutex
	entries    map[string]*runtimeEntry
	generation uint64
}

type runtimeEntry struct {
	mu              sync.Mutex
	cond            *sync.Cond
	generation      uint64
	built           bool
	buildErr        error
	ready           chan struct{}
	closing         bool
	closeDraining   bool
	inFlight        int
	engine          *runtime.Engine
	rebind          func(string) error
	sessionActivity *sessionActivityBroker
	promptActivity  *promptActivityBroker
	pendingPrompts  *pendingPromptStore
}

func newRuntimeDirectory() *runtimeDirectory {
	return &runtimeDirectory{entries: make(map[string]*runtimeEntry)}
}

func newRuntimeEntry(engine *runtime.Engine, generation uint64, rebind func(string) error) *runtimeEntry {
	entry := newBuildingRuntimeEntry(generation)
	entry.engine = engine
	entry.rebind = rebind
	entry.built = true
	close(entry.ready)
	return entry
}

func newBuildingRuntimeEntry(generation uint64) *runtimeEntry {
	entry := &runtimeEntry{
		generation:      generation,
		ready:           make(chan struct{}),
		sessionActivity: newSessionActivityBroker(),
		promptActivity:  newPromptActivityBroker(),
		pendingPrompts:  newPendingPromptStore(),
	}
	entry.cond = sync.NewCond(&entry.mu)
	return entry
}

func (e *runtimeEntry) resolveBuild(engine *runtime.Engine, rebind func(string) error, buildErr error) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.built || e.buildErr != nil {
		return
	}
	if buildErr == nil && engine == nil {
		buildErr = fmt.Errorf("runtime build produced no engine")
	}
	e.engine = engine
	e.rebind = rebind
	e.buildErr = buildErr
	e.built = buildErr == nil
	close(e.ready)
	e.cond.Broadcast()
}

func (e *runtimeEntry) awaitReady(ctx context.Context) (*runtime.Engine, error) {
	if e == nil {
		return nil, fmt.Errorf("runtime entry is unavailable")
	}
	select {
	case <-e.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.buildErr != nil {
		return nil, e.buildErr
	}
	return e.engine, nil
}

func (d *runtimeDirectory) Register(sessionID string, engine *runtime.Engine, rebind func(string) error, beforeReplace func(*runtimeEntry)) *runtimeEntry {
	if d == nil || engine == nil {
		return nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil
	}
	_, previous := d.installEntry(id, func(generation uint64) *runtimeEntry {
		return newRuntimeEntry(engine, generation, rebind)
	}, beforeReplace)
	return previous
}

func (d *runtimeDirectory) Claim(sessionID string) (*runtimeEntry, *runtimeEntry) {
	if d == nil {
		return nil, nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, nil
	}
	return d.installEntry(id, func(generation uint64) *runtimeEntry {
		return newBuildingRuntimeEntry(generation)
	}, nil)
}

func (d *runtimeDirectory) installEntry(id string, makeEntry func(generation uint64) *runtimeEntry, beforeReplace func(*runtimeEntry)) (*runtimeEntry, *runtimeEntry) {
	for {
		d.mu.Lock()
		previous := d.entries[id]
		if previous == nil {
			d.generation++
			entry := makeEntry(d.generation)
			d.entries[id] = entry
			d.mu.Unlock()
			return entry, nil
		}
		previous.markClosing()
		d.mu.Unlock()

		previous.waitForGuards()

		d.mu.Lock()
		if d.entries[id] != previous {
			d.mu.Unlock()
			continue
		}
		ref, ok := previous.beginReplacement()
		if !ok {
			d.mu.Unlock()
			continue
		}
		d.mu.Unlock()
		if beforeReplace != nil {
			beforeReplace(previous)
		}
		d.mu.Lock()
		if d.entries[id] != previous {
			d.mu.Unlock()
			ref.Release()
			continue
		}
		d.generation++
		entry := makeEntry(d.generation)
		d.entries[id] = entry
		d.mu.Unlock()
		ref.Release()
		return entry, previous
	}
}

func (d *runtimeDirectory) Unregister(sessionID string, engine *runtime.Engine) (string, *runtimeEntry) {
	if d == nil {
		return "", nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return "", nil
	}
	for {
		d.mu.Lock()
		entry := d.entries[id]
		if entry == nil || (engine != nil && entry.engine != engine) {
			d.mu.Unlock()
			return "", nil
		}
		entry.markClosing()
		d.mu.Unlock()

		entry.waitForGuards()

		d.mu.Lock()
		if d.entries[id] != entry {
			d.mu.Unlock()
			return "", nil
		}
		if entry.hasInFlight() {
			d.mu.Unlock()
			continue
		}
		delete(d.entries, id)
		d.mu.Unlock()
		return id, entry
	}
}

func (d *runtimeDirectory) BeginClose(sessionID string, engine *runtime.Engine) (string, *runtimeEntry, *runtimeCloseDrainRef) {
	if d == nil {
		return "", nil, nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return "", nil, nil
	}
	d.mu.RLock()
	entry := d.entries[id]
	if entry == nil || (engine != nil && entry.engine != engine) {
		d.mu.RUnlock()
		return "", nil, nil
	}
	ref, ok := entry.beginCloseDrain()
	if !ok {
		d.mu.RUnlock()
		return "", nil, nil
	}
	d.mu.RUnlock()
	return id, entry, ref
}

func (d *runtimeDirectory) RemoveClosing(sessionID string, engine *runtime.Engine, closingEntry *runtimeEntry) (string, *runtimeEntry) {
	if d == nil {
		return "", nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" || closingEntry == nil {
		return "", nil
	}
	d.mu.Lock()
	entry := d.entries[id]
	if entry == nil || entry != closingEntry || (engine != nil && entry.engine != engine) {
		d.mu.Unlock()
		return "", nil
	}
	delete(d.entries, id)
	d.mu.Unlock()
	return id, entry
}

func (d *runtimeDirectory) Resolve(sessionID string) *runtime.Engine {
	entry := d.Entry(sessionID)
	if entry == nil {
		return nil
	}
	return entry.engine
}

func (d *runtimeDirectory) Active(sessionID string) bool {
	return d.Entry(sessionID) != nil
}

func (d *runtimeDirectory) IDs() []string {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	ids := make([]string, 0, len(d.entries))
	for id := range d.entries {
		ids = append(ids, id)
	}
	d.mu.RUnlock()
	return ids
}

func (d *runtimeDirectory) Entry(sessionID string) *runtimeEntry {
	if d == nil {
		return nil
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil
	}
	d.mu.RLock()
	entry := d.entries[id]
	d.mu.RUnlock()
	return entry
}

func (d *runtimeDirectory) BeginGuard(ctx context.Context, sessionID string) (*runtimeGuard, error) {
	if d == nil {
		return nil, fmt.Errorf("runtime directory is required")
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, fmt.Errorf("runtime session id is required")
	}
	d.mu.RLock()
	entry := d.entries[id]
	if entry == nil {
		d.mu.RUnlock()
		return nil, fmt.Errorf("runtime %q is unavailable", id)
	}
	guard, err := entry.beginGuard(ctx, id)
	d.mu.RUnlock()
	return guard, err
}

func (e *runtimeEntry) beginGuard(ctx context.Context, sessionID string) (*runtimeGuard, error) {
	if e == nil {
		return nil, fmt.Errorf("runtime entry is unavailable")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return nil, fmt.Errorf("runtime entry is closing")
	}
	e.inFlight++
	return &runtimeGuard{entry: e, engine: e.engine, sessionID: strings.TrimSpace(sessionID), generation: e.generation}, nil
}

func (e *runtimeEntry) beginCloseDrain() (*runtimeCloseDrainRef, bool) {
	if e == nil {
		return nil, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing || e.closeDraining {
		return nil, false
	}
	e.closing = true
	e.closeDraining = true
	e.inFlight++
	e.cond.Broadcast()
	return &runtimeCloseDrainRef{entry: e}, true
}

func (e *runtimeEntry) beginReplacement() (*runtimeReplacementRef, bool) {
	if e == nil {
		return nil, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inFlight > 0 {
		return nil, false
	}
	e.inFlight++
	e.cond.Broadcast()
	return &runtimeReplacementRef{entry: e}, true
}

func (e *runtimeEntry) closeState() (bool, bool) {
	if e == nil {
		return false, false
	}
	e.mu.Lock()
	closing := e.closing
	draining := e.closeDraining
	e.mu.Unlock()
	return closing, draining
}

func (e *runtimeEntry) markClosing() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closing = true
	e.cond.Broadcast()
	e.mu.Unlock()
}

func (e *runtimeEntry) waitForGuards() {
	if e == nil {
		return
	}
	e.mu.Lock()
	for e.inFlight > 0 {
		e.cond.Wait()
	}
	e.mu.Unlock()
}

func (e *runtimeEntry) hasInFlight() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	hasInFlight := e.inFlight > 0
	e.mu.Unlock()
	return hasInFlight
}

func (e *runtimeEntry) waitForGuardsExceptCloseDrain() {
	if e == nil {
		return
	}
	e.mu.Lock()
	for e.inFlight > 1 {
		e.cond.Wait()
	}
	e.mu.Unlock()
}

type runtimeCloseDrainRef struct {
	entry     *runtimeEntry
	releaseMu sync.Mutex
	released  bool
}

type runtimeReplacementRef struct {
	entry     *runtimeEntry
	releaseMu sync.Mutex
	released  bool
}

func (r *runtimeReplacementRef) Release() {
	if r == nil || r.entry == nil {
		return
	}
	r.releaseMu.Lock()
	if r.released {
		r.releaseMu.Unlock()
		return
	}
	r.released = true
	r.releaseMu.Unlock()
	r.entry.mu.Lock()
	if r.entry.inFlight > 0 {
		r.entry.inFlight--
	}
	r.entry.cond.Broadcast()
	r.entry.mu.Unlock()
}

func (r *runtimeCloseDrainRef) WaitForGuards() {
	if r == nil || r.entry == nil {
		return
	}
	r.entry.waitForGuardsExceptCloseDrain()
}

func (r *runtimeCloseDrainRef) Release() {
	if r == nil || r.entry == nil {
		return
	}
	r.releaseMu.Lock()
	if r.released {
		r.releaseMu.Unlock()
		return
	}
	r.released = true
	r.releaseMu.Unlock()
	r.entry.mu.Lock()
	if r.entry.inFlight > 0 {
		r.entry.inFlight--
	}
	r.entry.closeDraining = false
	r.entry.cond.Broadcast()
	r.entry.mu.Unlock()
}

type runtimeGuard struct {
	entry      *runtimeEntry
	engine     *runtime.Engine
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
	if g == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	trimmedWorkdir := strings.TrimSpace(workdir)
	if trimmedWorkdir == "" {
		return fmt.Errorf("runtime workdir is required")
	}
	if g.entry != nil && g.entry.rebind != nil {
		return g.entry.rebind(trimmedWorkdir)
	}
	if g.engine != nil {
		g.engine.SetTranscriptWorkingDir(trimmedWorkdir)
	}
	return nil
}

func (g *runtimeGuard) SubmitPromptResponse(resp askquestion.AskQuestionResponse, err error) error {
	if g == nil || g.entry == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	return g.entry.pendingPrompts.Submit(resp, err, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
		g.entry.PublishPendingPrompt(g.sessionID, snapshot, eventType)
	})
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

func closeRuntimeEntry(entry *runtimeEntry, err error) {
	if entry == nil {
		return
	}
	entry.markClosing()
	entry.waitForGuards()
	entry.pendingPrompts.Close(err)
	entry.promptActivity.Close(err)
	entry.sessionActivity.Close(err)
}
