package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type goPackage struct {
	ImportPath   string
	Dir          string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

type selection struct {
	GoPackages []string
	Desktop    bool
	FullServer bool
}

func main() {
	repoRoot, err := repositoryRoot()
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	changedFiles, err := changedFiles(repoRoot)
	if err != nil {
		fatalf("list changed files: %v", err)
	}
	packages, err := listPackages(repoRoot)
	if err != nil {
		fatalf("list Go packages: %v", err)
	}
	result := selectAffected(repoRoot, packages, changedFiles)
	switch {
	case result.FullServer:
		fmt.Println("full-server")
	default:
		for _, packagePath := range result.GoPackages {
			fmt.Printf("server-package\t%s\n", packagePath)
		}
	}
	if result.Desktop {
		fmt.Println("desktop")
	}
	if !result.FullServer && !result.Desktop && len(result.GoPackages) == 0 {
		fmt.Println("none")
	}
}

func repositoryRoot() (string, error) {
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(output))), nil
}

func changedFiles(repoRoot string) ([]string, error) {
	base, err := mergeBase(repoRoot)
	if err != nil {
		return nil, err
	}
	tracked, err := gitNullSeparated(repoRoot, "diff", "--name-only", "-z", "--diff-filter=ACDMRTUXB", base, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := gitNullSeparated(repoRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	unique := make(map[string]struct{}, len(tracked)+len(untracked))
	for _, path := range append(tracked, untracked...) {
		cleaned := filepath.ToSlash(filepath.Clean(path))
		if cleaned != "." {
			unique[cleaned] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for path := range unique {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func mergeBase(repoRoot string) (string, error) {
	baseRef := strings.TrimSpace(os.Getenv("KENT_TEST_BASE_REF"))
	if baseRef == "" {
		command := exec.Command("git", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		command.Dir = repoRoot
		if output, err := command.Output(); err == nil {
			baseRef = strings.TrimSpace(string(output))
		}
	}
	if baseRef == "" {
		for _, candidate := range []string{"origin/main", "main", "origin/master", "master"} {
			command := exec.Command("git", "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
			command.Dir = repoRoot
			if command.Run() == nil {
				baseRef = candidate
				break
			}
		}
	}
	if baseRef == "" {
		return "", errors.New("no default branch ref found; set KENT_TEST_BASE_REF")
	}
	command := exec.Command("git", "merge-base", "HEAD", baseRef)
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("merge HEAD with %s: %w", baseRef, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func gitNullSeparated(repoRoot string, arguments ...string) ([]string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(output, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			result = append(result, string(part))
		}
	}
	return result, nil
}

func listPackages(repoRoot string) ([]goPackage, error) {
	command := exec.Command("go", "list", "-json", "./...")
	command.Dir = repoRoot
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(&output)
	var packages []goPackage
	for decoder.More() {
		var pkg goPackage
		if err := decoder.Decode(&pkg); err != nil {
			return nil, err
		}
		if pkg.ImportPath != "" && pkg.Dir != "" {
			packages = append(packages, pkg)
		}
	}
	return packages, nil
}

func selectAffected(repoRoot string, packages []goPackage, changedFiles []string) selection {
	result := selection{}
	for _, path := range changedFiles {
		switch {
		case globalGoInput(path):
			result.FullServer = true
		case path == "apps" || strings.HasPrefix(path, "apps/"):
			result.Desktop = true
		}
	}
	if !result.FullServer {
		result.GoPackages = affectedPackages(repoRoot, packages, changedFiles)
		if hasUnownedGoFile(repoRoot, packages, changedFiles) {
			result.FullServer = true
			result.GoPackages = nil
		}
	}
	return result
}

func hasUnownedGoFile(repoRoot string, packages []goPackage, changedFiles []string) bool {
	for _, changedFile := range changedFiles {
		if filepath.Ext(changedFile) != ".go" {
			continue
		}
		absolutePath := filepath.Join(repoRoot, filepath.FromSlash(changedFile))
		owned := false
		for _, pkg := range packages {
			if pathWithin(absolutePath, pkg.Dir) {
				owned = true
				break
			}
		}
		if !owned {
			return true
		}
	}
	return false
}

func globalGoInput(path string) bool {
	switch path {
	case "go.mod", "go.sum":
		return true
	}
	if !strings.Contains(path, "/") && filepath.Ext(path) == ".go" {
		return true
	}
	return path == "scripts" ||
		strings.HasPrefix(path, "scripts/") ||
		path == "tools" ||
		strings.HasPrefix(path, "tools/")
}

func affectedPackages(repoRoot string, packages []goPackage, changedFiles []string) []string {
	repoRoot = filepath.Clean(repoRoot)
	packageByPath := make(map[string]goPackage, len(packages))
	directlyChanged := make(map[string]bool)
	for _, pkg := range packages {
		packageByPath[pkg.ImportPath] = pkg
	}
	for _, changedFile := range changedFiles {
		absolutePath := filepath.Join(repoRoot, filepath.FromSlash(changedFile))
		var owner *goPackage
		for index := range packages {
			pkg := &packages[index]
			if pathWithin(absolutePath, pkg.Dir) &&
				(owner == nil || len(pkg.Dir) > len(owner.Dir)) {
				owner = pkg
			}
		}
		if owner != nil {
			directlyChanged[owner.ImportPath] = true
		}
	}
	affected := make(map[string]bool, len(directlyChanged))
	for importPath := range directlyChanged {
		affected[importPath] = true
	}
	for {
		added := false
		for _, pkg := range packages {
			if affected[pkg.ImportPath] {
				continue
			}
			for _, dependency := range packageDependencies(pkg) {
				if affected[dependency] {
					affected[pkg.ImportPath] = true
					added = true
					break
				}
			}
		}
		if !added {
			break
		}
	}
	result := make([]string, 0, len(affected))
	for importPath := range affected {
		if _, exists := packageByPath[importPath]; exists {
			result = append(result, importPath)
		}
	}
	sort.Strings(result)
	return result
}

func packageDependencies(pkg goPackage) []string {
	result := make([]string, 0, len(pkg.Imports)+len(pkg.TestImports)+len(pkg.XTestImports))
	result = append(result, pkg.Imports...)
	result = append(result, pkg.TestImports...)
	result = append(result, pkg.XTestImports...)
	return result
}

func pathWithin(path, directory string) bool {
	relative, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(path))
	return err == nil && relative != ".." &&
		relative != "." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "affected tests: "+format+"\n", arguments...)
	os.Exit(1)
}
