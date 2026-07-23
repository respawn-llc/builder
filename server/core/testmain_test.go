package core

import (
	"os"
	"testing"

	"core/shared/config"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv(config.PersistenceRootEnvName)
	os.Exit(m.Run())
}
