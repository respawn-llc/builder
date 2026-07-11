package app

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"core/internal/testharness/pty/appfixture"
	"core/shared/config"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv(config.PersistenceRootEnvName)
	if configPath, fixtureProcess := os.LookupEnv(appfixture.ProcessConfigEnvName); fixtureProcess {
		processConfig, err := appfixture.ReadProcessConfig(configPath)
		if err == nil {
			err = os.Setenv(config.PersistenceRootEnvName, processConfig.PersistenceRoot)
		}
		if err == nil {
			err = runPTYFixtureProcess(context.Background(), processConfig)
		}
		if err != nil {
			log.Print(err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	previousDuration := transientStatusDuration
	transientStatusDuration = 30 * time.Millisecond
	code := m.Run()
	transientStatusDuration = previousDuration
	os.Exit(code)
}
