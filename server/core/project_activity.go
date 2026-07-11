package core

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var (
	ErrProjectActivityAdmissionClosed             = errors.New("project activity admission is closed")
	ErrProjectActivityProjectAdmissionClosed      = errors.New("project activity admission is closed for project")
	ErrProjectActivityCreateInProgress            = errors.New("project creation is already in progress")
	ErrProjectActivityLifecycleGenerationMismatch = errors.New("project lifecycle generation does not match")
	ErrProjectActivityDeleteInProgress            = errors.New("project deletion is already in progress")
)

// projectActivityCoordinator owns Core's live project admission under the
// persistence-root lease. Durable project lifecycle state remains metadata's
// responsibility; this coordinator only serializes live operations and tokens.
type projectActivityCoordinator struct {
	mu            sync.Mutex
	initialized   bool
	admissionOpen bool
	tracked       int
	drained       chan struct{}
	projects      map[string]*projectActivityGate
}

func (c *projectActivityCoordinator) AcquireProjectActivity(projectID string) (func(), error) {
	projectID, err := requiredProjectActivityProjectID(projectID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.initializeLocked()
	if !c.admissionOpen {
		return nil, ErrProjectActivityAdmissionClosed
	}
	gate := c.projectLocked(projectID)
	if gate.closed {
		return nil, ErrProjectActivityProjectAdmissionClosed
	}
	gate.active++
	c.trackLocked()
	return c.activeRelease(projectID, gate), nil
}

// ReserveCreate fences a project before its durable row or artifacts exist.
// The caller must either Activate after inserting the active row or Release.
func (c *projectActivityCoordinator) ReserveCreate(projectID string) (*projectCreateReservation, error) {
	projectID, err := requiredProjectActivityProjectID(projectID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.initializeLocked()
	if !c.admissionOpen {
		return nil, ErrProjectActivityAdmissionClosed
	}
	gate := c.projectLocked(projectID)
	if gate.closed {
		return nil, ErrProjectActivityProjectAdmissionClosed
	}
	if gate.createReserved {
		return nil, ErrProjectActivityCreateInProgress
	}
	gate.createReserved = true
	c.trackLocked()
	return &projectCreateReservation{coordinator: c, projectID: projectID, gate: gate}, nil
}

// BeginDelete closes admission for one project, then waits for only that
// project's existing shared permits. The resulting token is not one of the
// permits it drains.
func (c *projectActivityCoordinator) BeginDelete(ctx context.Context, projectID string) (*projectDeleteToken, error) {
	projectID, err := requiredProjectActivityProjectID(projectID)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	c.initializeLocked()
	if !c.admissionOpen {
		c.mu.Unlock()
		return nil, ErrProjectActivityAdmissionClosed
	}
	gate := c.projectLocked(projectID)
	if gate.createReserved {
		c.mu.Unlock()
		return nil, ErrProjectActivityCreateInProgress
	}
	if gate.closed {
		c.mu.Unlock()
		return nil, ErrProjectActivityDeleteInProgress
	}
	gate.closed = true
	gate.deleteActive = true
	c.trackLocked()
	c.signalGateLocked(gate)
	token := &projectDeleteToken{coordinator: c, projectID: projectID, gate: gate}
	c.mu.Unlock()

	for {
		c.mu.Lock()
		if gate.active == 0 {
			c.mu.Unlock()
			return token, nil
		}
		changed := gate.changed
		c.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			c.mu.Lock()
			if gate.active == 0 {
				c.mu.Unlock()
				return token, nil
			}
			c.reopenDeleteLocked(projectID, gate)
			c.mu.Unlock()
			token.markReleased()
			return nil, ctx.Err()
		}
	}
}

// AcquireCleanupRetry serializes cleanup work for an already durable deleting
// project. It never reopens project admission.
func (c *projectActivityCoordinator) AcquireCleanupRetry(ctx context.Context, projectID string, generation int64) (*projectCleanupToken, error) {
	projectID, err := requiredProjectActivityProjectID(projectID)
	if err != nil {
		return nil, err
	}
	if generation <= 0 {
		return nil, ErrProjectActivityLifecycleGenerationMismatch
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		c.mu.Lock()
		c.initializeLocked()
		if !c.admissionOpen {
			c.mu.Unlock()
			return nil, ErrProjectActivityAdmissionClosed
		}
		gate := c.projects[projectID]
		if gate == nil || !gate.closed || gate.lifecycleGeneration == nil || *gate.lifecycleGeneration != generation {
			c.mu.Unlock()
			return nil, ErrProjectActivityLifecycleGenerationMismatch
		}
		if !gate.cleanupActive {
			gate.cleanupActive = true
			c.trackLocked()
			c.signalGateLocked(gate)
			token := &projectCleanupToken{
				coordinator: c,
				projectID:   projectID,
				gate:        gate,
				generation:  generation,
			}
			c.mu.Unlock()
			return token, nil
		}
		changed := gate.changed
		c.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *projectActivityCoordinator) CloseAdmission() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.initializeLocked()
	c.admissionOpen = false
}

func (c *projectActivityCoordinator) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	c.initializeLocked()
	if c.tracked == 0 {
		c.mu.Unlock()
		return nil
	}
	drained := c.drained
	c.mu.Unlock()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *projectActivityCoordinator) activeRelease(projectID string, gate *projectActivityGate) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			gate.active--
			c.untrackLocked()
			c.signalGateLocked(gate)
			c.removeIdleProjectLocked(projectID, gate)
		})
	}
}

