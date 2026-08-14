package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoverTestRootsIncludesInternalAndExternalRoots(t *testing.T) {
	dir := t.TempDir()
	writeTestSource(t, filepath.Join(dir, "internal_test.go"), `package fixture
import "testing"
func TestMain(m *testing.M) {}
func TestInternal(t *testing.T) {}
func Test(t *testing.T) {}
func ExampleInternal() {
// Output:
}
func Example() {
// Output:
}
func FuzzInternal(f *testing.F) {}
func Fuzz(f *testing.F) {}
func TestExclusive(t *testing.T) {}
func TestShared(t *testing.T) {}
func Testhelper(t *testing.T) {}
func TestInvalid() {}
func ExampleInvalid(arg string) {}
func FuzzInvalid(t *testing.T) {}
`)
	writeTestSource(t, filepath.Join(dir, "external_test.go"), `package fixture_test
import testpkg "testing"
func TestShared(t *testpkg.T) {}
func TestExternal(t *testpkg.T) {}
func ExampleExternal() {
// Output:
}
func FuzzExternal(f *testpkg.F) {}
`)

	discovery, err := discoverTestRoots(goPackage{
		ImportPath:   "core/fixture",
		Dir:          dir,
		TestGoFiles:  []string{"internal_test.go"},
		XTestGoFiles: []string{"external_test.go"},
	})
	if err != nil {
		t.Fatalf("discover roots: %v", err)
	}
	roots := discovery.roots
	names := make([]string, 0, len(roots))
	for _, root := range roots {
		names = append(names, root.name)
	}
	want := []string{
		"Example",
		"ExampleExternal",
		"ExampleInternal",
		"Fuzz",
		"FuzzExternal",
		"FuzzInternal",
		"Test",
		"TestExclusive",
		"TestExternal",
		"TestInternal",
		"TestShared",
	}
	if !equalStrings(names, want) {
		t.Fatalf("roots = %v, want %v", names, want)
	}
}

func TestDiscoverTestRootsUsesGoCanonicalTestFunctionShapes(t *testing.T) {
	dir := t.TempDir()
	writeTestSource(t, filepath.Join(dir, "internal_test.go"), `package fixture
func TestMain(m *testkit.M) {}
func TestInternal(t *testkit.T) {}
func FuzzInternal(f *testkit.F) {}
`)
	writeTestSource(t, filepath.Join(dir, "external_test.go"), `package fixture_test
func TestExternal(t *other.T) {}
`)

	discovery, err := discoverTestRoots(goPackage{
		ImportPath:   "core/fixture",
		Dir:          dir,
		TestGoFiles:  []string{"internal_test.go"},
		XTestGoFiles: []string{"external_test.go"},
	})
	if err != nil {
		t.Fatalf("discover roots: %v", err)
	}
	if !discovery.hasTestMain {
		t.Fatal("TestMain with a testing.M alias was not discovered")
	}
	names := make([]string, 0, len(discovery.roots))
	for _, root := range discovery.roots {
		names = append(names, root.name)
	}
	if want := []string{"FuzzInternal", "TestExternal", "TestInternal"}; !equalStrings(names, want) {
		t.Fatalf("roots = %v, want %v", names, want)
	}
}

func TestDiscoverTestRootsExcludesExamplesWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	writeTestSource(t, filepath.Join(dir, "fixture_test.go"), `package fixture
import "testing"
func ExampleWithoutOutput() {}
// Output:
func ExampleWithExplicitEmptyOutput() {
// Output:
}
func TestRoot(t *testing.T) {}
`)

	discovery, err := discoverTestRoots(goPackage{
		ImportPath:  "core/fixture",
		Dir:         dir,
		TestGoFiles: []string{"fixture_test.go"},
	})
	if err != nil {
		t.Fatalf("discover roots: %v", err)
	}
	names := make([]string, 0, len(discovery.roots))
	for _, root := range discovery.roots {
		names = append(names, root.name)
	}
	if want := []string{"ExampleWithExplicitEmptyOutput", "TestRoot"}; !equalStrings(names, want) {
		t.Fatalf("roots = %v, want %v", names, want)
	}
}

func TestPlanJobsAssignsEveryDiscoveredRootExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	const roots = 101
	source := "package fixture\nimport \"testing\"\n"
	for index := 0; index < roots; index++ {
		source += "func TestRoot" + itoa(index) + "(t *testing.T) {}\n"
	}
	writeTestSource(t, filepath.Join(dir, "fixture_test.go"), source)

	jobs, err := planJobs([]goPackage{{
		ImportPath:  "core/fixture",
		Dir:         dir,
		TestGoFiles: []string{"fixture_test.go"},
	}}, 12)
	if err != nil {
		t.Fatalf("plan jobs: %v", err)
	}
	seen := make(map[string]struct{})
	for _, job := range jobs {
		for _, name := range job.testNames {
			if _, exists := seen[name]; exists {
				t.Fatalf("duplicate root %q", name)
			}
			seen[name] = struct{}{}
		}
	}
	if len(seen) != roots {
		t.Fatalf("planned roots = %d, want %d", len(seen), roots)
	}
}

