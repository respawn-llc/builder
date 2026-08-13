package core_test

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

type repositoryBoundaryFinding string

const (
	repositoryBackendImportsCLI   repositoryBoundaryFinding = "backend_imports_cli"
	repositorySharedImportsServer repositoryBoundaryFinding = "shared_imports_server"
	repositoryCLIUsesServer       repositoryBoundaryFinding = "cli_uses_unapproved_server_capability"
	repositoryCLIUsesPersistence  repositoryBoundaryFinding = "cli_uses_persistence_capability"
	repositoryClientUIPolicy      repositoryBoundaryFinding = "clientui_execution_policy"
)

func TestRepositoryDependencyAndCapabilityBoundaries(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, platform := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"windows", "amd64"}} {
		pkgs := testharness.LoadTypedPackagesForPlatform(t, repoRoot, false, platform[0], platform[1], "./server/...", "./shared/...", "./cli/...")
		if findings := repositoryBoundaryFindings(pkgs); len(findings) != 0 {
			t.Fatalf("%s repository boundary violations: %v", platform[0], findings)
		}
	}
}

func TestRepositoryBoundaryAnalyzerRejectsEachViolationKind(t *testing.T) {
	root := t.TempDir()
	testharness.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	files := map[string]string{
		"cli/base/base.go":       "package base",
		"server/base/base.go":    "package base",
		"server/bad/bad.go":      "package bad\nimport _ \"core/cli/base\"\n",
		"shared/cli/bad.go":      "package cli\nimport _ \"core/cli/base\"\n",
		"shared/bad/bad.go":      "package bad\nimport _ \"core/server/base\"\n",
		"server/auth/auth.go":    "package auth\ntype Client struct{}\nfunc (Client) ForbiddenMethod() {}\n",
		"server/metadata/db.go":  "package metadata\nfunc Open() {}\n",
		"cli/app/app.go":         "package app\nimport \"core/server/auth\"\nfunc use(c auth.Client) { c.ForbiddenMethod() }\n",
		"cli/persist/persist.go": "package persist\nimport \"core/server/metadata\"\nfunc use() { metadata.Open() }\n",
		"shared/clientui/bad.go": "package clientui\nimport \"context\"\ntype Callback func()\ntype Events chan int\ntype Runner struct{ Run Callback; Events Events }\nfunc (Runner) Execute(context.Context) {}\n",
	}
	for path, source := range files {
		testharness.WriteFile(t, filepath.Join(root, path), source)
	}
	pkgs := testharness.LoadTypedPackages(t, root, false, "./...")
	got := map[repositoryBoundaryFinding]bool{}
	for _, finding := range repositoryBoundaryFindings(pkgs) {
		got[finding] = true
	}
	for _, want := range []repositoryBoundaryFinding{
		repositoryBackendImportsCLI,
		repositorySharedImportsServer,
		repositoryCLIUsesServer,
		repositoryCLIUsesPersistence,
		repositoryClientUIPolicy,
	} {
		if !got[want] {
			t.Errorf("findings = %v, want %s", got, want)
		}
	}
}

