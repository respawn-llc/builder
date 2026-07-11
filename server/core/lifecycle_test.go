package core

import (
	"errors"
	"reflect"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/shared/config"
)

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

func TestCoreCloseRetriesFailedBarrierBeforeClosingDownstreamResources(t *testing.T) {
	fail := true
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
					if fail {
						return errors.New("blocked")
					}
					return nil
				}},
			},
		},
	}

	if err := appCore.Close(); err == nil {
		t.Fatal("first Close unexpectedly succeeded")
	}
	if want := []string{"background manager"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("first close calls = %v, want %v", calls, want)
	}
	fail = false
	if err := appCore.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if want := []string{"background manager", "background manager", "metadata store", "root lock"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("retry close calls = %v, want %v", calls, want)
	}
}

func TestCoreCloseDoesNotRepeatResourcesClosedBeforeRetryBarrier(t *testing.T) {
	fail := true
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
					if fail {
						return errors.New("blocked")
					}
					return nil
				}},
				{name: "background manager", close: func() error {
					calls = append(calls, "background manager")
					return nil
				}},
			},
		},
	}

	if err := appCore.Close(); err == nil {
		t.Fatal("first Close unexpectedly succeeded")
	}
	fail = false
	if err := appCore.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if want := []string{"background manager", "metadata store", "metadata store", "root lock"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("close calls = %v, want %v", calls, want)
	}
}

func TestStartupCleanupErrorRetainsCoreForRetry(t *testing.T) {
	startupErr := errors.New("scheduler start failed")
	cleanupErr := errors.New("background manager blocked")
	fail := true
	appCore := &Core{
		bundles: &Bundles{
			cleanup: []lifecycleResource{
				{name: "root lock", close: func() error { return nil }},
				{name: "background manager", close: func() error {
					if fail {
						return cleanupErr
					}
					return nil
				}},
			},
		},
	}

	err := startupFailureWithCleanup(appCore, startupErr)
	var retained *StartupCleanupError
	if !errors.As(err, &retained) {
		t.Fatalf("startup error = %v, want StartupCleanupError", err)
	}
	if !errors.Is(err, startupErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("startup cleanup error = %v, want both startup and cleanup causes", err)
	}
	owner, ok := RetainedStartupCleanupCore(err)
	if !ok || owner != appCore {
		t.Fatalf("RetainedStartupCleanupCore = owner:%p ok:%t, want %p true", owner, ok, appCore)
	}
	fail = false
	if err := retained.RetryClose(); err != nil {
		t.Fatalf("RetryClose: %v", err)
	}
}

func TestNewWithContextNamesMissingAuthBundleResource(t *testing.T) {
	cfg := config.App{PersistenceRoot: t.TempDir()}
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
	cfg := config.App{PersistenceRoot: t.TempDir()}
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
