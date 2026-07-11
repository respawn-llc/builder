package core

import (
	"errors"
	"fmt"
	"sync"
)

type lifecycleResource struct {
	name  string
	close func() error
}

// lifecycleController serializes cleanup and retains the failed reverse-order
// barrier so a later Close can retry without re-closing completed resources.
type lifecycleController struct {
	mu          sync.Mutex
	next        int
	initialized bool
}

func (c *lifecycleController) Close(resources []lifecycleResource) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.initialized {
		c.next = len(resources)
		c.initialized = true
	}
	for c.next > 0 {
		resource := resources[c.next-1]
		if resource.close != nil {
			if err := resource.close(); err != nil {
				return fmt.Errorf("%s: %w", resource.name, err)
			}
		}
		c.next--
	}
	return nil
}

// StartupCleanupError retains the constructed Core when a startup failure and
// its cleanup both fail. Callers must retry Close through the retained owner;
// closing individual resources would violate the lifecycle barrier ordering.
type StartupCleanupError struct {
	Startup error
	Cleanup error
	core    *Core
}

func (e *StartupCleanupError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("startup failed: %v; cleanup failed: %v", e.Startup, e.Cleanup)
}

func (e *StartupCleanupError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.Startup, e.Cleanup}
}

// RetryClose retries the retained Core's failed cleanup barrier.
func (e *StartupCleanupError) RetryClose() error {
	if e == nil || e.core == nil {
		return nil
	}
	return e.core.Close()
}

// Close implements the retained lifecycle owner's normal close operation.
func (e *StartupCleanupError) Close() error {
	return e.RetryClose()
}

// RetainedStartupCleanupCore returns the lifecycle owner from a startup
// cleanup error. It is the only owner allowed to retry teardown.
func RetainedStartupCleanupCore(err error) (*Core, bool) {
	var cleanupErr *StartupCleanupError
	if !errors.As(err, &cleanupErr) || cleanupErr.core == nil {
		return nil, false
	}
	return cleanupErr.core, true
}

func startupFailureWithCleanup(core *Core, startupErr error) error {
	if core == nil {
		return startupErr
	}
	if cleanupErr := core.Close(); cleanupErr != nil {
		return &StartupCleanupError{
			Startup: startupErr,
			Cleanup: cleanupErr,
			core:    core,
		}
	}
	return startupErr
}

// BundleResourceRequiredError reports that a required resource for a server
// bundle was not supplied. It carries the bundle and resource names so callers
// match the specific missing dependency with errors.As instead of parsing the
// rendered message.
type BundleResourceRequiredError struct {
	BundleName   string
	ResourceName string
}

func (e BundleResourceRequiredError) Error() string {
	return fmt.Sprintf("%s bundle: %s is required", e.BundleName, e.ResourceName)
}

func bundleResourceRequiredError(bundleName string, resourceName string) error {
	return BundleResourceRequiredError{BundleName: bundleName, ResourceName: resourceName}
}
