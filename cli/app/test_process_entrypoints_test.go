package app

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"core/internal/testharness/pty/appfixture"
)

const onboardingRemoteLifecycleTestRunArgument = "-test.run=^TestOnboardingRemoteLifecycleProcess$"

func init() {
	if !appTestProcessInvocation() {
		transientStatusDuration, spinnerTickInterval = 30*time.Millisecond, time.Millisecond
	}
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
	runAppFixtureProcess(t, appfixture.LifecycleServerProcessConfigEnvName, appfixture.ReadLifecycleServerProcessConfig, func(value appfixture.LifecycleServerProcessConfig) error {
		return runLifecycleHookServerFixtureProcess(t, context.Background(), value)
	})
}

func TestLifecycleHookPTYFixtureProcess(t *testing.T) {
	runAppFixtureProcess(t, appfixture.LifecycleProcessConfigEnvName, appfixture.ReadLifecycleProcessConfig, func(value appfixture.LifecycleProcessConfig) error {
		return runLifecycleHookPTYFixtureProcess(t, context.Background(), value)
	})
}

func TestPTYFixtureProcess(t *testing.T) {
	runAppFixtureProcess(t, appfixture.ProcessConfigEnvName, appfixture.ReadProcessConfig, func(value appfixture.ProcessConfig) error {
		return runPTYFixtureProcess(t, context.Background(), value)
	})
}

func runAppFixtureProcess[T any](
	t *testing.T,
	environmentName string,
	read func(string) (T, error),
	run func(T) error,
) {
	configPath, runningHelper := os.LookupEnv(environmentName)
	if !runningHelper {
		t.Skip("fixture process runs only through its exact subprocess invocation")
	}
	processConfig, err := read(configPath)
	if err == nil {
		err = run(processConfig)
	}
	exitAppTestProcess(err)
}

func exitAppTestProcess(err error) {
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}