func TestPlanJobsDoesNotShardPackagesWithTestMain(t *testing.T) {
	dir := t.TempDir()
	const roots = shardThreshold
	source := "package fixture\nimport \"testing\"\nfunc TestMain(m *testing.M) {}\n"
	for index := 0; index < roots; index++ {
		source += "func TestRoot" + itoa(index) + "(t *testing.T) {}\n"
	}
	writeTestSource(t, filepath.Join(dir, "fixture_test.go"), source)

	jobs, err := planJobs([]goPackage{{
		ImportPath:  "core/fixture",
		Dir:         dir,
		TestGoFiles: []string{"fixture_test.go"},
	}}, 12)
	if err != nil {
		t.Fatalf("plan jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want one unsharded TestMain package", len(jobs))
	}
	if jobs[0].testNames != nil {
		t.Fatalf("test names = %v, want an unfiltered package test", jobs[0].testNames)
	}
	if jobs[0].testRootCount != roots {
		t.Fatalf("test roots = %d, want %d", jobs[0].testRootCount, roots)
	}
}

func TestPlanJobsDoesNotShardWhenOnlyOneWorkerIsAvailable(t *testing.T) {
	dir := t.TempDir()
	const roots = shardThreshold + 1
	source := "package fixture\nimport \"testing\"\n"
	for index := 0; index < roots; index++ {
		source += "func TestRoot" + itoa(index) + "(t *testing.T) {}\n"
	}
	writeTestSource(t, filepath.Join(dir, "fixture_test.go"), source)

	jobs, err := planJobs([]goPackage{{
		ImportPath:  "core/fixture",
		Dir:         dir,
		TestGoFiles: []string{"fixture_test.go"},
	}}, 1)
	if err != nil {
		t.Fatalf("plan jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].testNames != nil || jobs[0].testRootCount != roots {
		t.Fatalf("jobs = %+v, want one unfiltered job with %d roots", jobs, roots)
	}
}

func TestRunScheduledJobsUsesWorkerBound(t *testing.T) {
	jobs := []testJob{
		{packagePath: "first"},
		{packagePath: "second"},
		{packagePath: "third"},
	}
	started := make(chan testJob, len(jobs))
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runScheduledJobs(jobs, 2, func(job testJob) error {
			started <- job
			<-release
			return nil
		})
	}()

	<-started
	<-started
	select {
	case unexpected := <-started:
		t.Fatalf("job %+v started beyond the worker bound", unexpected)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("run scheduled jobs: %v", err)
	}
	if final := <-started; final.packagePath != "third" {
		t.Fatalf("final job = %+v, want third", final)
	}
}

func TestOrderJobsBalancesPackageWeightAcrossShards(t *testing.T) {
	jobs := []testJob{
		{packagePath: "core/high", packageEstimatedWeight: 100, packageShardIndex: 0, estimatedWeight: 50},
		{packagePath: "core/high", packageEstimatedWeight: 100, packageShardIndex: 1, estimatedWeight: 50},
		{packagePath: "core/low", packageEstimatedWeight: 60, packageShardIndex: 0, estimatedWeight: 10},
	}
	orderJobs(jobs)
	got := []string{jobs[0].packagePath, jobs[1].packagePath, jobs[2].packagePath}
	want := []string{"core/high", "core/low", "core/high"}
	if !equalStrings(got, want) {
		t.Fatalf("job order = %v, want %v", got, want)
	}
}

func TestPlanJobsRetainsPackagesWithoutTestRoots(t *testing.T) {
	jobs, err := planJobs([]goPackage{{ImportPath: "core/fixture", Dir: t.TempDir()}}, 1)
	if err != nil {
		t.Fatalf("plan jobs: %v", err)
	}
	if len(jobs) != 1 || len(jobs[0].testNames) != 0 || jobs[0].testRootCount != 0 {
		t.Fatalf("jobs = %+v, want one all-package job", jobs)
	}
}

func TestExactTestExpressionEscapesEveryRootName(t *testing.T) {
	got := exactTestExpression([]string{"TestPlain", "Test[a].+", "Example(path)"})
	want := "^(TestPlain|Test\\[a\\]\\.\\+|Example\\(path\\))$"
	if got != want {
		t.Fatalf("expression = %q, want %q", got, want)
	}
}

func TestGoTestArgumentsLimitEachShardToOnePackageBuild(t *testing.T) {
	got := goTestArguments(testJob{
		packagePath: "core/fixture",
		testNames:   []string{"TestOne", "TestTwo"},
	})
	want := []string{
		"test",
		"-json",
		"-count=1",
		"-p",
		"1",
		"-parallel",
		"4",
		"core/fixture",
		"-run",
		"^(TestOne|TestTwo)$",
	}
	if !equalStrings(got, want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
}

func TestUnshardedJobEventOmitsAbsentRootListHash(t *testing.T) {
	event := completedJobEvent(testJob{
		packagePath:   "core/fixture",
		testRootCount: 1,
	}, time.Now(), nil)
	if event.RootListSHA256 != nil {
		t.Fatalf("root list hash = %q, want absent", *event.RootListSHA256)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal job event: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal job event: %v", err)
	}
	if _, exists := fields["rootListSHA256"]; exists {
		t.Fatalf("unsharded job event contains a root list hash: %s", encoded)
	}
}

func writeTestSource(t *testing.T, path string, source string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