func (c *projectActivityCoordinator) reopenDeleteLocked(projectID string, gate *projectActivityGate) {
	if !gate.deleteActive {
		return
	}
	gate.deleteActive = false
	gate.closed = false
	c.untrackLocked()
	c.signalGateLocked(gate)
	c.removeIdleProjectLocked(projectID, gate)
}

func (c *projectActivityCoordinator) projectLocked(projectID string) *projectActivityGate {
	if gate := c.projects[projectID]; gate != nil {
		return gate
	}
	gate := &projectActivityGate{changed: make(chan struct{})}
	c.projects[projectID] = gate
	return gate
}

func (c *projectActivityCoordinator) removeIdleProjectLocked(projectID string, gate *projectActivityGate) {
	if gate.active == 0 && !gate.createReserved && !gate.deleteActive && !gate.cleanupActive && !gate.closed {
		delete(c.projects, projectID)
	}
}

func (c *projectActivityCoordinator) trackLocked() {
	if c.tracked == 0 {
		c.drained = make(chan struct{})
	}
	c.tracked++
}

func (c *projectActivityCoordinator) untrackLocked() {
	c.tracked--
	if c.tracked == 0 {
		close(c.drained)
	}
}

func (c *projectActivityCoordinator) signalGateLocked(gate *projectActivityGate) {
	close(gate.changed)
	gate.changed = make(chan struct{})
}

func (c *projectActivityCoordinator) initializeLocked() {
	if c.initialized {
		return
	}
	c.initialized = true
	c.admissionOpen = true
	c.drained = make(chan struct{})
	close(c.drained)
	c.projects = make(map[string]*projectActivityGate)
}

func requiredProjectActivityProjectID(projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", errors.New("project id is required")
	}
	return projectID, nil
}

type projectActivityGate struct {
	active              int
	createReserved      bool
	closed              bool
	deleteActive        bool
	cleanupActive       bool
	lifecycleGeneration *int64
	changed             chan struct{}
}

type projectCreateReservation struct {
	coordinator *projectActivityCoordinator
	projectID   string
	gate        *projectActivityGate

	mu       sync.Mutex
	released bool
}

// Activate atomically replaces the provisional create reservation with a
// shared permit, so deletion cannot enter an admission gap between them.
func (r *projectCreateReservation) Activate() (func(), error) {
	if r == nil {
		return nil, errors.New("project create reservation is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return nil, ErrProjectActivityCreateInProgress
	}
	r.coordinator.mu.Lock()
	defer r.coordinator.mu.Unlock()
	if !r.coordinator.admissionOpen {
		return nil, ErrProjectActivityAdmissionClosed
	}
	if !r.gate.createReserved || r.gate.closed {
		return nil, ErrProjectActivityProjectAdmissionClosed
	}
	r.gate.createReserved = false
	r.gate.active++
	r.released = true
	r.coordinator.signalGateLocked(r.gate)
	return r.coordinator.activeRelease(r.projectID, r.gate), nil
}

func (r *projectCreateReservation) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return
	}
	r.coordinator.mu.Lock()
	defer r.coordinator.mu.Unlock()
	if r.gate.createReserved {
		r.gate.createReserved = false
		r.coordinator.untrackLocked()
		r.coordinator.signalGateLocked(r.gate)
		r.coordinator.removeIdleProjectLocked(r.projectID, r.gate)
	}
	r.released = true
}

type projectDeleteToken struct {
	coordinator *projectActivityCoordinator
	projectID   string
	gate        *projectActivityGate

	mu       sync.Mutex
	released bool
}

func (t *projectDeleteToken) Reopen() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return
	}
	t.coordinator.mu.Lock()
	defer t.coordinator.mu.Unlock()
	t.coordinator.reopenDeleteLocked(t.projectID, t.gate)
	t.released = true
}

// PromoteToCleanup converts a successful generation-fenced durable deletion
// transition into its matching cleanup token without reopening admission.
func (t *projectDeleteToken) PromoteToCleanup(generation int64) (*projectCleanupToken, error) {
	if t == nil {
		return nil, errors.New("project delete token is required")
	}
	if generation <= 0 {
		return nil, ErrProjectActivityLifecycleGenerationMismatch
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return nil, ErrProjectActivityDeleteInProgress
	}
	t.coordinator.mu.Lock()
	defer t.coordinator.mu.Unlock()
	if !t.gate.deleteActive || !t.gate.closed {
		return nil, ErrProjectActivityDeleteInProgress
	}
	t.gate.deleteActive = false
	t.gate.cleanupActive = true
	t.gate.lifecycleGeneration = &generation
	t.released = true
	t.coordinator.signalGateLocked(t.gate)
	return &projectCleanupToken{
		coordinator: t.coordinator,
		projectID:   t.projectID,
		gate:        t.gate,
		generation:  generation,
	}, nil
}

func (t *projectDeleteToken) markReleased() {
	t.mu.Lock()
	t.released = true
	t.mu.Unlock()
}

type projectCleanupToken struct {
	coordinator *projectActivityCoordinator
	projectID   string
	gate        *projectActivityGate
	generation  int64

	once sync.Once
}

func (t *projectCleanupToken) Release() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		t.coordinator.mu.Lock()
		defer t.coordinator.mu.Unlock()
		if !t.gate.cleanupActive || t.gate.lifecycleGeneration == nil || *t.gate.lifecycleGeneration != t.generation {
			return
		}
		t.gate.cleanupActive = false
		t.coordinator.untrackLocked()
		t.coordinator.signalGateLocked(t.gate)
	})
}