func repositoryBoundaryFindings(pkgs []*packages.Package) []repositoryBoundaryFinding {
	var findings []repositoryBoundaryFinding
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		for imported := range pkg.Imports {
			switch {
			case (pkg.PkgPath == "core/server" || strings.HasPrefix(pkg.PkgPath, "core/server/") ||
				strings.HasPrefix(pkg.PkgPath, "core/shared/")) &&
				strings.HasPrefix(imported, "core/cli/"):
				findings = append(findings, repositoryBackendImportsCLI)
			case strings.HasPrefix(pkg.PkgPath, "core/shared/") &&
				(imported == "core/server" || strings.HasPrefix(imported, "core/server/")):
				findings = append(findings, repositorySharedImportsServer)
			case strings.HasPrefix(pkg.PkgPath, "core/cli/") &&
				(imported == "core/server" || strings.HasPrefix(imported, "core/server/")) &&
				approvedCLIServerCapabilities[pkg.PkgPath][imported] == nil:
				findings = append(findings, classifyCLIServerFinding(imported))
			}
		}
		if !strings.HasPrefix(pkg.PkgPath, "core/cli/") {
			if pkg.PkgPath == "core/shared/clientui" {
				for _, file := range pkg.Syntax {
					ast.Inspect(file, func(node ast.Node) bool {
						if function, ok := node.(*ast.FuncDecl); ok &&
							signatureUsesContext(pkg.TypesInfo.Defs[function.Name].Type().(*types.Signature)) {
							findings = append(findings, repositoryClientUIPolicy)
						}
						typeSpec, ok := node.(*ast.TypeSpec)
						if !ok {
							return true
						}
						structure, ok := typeSpec.Type.(*ast.StructType)
						if !ok {
							return false
						}
						for _, field := range structure.Fields.List {
							switch types.Unalias(pkg.TypesInfo.TypeOf(field.Type)).Underlying().(type) {
							case *types.Signature, *types.Chan:
								findings = append(findings, repositoryClientUIPolicy)
							}
						}
						return false
					})
				}
			}
			continue
		}
		for _, object := range pkg.TypesInfo.Uses {
			if object == nil || object.Pkg() == nil {
				continue
			}
			serverPackage := object.Pkg().Path()
			if serverPackage != "core/server" && !strings.HasPrefix(serverPackage, "core/server/") {
				continue
			}
			name := serverCapabilityName(object)
			if name != "" && !approvedCLIServerCapabilities[pkg.PkgPath][serverPackage][name] {
				findings = append(findings, classifyCLIServerFinding(serverPackage))
			}
		}
	}
	return findings
}

func serverCapabilityName(object types.Object) string {
	function, ok := object.(*types.Func)
	if !ok {
		if object.Parent() != object.Pkg().Scope() {
			return ""
		}
		return object.Name()
	}
	signature := function.Type().(*types.Signature)
	if signature.Recv() == nil {
		return function.Name()
	}
	receiver := types.Unalias(signature.Recv().Type())
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = types.Unalias(pointer.Elem())
	}
	named, _ := receiver.(*types.Named)
	return named.Obj().Name() + "." + function.Name()
}

func classifyCLIServerFinding(serverPackage string) repositoryBoundaryFinding {
	if serverPackage == "core/server/metadata" || serverPackage == "core/server/session" {
		return repositoryCLIUsesPersistence
	}
	return repositoryCLIUsesServer
}

var approvedCLIServerCapabilities = map[string]map[string]map[string]bool{
	"core/cli/app": {
		"core/server/auth": {
			"BeginOpenAIBrowserFlow": true, "CollectOpenAIDeviceAuthorizationGrant": true,
			"OpenBrowser": true, "ParseOAuthCallbackInput": true, "StartOAuthCallbackListener": true,
		},
		"core/server/llm": {"ModelDisplayLabel": true},
	},
	"core/cli/app/internal/authui": {
		"core/server/auth": {"BrowserCallback": true, "DeviceCode": true, "OpenAIOAuthOptions": true},
	},
	"core/cli/app/internal/status": {
		"core/server/llm":     {"ModelDisplayLabel": true},
		"core/server/runtime": {"InspectSkills": true, "InstalledAgentsPaths": true, "SkillInspection": true},
	},
	"core/cli/app/internal/startupconfig": {
		"core/server/bootstrap": {
			"InitialConfigSnapshot": true, "Request": true, "ResolveConfig": true, "ValidateSessionExists": true,
		},
	},
	"core/cli/kent": {
		"core/server/startup": {
			"AuthHandler": true, "NewHeadlessHandlers": true, "OnboardingHandler": true,
			"Request": true, "StartServeServer": true,
		},
	},
}

func findRepoRoot(t testing.TB) string { return testharness.RepositoryRoot(t) }
