package registry

import (
	"testing"

	"core/internal/testharness/runtimewirefixture"
	"core/server/tools"
)

func newTestToolRegistry(t testing.TB, registrations ...tools.HandlerRegistration) *tools.Registry {
	t.Helper()
	return runtimewirefixture.NewToolRegistry(t, registrations...)
}
