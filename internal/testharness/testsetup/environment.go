package testsetup

import (
	"core/internal/testharness/testenv"
	"core/shared/config"
)

// init applies persistence isolation to every test process that uses testsetup.
func init() {
	testenv.ClearAtProcessStart(config.PersistenceRootEnvName)
}
