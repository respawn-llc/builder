package main

import (
	"reflect"
	"testing"
)

func TestAffectedPackagesIncludeChangedPackageAndReverseDependencies(t *testing.T) {
	packages := []goPackage{
		{ImportPath: "core/shared", Dir: "/repo/shared"},
		{ImportPath: "core/server", Dir: "/repo/server", Imports: []string{"core/shared"}},
		{ImportPath: "core/cli", Dir: "/repo/cli", TestImports: []string{"core/server"}},
		{ImportPath: "core/unrelated", Dir: "/repo/unrelated"},
	}

	got := affectedPackages("/repo", packages, []string{"shared/value.go"})
	want := []string{"core/cli", "core/server", "core/shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("affectedPackages() = %v, want %v", got, want)
	}
}

func TestAffectedPackagesMapTestdataToOwningPackage(t *testing.T) {
	packages := []goPackage{
		{ImportPath: "core/server/session", Dir: "/repo/server/session"},
	}

	got := affectedPackages(
		"/repo",
		packages,
		[]string{"server/session/testdata/legacy/events.jsonl"},
	)
	want := []string{"core/server/session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("affectedPackages() = %v, want %v", got, want)
	}
}

func TestSelectionFallsBackToFullServerForGlobalGoInputs(t *testing.T) {
	selection := selectAffected(
		"/repo",
		[]goPackage{{ImportPath: "core/server", Dir: "/repo/server"}},
		[]string{"go.mod"},
	)

	if !selection.FullServer {
		t.Fatal("selectAffected() did not select the full server suite")
	}
	if len(selection.GoPackages) != 0 {
		t.Fatalf("selectAffected() packages = %v, want none with full-server fallback", selection.GoPackages)
	}
}

func TestSelectionFallsBackToFullServerForDeletedGoPackage(t *testing.T) {
	selection := selectAffected(
		"/repo",
		[]goPackage{{ImportPath: "core/server", Dir: "/repo/server"}},
		[]string{"removed/package/store.go"},
	)

	if !selection.FullServer {
		t.Fatal("selectAffected() did not select the full server suite")
	}
}

func TestSelectionIncludesDesktopOnlyForDesktopInputs(t *testing.T) {
	selection := selectAffected(
		"/repo",
		[]goPackage{{ImportPath: "core/server", Dir: "/repo/server"}},
		[]string{"apps/desktop/src/App.tsx"},
	)

	if !selection.Desktop {
		t.Fatal("selectAffected() did not select desktop tests")
	}
	if selection.FullServer || len(selection.GoPackages) != 0 {
		t.Fatalf("selectAffected() unexpectedly selected server tests: %+v", selection)
	}
}

func TestSelectionIgnoresDocumentationOnlyChanges(t *testing.T) {
	selection := selectAffected(
		"/repo",
		[]goPackage{{ImportPath: "core/server", Dir: "/repo/server"}},
		[]string{"docs/content/index.mdx"},
	)

	if selection.Desktop || selection.FullServer || len(selection.GoPackages) != 0 {
		t.Fatalf("selectAffected() = %+v, want no tests", selection)
	}
}

func TestSelectionFallsBackToFullServerForRootGoInputs(t *testing.T) {
	selection := selectAffected(
		"/repo",
		[]goPackage{{ImportPath: "core/server", Dir: "/repo/server"}},
		[]string{"tools.go"},
	)

	if !selection.FullServer {
		t.Fatal("selectAffected() did not select the full server suite")
	}
}
