package core

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/shared/config"
)

func init() {
	testsetup.ClearEnvironmentAtTestProcessStart(config.PersistenceRootEnvName)
}

func TestCoreTestProcessClearsPersistenceRoot(t *testing.T) {
	testsetup.AssertEnvironmentUnsetAtProcessStart(
		t,
		"TestCoreTestProcessClearsPersistenceRoot",
		config.PersistenceRootEnvName,
	)
}
