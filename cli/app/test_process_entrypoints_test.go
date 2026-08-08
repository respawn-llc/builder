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

const onboardingRemoteLifecycleTestRunArgument = "-test.run=^TestOnboardingRemoteLifecycleProcess$"

func init() {
	if appTestProcessInvocation() {
		return
	}
	transientStatusDuration = 30 * time.Millisecond
	spinnerTickInterval = time.Millisecond
}

func appTestProcessInvocation() bool {
	if isLifecycleHookProductRecorderInvocation(os.Args) {
		return true
	}
	for _, environmentName := range []string{
		onboardingRemoteLifecycleConfigEnv,
		appfixture.LifecycleServerProcessConfigEnvName,
		appfixture.LifecycleProcessConfigEnvName,
		appfixture.ProcessConfigEnvName,
	} {
		if _, found := os.LookupEnv(environmentName); found {
			return true
		}
	}
	return false
}

func TestOnboardingRemoteLifecycleProcess(t *testing.T) {
	configPath, runningHelper := os.LookupEnv(onboardingRemoteLifecycleConfigEnv)
	if !runningHelper {
		t.Skip("onboarding remote lifecycle process runs only through its exact subprocess invocation")
	}
	exitAppTestProcess(runOnboardingRemoteLifecycleHelper(configPath))
}

func TestLifecycleHookServerFixtureProcess(t *testing.T) {
	configPath, runningHelper := os.LookupEnv(appfixture.LifecycleServerProcessConfigEnvName)
	if !runningHelper {
		t.Skip("lifecycle server fixture process runs only through its exact subprocess invocation")
	}
	processConfig, err := appfixture.ReadLifecycleServerProcessConfig(configPath)
	if err == nil {
		err = os.Setenv(config.PersistenceRootEnvName, processConfig.PersistenceRoot)
	}
	if err == nil {
		err = runLifecycleHookServerFixtureProcess(context.Background(), processConfig)
	}
	exitAppTestProcess(err)
}

func TestLifecycleHookPTYFixtureProcess(t *testing.T) {
	configPath, runningHelper := os.LookupEnv(appfixture.LifecycleProcessConfigEnvName)
	if !runningHelper {
		t.Skip("lifecycle PTY fixture process runs only through its exact subprocess invocation")
	}
	processConfig, err := appfixture.ReadLifecycleProcessConfig(configPath)
	if err == nil {
		err = os.Setenv(config.PersistenceRootEnvName, processConfig.PersistenceRoot)
	}
	if err == nil {
		err = runLifecycleHookPTYFixtureProcess(context.Background(), processConfig)
	}
	exitAppTestProcess(err)
}

func TestPTYFixtureProcess(t *testing.T) {
	configPath, runningHelper := os.LookupEnv(appfixture.ProcessConfigEnvName)
	if !runningHelper {
		t.Skip("PTY fixture process runs only through its exact subprocess invocation")
	}
	processConfig, err := appfixture.ReadProcessConfig(configPath)
	if err == nil {
		err = os.Setenv(config.PersistenceRootEnvName, processConfig.PersistenceRoot)
	}
	if err == nil {
		err = runPTYFixtureProcess(context.Background(), processConfig)
	}
	exitAppTestProcess(err)
}

func exitAppTestProcess(err error) {
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	os.Exit(0)
}
