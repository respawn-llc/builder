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
	closed          chan struct{}
	closedOnce      sync.Once
	ownerRefs       int
	ownerIDs        map[string]struct{}
	closing         bool
	closeDraining   bool
	inFlight        int
	engine          *runtime.Engine
	rebind          func(string) error
	teardown        func()
	sessionActivity *sessionActivityBroker
	promptActivity  *promptActivityBroker
	pendingPrompts  *pendingPromptStore
}

func newRuntimeDirectory() *runtimeDirectory {
	return &runtimeDirectory{entries: make(map[string]*runtimeEntry)}
}

func newBuildingRuntimeEntry(generation uint64) *runtimeEntry {
	entry := &runtimeEntry{
		generation:      generation,
		ready:           make(chan struct{}),
		closed:          make(chan struct{}),
		ownerIDs:        make(map[string]struct{}),
		sessionActivity: newSessionActivityBroker(),
		promptActivity:  newPromptActivityBroker(),
		pendingPrompts:  newPendingPromptStore(),
	}
	entry.cond = sync.NewCond(&entry.mu)
	return entry
}

func (e *runtimeEntry) addOwner(ownerID string) int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ownerRefs++
	if trimmed := strings.TrimSpace(ownerID); trimmed != "" {
		if e.ownerIDs == nil {
			e.ownerIDs = make(map[string]struct{})
		}
		e.ownerIDs[trimmed] = struct{}{}
	}
	return e.ownerRefs
}

func (e *runtimeEntry) dropOwner(ownerID string) int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if trimmed := strings.TrimSpace(ownerID); trimmed != "" {
		if _, ok := e.ownerIDs[trimmed]; !ok {
			return e.ownerRefs
		}
		delete(e.ownerIDs, trimmed)
	}
	if e.ownerRefs > 0 {
		e.ownerRefs--
	}
	return e.ownerRefs
}

func (e *runtimeEntry) ownerCount() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ownerRefs
}

func (e *runtimeEntry) signalClosed() {
	if e == nil || e.closed == nil {
		return
	}
	e.closedOnce.Do(func() {
		close(e.closed)
	})
}

func (e *runtimeEntry) awaitClosed(ctx context.Context) error {
	if e == nil || e.closed == nil {
		return nil
	}
	select {
	case <-e.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *runtimeEntry) resolveBuild(engine *runtime.Engine, rebind func(string) error, teardown func(), buildErr error) {
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
	e.teardown = teardown
	e.buildErr = buildErr
	e.built = buildErr == nil
	if e.built && e.ownerRefs <= 0 {
		e.ownerRefs = 1
	}
	close(e.ready)
	e.cond.Broadcast()
}

func (e *runtimeEntry) engineRef() *runtime.Engine {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.engine
}

func (e *runtimeEntry) rebindWorkdir(workdir string) error {
	trimmedWorkdir := strings.TrimSpace(workdir)
	if trimmedWorkdir == "" {
		return fmt.Errorf("runtime workdir is required")
	}
	e.mu.Lock()
	rebind := e.rebind
	engine := e.engine
	e.mu.Unlock()
	if rebind != nil {
		return rebind(trimmedWorkdir)
	}
	if engine != nil {
		engine.SetTranscriptWorkingDir(trimmedWorkdir)
	}
	return nil
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

func (d *runtimeDirectory) acquireOrCreateBuilding(sessionID string) (*runtimeEntry, bool, bool) {
	id := strings.TrimSpace(sessionID)
	if d == nil || id == "" {
		return nil, false, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if entry := d.entries[id]; entry != nil {
		if entry.isClosing() {
			return entry, false, true
		}
		return entry, true, false
	}
	d.generation++
	entry := newBuildingRuntimeEntry(d.generation)
	d.entries[id] = entry
	return entry, false, false
}

func (d *runtimeDirectory) installBuildingIfAbsent(sessionID string) (*runtimeEntry, *runtimeEntry) {
	id := strings.TrimSpace(sessionID)
	if d == nil || id == "" {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing := d.entries[id]; existing != nil {
		return nil, existing
	}
	d.generation++
	entry := newBuildingRuntimeEntry(d.generation)
	d.entries[id] = entry
	return entry, nil
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

func (e *runtimeEntry) isClosing() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closing
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
	if g == nil || g.entry == nil {
		return fmt.Errorf("runtime guard is unavailable")
	}
	return g.entry.rebindWorkdir(workdir)
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
