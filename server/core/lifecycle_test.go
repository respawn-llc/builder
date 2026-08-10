package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/shared/config"
	"core/shared/runtimeids"
)

func TestLifecycleFatalReporterInstallsUnavailableGateAndSignalsShutdownWithoutClosingCore(t *testing.T) {
	state := newLifecycleFatalState(context.Background())
	appCore := &Core{fatalLifecycle: state}
	persistenceErr := errors.New("persist admitted interruption")
	scopeID := runtimeids.NewExecutionScopeID()
	diagnostic := workflowexecution.LifecycleFatalDiagnostic{
		Operation:          workflowexecution.LifecycleFatalOperationOutcomeLessFinalization,
		TaskID:             "KENT-438",
		CurrentNode:        workflow.CurrentNodeReference{TaskID: "KENT-438", NodeID: "agent"},
		RunID:              7,
		RunPhase:           workflowexecution.LifecycleFatalRunPhaseRetiring,
		ExpectedScheduling: workflow.CurrentNodeSchedulingAdmitted,
		ScopeID:            &scopeID,
		OriginalOutcome:    errors.New("exact returned without outcome"),
		PersistenceFailure: persistenceErr,
	}

	reported := make(chan workflowexecution.LifecycleFatalReportResult, 1)
	go func() {
		reported <- state.ReportFatal(diagnostic)
	}()
	select {
	case result := <-reported:
		if !result.ShutdownAccepted {
			t.Fatal("first fatal report did not accept shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("fatal reporting blocked on Core shutdown")
	}
	if err := appCore.RouteDependencyAvailable(""); !errors.Is(err, persistenceErr) {
		t.Fatalf("Core availability error = %v, want persistence failure", err)
	}
	select {
	case <-appCore.LifecycleFatalShutdown():
	default:
		t.Fatal("fatal report did not signal the ServeServer owner")
	}
	if err := appCore.Close(); err != nil {
		t.Fatalf("close Core after report: %v", err)
	}
}

func TestLifecycleFatalReporterJoinsLaterDiagnosticsWithoutSecondShutdown(t *testing.T) {
	state := newLifecycleFatalState(context.Background())
	first := state.ReportFatal(workflowexecution.LifecycleFatalDiagnostic{
		Operation:          workflowexecution.LifecycleFatalOperationExactRuntimeFailure,
		TaskID:             "task",
		CurrentNode:        workflow.CurrentNodeReference{TaskID: "task", NodeID: "first"},
		RunID:              1,
		RunPhase:           workflowexecution.LifecycleFatalRunPhaseExact,
		ExpectedScheduling: workflow.CurrentNodeSchedulingAdmitted,
		OriginalOutcome:    errors.New("runner failed"),
		PersistenceFailure: errors.New("first persistence failure"),
	})
	second := state.ReportFatal(workflowexecution.LifecycleFatalDiagnostic{
		Operation:          workflowexecution.LifecycleFatalOperationControllerClose,
		TaskID:             "task",
		CurrentNode:        workflow.CurrentNodeReference{TaskID: "task", NodeID: "second"},
		RunID:              2,
		RunPhase:           workflowexecution.LifecycleFatalRunPhaseLaunching,
		ExpectedScheduling: workflow.CurrentNodeSchedulingReady,
		OriginalOutcome:    errors.New("controller closed"),
		PersistenceFailure: errors.New("second persistence failure"),
	})

	if !first.ShutdownAccepted || second.ShutdownAccepted {
		t.Fatalf("shutdown acceptance = first %t, second %t; want true then false", first.ShutdownAccepted, second.ShutdownAccepted)
	}
	err := state.Available()
	if err == nil || !strings.Contains(err.Error(), "first persistence failure") || !strings.Contains(err.Error(), "second persistence failure") {
		t.Fatalf("joined fatal diagnostics = %v", err)
	}
}

func TestCoreCloseClosesResourcesOnceInReverseRegistrationOrder(t *testing.T) {
	var calls []string
	appCore := &Core{
		bundles: &Bundles{
			cleanup: []lifecycleResource{
				{name: "root lock", close: func() error {
					calls = append(calls, "root lock")
					return nil
				}},
				{name: "metadata store", close: func() error {
					calls = append(calls, "metadata store")
					return nil
				}},
				{name: "background manager", close: func() error {
					calls = append(calls, "background manager")
					return nil
				}},
			},
		},
	}

	if err := appCore.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	if err := appCore.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}
	want := []string{"background manager", "metadata store", "root lock"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("close calls = %v, want %v", calls, want)
	}
}

func TestComposeBundlesClosesWorkflowExecutionBeforeAuthorityAndPersistence(t *testing.T) {
	bundles := composeBundles(bundleCompositionInput{})
	registrationIndex := make(map[string]int, len(bundles.cleanup))
	for index, resource := range bundles.cleanup {
		registrationIndex[resource.name] = index
	}

	metadataIndex, hasMetadata := registrationIndex["metadata store"]
	authorityIndex, hasAuthority := registrationIndex["session runtime authority"]
	starterIndex, hasStarter := registrationIndex["workflow runtime starter"]
	controllerIndex, hasController := registrationIndex["workflow execution controller"]
	if !hasMetadata || !hasAuthority || !hasStarter || !hasController {
		t.Fatalf("cleanup resources = %+v, want metadata, authority, starter, and controller", registrationIndex)
	}
	if !(metadataIndex < authorityIndex && authorityIndex < starterIndex && starterIndex < controllerIndex) {
		t.Fatalf(
			"cleanup registration = %+v, want reverse close order controller, starter, authority, metadata",
			registrationIndex,
		)
	}
}

func TestCoreCloseNamesFailedResources(t *testing.T) {
	wantErr := errors.New("boom")
	appCore := &Core{
		bundles: &Bundles{
			cleanup: []lifecycleResource{
				{name: "metadata store", close: func() error {
					return wantErr
				}},
			},
		},
	}

	err := appCore.Close()
	if err == nil {
		t.Fatal("expected close error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Close error = %v, want wrapped %v", err, wantErr)
	}
	if got := err.Error(); got != "metadata store: boom" {
		t.Fatalf("Close error text = %q, want resource name", got)
	}
}

func TestNewWithContextNamesMissingAuthBundleResource(t *testing.T) {
	cfg := config.App{
		PersistenceRoot: t.TempDir(),
		Settings: config.Settings{
			Shell: config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	_, err = NewWithContext(t.Context(), cfg, serverbootstrap.AuthSupport{}, runtimeSupport)
	if err == nil {
		t.Fatal("expected NewWithContext error")
	}
	var missing BundleResourceRequiredError
	if !errors.As(err, &missing) || missing.BundleName != "auth" || missing.ResourceName != "auth manager" {
		t.Fatalf("error = %v, want auth bundle/resource name", err)
	}
}

func TestNewWithContextNamesMissingRuntimeBundleResource(t *testing.T) {
	cfg := config.App{PersistenceRoot: t.TempDir()}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}

	_, err = NewWithContext(t.Context(), cfg, authSupport, serverbootstrap.RuntimeSupport{})
	if err == nil {
		t.Fatal("expected NewWithContext error")
	}
	var missing BundleResourceRequiredError
	if !errors.As(err, &missing) || missing.BundleName != "runtime" || missing.ResourceName != "background manager" {
		t.Fatalf("error = %v, want runtime bundle/resource name", err)
	}
}

func TestNewWithContextCleansPersistenceOnAuthBundleFailure(t *testing.T) {
	cfg := config.App{
		PersistenceRoot: t.TempDir(),
		Settings: config.Settings{
			Shell:    config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
			Workflow: config.WorkflowSettings{Concurrency: 1},
		},
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport first: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	_, err = NewWithContext(t.Context(), cfg, serverbootstrap.AuthSupport{}, runtimeSupport)
	if err == nil {
		t.Fatal("expected first NewWithContext error")
	}

	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupportSecond, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport second: %v", err)
	}
	appCore, err := NewWithContext(t.Context(), cfg, authSupport, runtimeSupportSecond)
	if err != nil {
		t.Fatalf("NewWithContext after failed construction: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
}
