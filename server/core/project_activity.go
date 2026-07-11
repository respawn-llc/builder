package core

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var ErrProjectActivityAdmissionClosed = errors.New("project activity admission is closed")

// projectActivityCoordinator owns global admission for project-scoped work.
// It closes admission before shutdown drains already admitted operations.
type projectActivityCoordinator struct {
	mu            sync.Mutex
	initialized   bool
	admissionOpen bool
	active        int
	drained       chan struct{}
	projects      map[string]projectActivityGate
}

func (c *projectActivityCoordinator) AcquireProjectActivity(projectID string) (func(), error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	c.mu.Lock()
	c.initializeLocked()
	if !c.admissionOpen {
		c.mu.Unlock()
		return nil, ErrProjectActivityAdmissionClosed
	}
	if c.active == 0 {
		c.drained = make(chan struct{})
	}
	c.active++
	gate := c.projects[projectID]
	gate.active++
	c.projects[projectID] = gate
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			gate := c.projects[projectID]
			gate.active--
			if gate.active == 0 {
				delete(c.projects, projectID)
			} else {
				c.projects[projectID] = gate
			}
			c.active--
			if c.active == 0 {
				close(c.drained)
			}
		})
	}, nil
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
	if c.active == 0 {
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

func (c *projectActivityCoordinator) initializeLocked() {
	if c.initialized {
		return
	}
	c.initialized = true
	c.admissionOpen = true
	c.drained = make(chan struct{})
	close(c.drained)
	c.projects = make(map[string]projectActivityGate)
}

type projectActivityGate struct {
	active int
}
