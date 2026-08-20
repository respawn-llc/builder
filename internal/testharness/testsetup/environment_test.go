package testsetup

import (
	"testing"

	"core/internal/testharness/testenv"
	"core/shared/config"
)

func TestTestSetupProcessClearsPersistenceRoot(t *testing.T) {
	testenv.AssertUnsetAtProcessStart(
		t,
		"TestTestSetupProcessClearsPersistenceRoot",
		config.PersistenceRootEnvName,
	)
}
