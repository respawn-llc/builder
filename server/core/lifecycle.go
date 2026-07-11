package core

import (
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
