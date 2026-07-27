package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	shardThreshold = 30
	maxShards      = 24

	runtimePackagePath                 = "core/server/runtime"
	runtimeAdmissionHeldEnvironment    = "KENT_TESTSHARD_RUNTIME_ADMISSION_HELD"
	runtimeAdmissionLockScriptRelative = "scripts/runtime-test-lock.py"
)

func main() {
	workers := flag.Int("workers", min(runtime.NumCPU(), 18), "maximum concurrent package test processes")
	packagesPattern := flag.String("packages", "./...", "Go package pattern to test")
	flag.Parse()
	if *workers <= 0 {
		fatalf("--workers must be positive")
	}

	packages, err := listPackages(*packagesPattern)
	if err != nil {
		fatalf("list Go packages: %v", err)
	}
	jobs, err := planJobs(packages, *workers)
	if err != nil {
		fatalf("plan Go test shards: %v", err)
	}
	if requiresRuntimeAdmission(jobs) {
		if err := runWithRuntimeAdmission(); err != nil {
			fatalf("admit runtime test shards: %v", err)
		}
		return
	}
	if err := runJobs(jobs, *workers); err != nil {
		fatalf("run Go test shards: %v", err)
	}
}

func requiresRuntimeAdmission(jobs []testJob) bool {
	if os.Getenv(runtimeAdmissionHeldEnvironment) == "1" {
		return false
	}
	for _, job := range jobs {
		if job.packagePath == runtimePackagePath {
			return true
		}
	}
	return false
}

func runWithRuntimeAdmission() error {
	arguments := append(
		[]string{runtimeAdmissionLockScriptRelative, os.Args[0]},
		os.Args[1:]...,
	)
	command := exec.Command("python3", arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(), runtimeAdmissionHeldEnvironment+"=1")
	return command.Run()
}

func listPackages(pattern string) ([]goPackage, error) {
	command := exec.Command("go", "list", "-json", pattern)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(&output)
	packages := make([]goPackage, 0)
	for decoder.More() {
		var pkg goPackage
		if err := decoder.Decode(&pkg); err != nil {
			return nil, err
		}
		if pkg.ImportPath == "" || pkg.Dir == "" {
			return nil, fmt.Errorf("Go list returned package without import path or directory: %+v", pkg)
		}
		packages = append(packages, pkg)
	}
	sort.Slice(packages, func(left, right int) bool {
		return packages[left].ImportPath < packages[right].ImportPath
	})
	return packages, nil
}

func planJobs(packages []goPackage, workers int) ([]testJob, error) {
	jobs := make([]testJob, 0, len(packages))
	for _, pkg := range packages {
		discovery, err := discoverTestRoots(pkg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pkg.ImportPath, err)
		}
		testRoots := discovery.roots
		if discovery.hasTestMain {
			jobs = append(jobs, testJob{
				packagePath:     pkg.ImportPath,
				testRootCount:   len(testRoots),
				estimatedWeight: sumRootWeights(testRoots),
			})
			continue
		}
		appendShardableJobs(&jobs, pkg, testRoots, workers)
	}
	assignPackageScheduling(&jobs)
	return jobs, nil
}

func assignPackageScheduling(jobs *[]testJob) {
	packageWeights := make(map[string]int)
	packageJobs := make(map[string][]int)
	for index := range *jobs {
		job := &(*jobs)[index]
		packageWeights[job.packagePath] += job.estimatedWeight
		packageJobs[job.packagePath] = append(packageJobs[job.packagePath], index)
	}
	for packagePath, indexes := range packageJobs {
		sort.Slice(indexes, func(left, right int) bool {
			leftJob := (*jobs)[indexes[left]]
			rightJob := (*jobs)[indexes[right]]
			if leftJob.estimatedWeight != rightJob.estimatedWeight {
				return leftJob.estimatedWeight > rightJob.estimatedWeight
			}
			return strings.Join(leftJob.testNames, "\x00") < strings.Join(rightJob.testNames, "\x00")
		})
		for shardIndex, index := range indexes {
			(*jobs)[index].packageEstimatedWeight = packageWeights[packagePath]
			(*jobs)[index].packageShardIndex = shardIndex
		}
	}
}

func appendShardableJobs(
	jobs *[]testJob,
	pkg goPackage,
	testRoots []testRoot,
	workers int,
) {
	if workers < 2 || len(testRoots) < shardThreshold {
		*jobs = append(*jobs, testJob{
			packagePath:     pkg.ImportPath,
			testRootCount:   len(testRoots),
			estimatedWeight: sumRootWeights(testRoots),
		})
		return
	}
	shardCount := min(maxShards, workers*2)
	shardCount = min(shardCount, (len(testRoots)+shardThreshold-1)/shardThreshold)
	if shardCount < 2 {
		*jobs = append(*jobs, testJob{
			packagePath:     pkg.ImportPath,
			testRootCount:   len(testRoots),
			estimatedWeight: sumRootWeights(testRoots),
		})
		return
	}
	for _, shard := range distribute(testRoots, shardCount) {
		*jobs = append(*jobs, testJob{
			packagePath:     pkg.ImportPath,
			testNames:       shard.names,
			testRootCount:   len(shard.names),
			estimatedWeight: shard.weight,
		})
	}
}

