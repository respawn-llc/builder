package core_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSmallPackagesRemainExplicitlyClassified(t *testing.T) {
	repoRoot := findRepoRoot(t)
	packages := listRepoPackages(t, repoRoot)
	smallPackages := map[string]smallPackageInfo{}
	allPackages := map[string]struct{}{}
	for _, pkg := range packages {
		path := strings.TrimPrefix(pkg.ImportPath, "core/")
		allPackages[path] = struct{}{}
		if pkg.isSmall() {
			smallPackages[path] = pkg
		}
	}

	violations := make([]string, 0)
	for path, pkg := range smallPackages {
		if strings.TrimSpace(allowedSmallPackages[path]) == "" {
			violations = append(violations, path+" is a small/test-only first-party package without an explicit merge/exception classification"+
				" (go_files="+strconv.Itoa(pkg.GoFiles)+", test_files="+strconv.Itoa(pkg.TestGoFiles+pkg.XTestGoFiles)+")")
		}
	}
	for path, reason := range allowedSmallPackages {
		if strings.TrimSpace(reason) == "" {
			violations = append(violations, path+" has an empty small-package exception rationale")
			continue
		}
		if _, ok := allPackages[path]; !ok {
			violations = append(violations, path+" is a stale small-package exception for a package that no longer exists")
			continue
		}
		if _, ok := smallPackages[path]; !ok {
			violations = append(violations, path+" is a stale small-package exception for a package that is no longer small")
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("small-package guardrail violations:\n%s", strings.Join(violations, "\n"))
	}
}

type goListSmallPackage struct {
	ImportPath   string   `json:"ImportPath"`
	GoFiles      []string `json:"GoFiles"`
	TestGoFiles  []string `json:"TestGoFiles"`
	XTestGoFiles []string `json:"XTestGoFiles"`
}

type smallPackageInfo struct {
	ImportPath   string
	GoFiles      int
	TestGoFiles  int
	XTestGoFiles int
}

func (p smallPackageInfo) isSmall() bool {
	testFiles := p.TestGoFiles + p.XTestGoFiles
	return p.GoFiles <= 2 || (p.GoFiles == 0 && testFiles > 0)
}

func listRepoPackages(t *testing.T, repoRoot string) []smallPackageInfo {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list: %v\n%s", err, string(exitErr.Stderr))
		}
		t.Fatalf("go list: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	packages := make([]smallPackageInfo, 0)
	for {
		var pkg goListSmallPackage
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode go list package: %v", err)
		}
		packages = append(packages, smallPackageInfo{
			ImportPath:   pkg.ImportPath,
			GoFiles:      len(pkg.GoFiles),
			TestGoFiles:  len(pkg.TestGoFiles),
			XTestGoFiles: len(pkg.XTestGoFiles),
		})
	}
	return packages
}

var allowedSmallPackages = map[string]string{
	"cli/app/internal/daemonlaunch":      "narrow process helper that intentionally isolates daemon process ownership and termination behavior from attachment policy",
	"cli/app/internal/embeddedattach":    "narrow embedded-server attachment seam after absorbing embedded binding/startup helpers",
	"cli/app/internal/projectbinding":    "interactive project binding workflow seam after absorbing project picker behavior",
	"cli/app/internal/remoteattach":      "narrow remote attachment seam after absorbing remote binding",
	"cli/app/internal/runtimestate":      "DTO-only reducer boundary; package-level tests enforce stdlib plus shared/clientui imports only",
	"cli/app/internal/startupconfig":     "narrow CLI startup config-resolution seam after absorbing serve-command env construction",
	"server/bootstrap":                   "composition support boundary shared by core and startup; merging into startup creates a cycle",
	"server/metadata/lifecyclegen":       "repo-owned generator command for the narrow SQLite lifecycle generated seam",
	"server/metadata/sqlitelifecyclegen": "generated SQLite lifecycle seam isolated from sqlc output because sqlc does not emit transaction-scoped PRAGMA statements",
	"server/projectview":                 "cohesive project read-model service owner with substantial service tests",
	"server/runlog":                      "shared run-logging and runtime-event formatting helpers extracted from runprompt so sessionruntime and workflowrunner consume them without the runprompt import cycle",
	"server/requestmemo":                 "cross-service infrastructure used by runtime, prompt, session, process, and workflow services",
	"server/runtimecontrol":              "cohesive runtime mutation service owner with focused service tests",
	"server/runtimeview":                 "cohesive runtime projection owner with focused service tests",
	"server/serverstatus":                "server status/update-status service owner after status consolidation",
	"server/sessionlaunch":               "session launch service seam kept separate from session runtime to avoid runprompt/runtime cycles",
	"server/session/sessiontest":         "test-only helper package exposing full event-history collectors kept out of the production session surface so production code cannot materialize whole histories",
	"server/sessionruntime":              "session runtime service seam kept separate from session launch to avoid runprompt/runtime cycles",
	"server/workflowrunner":              "workflow run-starting owner after absorbing scheduler and workflow runtime test helpers",
	"server/workflowruntime":             "runtime/workflow contract boundary imported by server runtime; merging into runner would invert dependencies",
	"server/workflowsvc":                 "workflow mutation service owner with service tests",
	"server/worktree":                    "cohesive worktree service owner with focused service tests",
	"shared/apicontract":                 "shared API route/service contract owner after absorbing RPC and service contracts",
	"shared/auth":                        "low-level shared auth contract required below server/auth and shared/serverapi",
	"shared/llmerrors":                   "shared provider-error contract surfaced by CLI and server",
	"shared/modelcontract":               "shared model identifier contract needed by server/llm and shared clients",
	"shared/rollbacktarget":              "shared session rollback target contract used by CLI and server session lifecycle",
	"shared/rpcwire":                     "cohesive shared RPC wire encoding owner",
	"shared/sessioncontract":             "shared session contract required below config/startup and server session packages",
	"shared/sessionenv":                  "shared session environment contract used by CLI commands and shell env construction",
	"shared/toolspec":                    "shared model-facing tool spec contract required below runtime, runtimewire, and clients",
	"shared/transcriptdiag":              "transcript diagnostic DTO adapter kept separate because transcript and clientui dependencies would cycle",
	"shared/workflowkey":                 "shared workflow key contract required by workflow validation and shared server API",
}
