package transport

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/shared/config"
)

func init() {
	testsetup.ClearEnvironmentAtTestProcessStart(config.PersistenceRootEnvName)
}

func TestTransportTestProcessClearsPersistenceRoot(t *testing.T) {
	testsetup.AssertEnvironmentUnsetAtProcessStart(
		t,
		"TestTransportTestProcessClearsPersistenceRoot",
		config.PersistenceRootEnvName,
	)
}
