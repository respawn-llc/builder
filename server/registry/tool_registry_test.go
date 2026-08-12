package registry

import (
	"testing"

	"core/internal/testharness/toolfixture"
	"core/server/tools"
)

func newTestToolRegistry(t testing.TB, registrations ...tools.HandlerRegistration) *tools.Registry {
	t.Helper()
	return toolfixture.NewRegistry(t, registrations...)
}