type testDiscovery struct {
	roots       []testRoot
	hasTestMain bool
}

func discoverTestRoots(pkg goPackage) (testDiscovery, error) {
	files := append(append([]string(nil), pkg.TestGoFiles...), pkg.XTestGoFiles...)
	if len(files) == 0 {
		return testDiscovery{}, nil
	}
	rootsByName := make(map[string]testRoot)
	hasTestMain := false
	parsedFiles := make([]*ast.File, 0, len(files))
	for _, filename := range files {
		path := filepath.Join(pkg.Dir, filename)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return testDiscovery{}, err
		}
		parsedFiles = append(parsedFiles, file)
	}
	executableExamples := make(map[string]bool)
	for _, example := range doc.Examples(parsedFiles...) {
		if example.Output != "" || example.EmptyOutput {
			executableExamples["Example"+example.Name] = true
		}
	}
	for _, file := range parsedFiles {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name == nil {
				continue
			}
			name := function.Name.Name
			if name == "TestMain" && hasTestingPointerParameter(function, "M") {
				hasTestMain = true
				continue
			}
			if isTestRoot(function, executableExamples) {
				root := testRoot{
					name:   name,
					weight: testRootWeight(function),
				}
				if existing, exists := rootsByName[name]; exists {
					existing.weight += root.weight
					rootsByName[name] = existing
					continue
				}
				rootsByName[name] = root
			}
		}
	}
	roots := make([]testRoot, 0, len(rootsByName))
	for _, root := range rootsByName {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(left, right int) bool {
		return roots[left].name < roots[right].name
	})
	return testDiscovery{roots: roots, hasTestMain: hasTestMain}, nil
}

func testRootWeight(function *ast.FuncDecl) int {
	weight := 1
	ast.Inspect(function.Body, func(ast.Node) bool {
		weight++
		return true
	})
	return weight
}

func sumRootWeights(roots []testRoot) int {
	total := 0
	for _, root := range roots {
		total += root.weight
	}
	return total
}

func isTestRoot(function *ast.FuncDecl, executableExamples map[string]bool) bool {
	if function == nil || function.Name == nil {
		return false
	}
	name := function.Name.Name
	switch {
	case isGoTestName(name, "Test"):
		return hasTestingPointerParameter(function, "T")
	case isGoTestName(name, "Fuzz"):
		return hasTestingPointerParameter(function, "F")
	case isGoTestName(name, "Example"):
		return hasNoParametersOrResults(function) && executableExamples[name]
	default:
		return false
	}
}

func isGoTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	first, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(first)
}

func hasTestingPointerParameter(function *ast.FuncDecl, typeName string) bool {
	if function.Type == nil || function.Type.Params == nil || len(function.Type.Params.List) != 1 ||
		len(function.Type.Params.List[0].Names) > 1 ||
		(function.Type.Results != nil && len(function.Type.Results.List) != 0) {
		return false
	}
	pointer, ok := function.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	if ident, ok := pointer.X.(*ast.Ident); ok && ident.Name == typeName {
		return true
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	return ok && selector.Sel != nil && selector.Sel.Name == typeName
}

func hasNoParametersOrResults(function *ast.FuncDecl) bool {
	if function.Type == nil {
		return false
	}
	return (function.Type.Params == nil || len(function.Type.Params.List) == 0) &&
		(function.Type.Results == nil || len(function.Type.Results.List) == 0)
}

func distribute(roots []testRoot, shardCount int) []testShard {
	shards := make([]testShard, shardCount)
	sortedRoots := append([]testRoot(nil), roots...)
	sort.Slice(sortedRoots, func(left, right int) bool {
		if sortedRoots[left].weight != sortedRoots[right].weight {
			return sortedRoots[left].weight > sortedRoots[right].weight
		}
		return sortedRoots[left].name < sortedRoots[right].name
	})
	for _, root := range sortedRoots {
		target := 0
		for index := 1; index < len(shards); index++ {
			if shards[index].weight < shards[target].weight {
				target = index
			}
		}
		shards[target].names = append(shards[target].names, root.name)
		shards[target].weight += root.weight
	}
	for index := range shards {
		sort.Strings(shards[index].names)
	}
	return shards
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "go test sharder: "+format+"\n", args...)
	os.Exit(1)
}
