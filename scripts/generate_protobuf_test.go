package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"core/internal/testharness/testsetup"
)

func TestProtobufEnsureIsLazyAndRepairsOutputDrift(t *testing.T) {
	fixture := newProtobufGenerationFixture(t)

	if output, err := fixture.run("ensure", "all"); err != nil {
		t.Fatalf("first ensure: %v\n%s", err, output)
	}
	if output, err := fixture.run("ensure", "all"); err != nil {
		t.Fatalf("second ensure: %v\n%s", err, output)
	}
	if got := fixture.generationCount(t, "go"); got != 1 {
		t.Fatalf("unchanged Go generation count = %d, want 1", got)
	}
	if got := fixture.generationCount(t, "ts"); got != 1 {
		t.Fatalf("unchanged TypeScript generation count = %d, want 1", got)
	}

	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"shared/protoapi/gen/example.pb.go",
		[]byte("manually edited\n"),
	)
	if output, err := fixture.run("ensure", "go"); err != nil {
		t.Fatalf("repair output drift: %v\n%s", err, output)
	}
	if got := fixture.generationCount(t, "go"); got != 2 {
		t.Fatalf("repaired Go generation count = %d, want 2", got)
	}
}

func TestProtobufEnsureInvalidatesOnlyAffectedTarget(t *testing.T) {
	fixture := newProtobufGenerationFixture(t)
	if output, err := fixture.run("ensure", "all"); err != nil {
		t.Fatalf("initial ensure: %v\n%s", err, output)
	}

	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"tools/protobuf/protoc-gen-kent-go-registry/main.go",
		[]byte("changed generator\n"),
	)
	if output, err := fixture.run("ensure", "all"); err != nil {
		t.Fatalf("ensure after Go generator change: %v\n%s", err, output)
	}
	if got := fixture.generationCount(t, "go"); got != 2 {
		t.Fatalf("Go generation count = %d, want 2", got)
	}
	if got := fixture.generationCount(t, "ts"); got != 1 {
		t.Fatalf("TypeScript generation count = %d, want 1", got)
	}
}

func TestProtobufEnsureSerializesConcurrentCalls(t *testing.T) {
	fixture := newProtobufGenerationFixture(t)
	var waitGroup sync.WaitGroup
	errorsByCall := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := fixture.run("ensure", "go")
			errorsByCall <- err
		}()
	}
	waitGroup.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.generationCount(t, "go"); got != 1 {
		t.Fatalf("concurrent Go generation count = %d, want 1", got)
	}
}

func TestProtobufVerifyRejectsNondeterministicGeneration(t *testing.T) {
	fixture := newProtobufGenerationFixture(t)
	fixture.environment = append(fixture.environment, "KENT_PROTOBUF_TEST_NONDETERMINISTIC=1")
	if output, err := fixture.run("verify", "go"); err == nil {
		t.Fatalf("verify accepted nondeterministic generation\n%s", output)
	}
}

type protobufGenerationFixture struct {
	repositoryRoot string
	fakeBinRoot    string
	stateRoot      string
	environment    []string
}

func newProtobufGenerationFixture(t *testing.T) protobufGenerationFixture {
	t.Helper()
	sourceRoot := repositoryRoot(t)
	fixtureRoot := t.TempDir()
	fixture := protobufGenerationFixture{
		repositoryRoot: filepath.Join(fixtureRoot, "repository"),
		fakeBinRoot:    filepath.Join(fixtureRoot, "bin"),
		stateRoot:      filepath.Join(fixtureRoot, "state"),
	}
	for _, relativePath := range []string{
		"api/proto",
		"buf.yaml",
		"buf.lock",
		"buf.gen.go.yaml",
		"buf.gen.ts.yaml",
		"scripts/generate-protobuf.sh",
		"tools/protobuf/cmd/protogen",
		"tools/protobuf/internal/protogen",
		"tools/protobuf/internal/registrygen",
		"tools/protobuf/package.json",
		"tools/protobuf/pnpm-lock.yaml",
		"tools/protobuf/protoc-gen-kent-go-registry",
		"tools/protobuf/protoc-gen-kent-ts-registry",
		"tools/protobuf/go.mod",
		"tools/protobuf/go.sum",
	} {
		copyFixturePath(t, sourceRoot, fixture.repositoryRoot, relativePath)
	}
	if err := os.Chmod(
		filepath.Join(fixture.repositoryRoot, "scripts", "generate-protobuf.sh"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	realGoPath := realGo(t)
	installFakeGo(t, fixture.fakeBinRoot)
	testPath := fixture.fakeBinRoot + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", testPath)
	fixture.environment = append(
		testsetup.EnvironmentWithout("PATH", "KENT_PROTOBUF_REAL_GO", "KENT_PROTOBUF_TEST_STATE_ROOT"),
		"PATH="+testPath,
		"KENT_PROTOBUF_TEST_STATE_ROOT="+fixture.stateRoot,
		"KENT_PROTOBUF_REAL_GO="+realGoPath,
		"KENT_PROTOBUF_TS_GENERATOR="+filepath.Join(fixture.fakeBinRoot, "protoc-gen-es"),
	)
	return fixture
}

func (fixture protobufGenerationFixture) run(arguments ...string) ([]byte, error) {
	command := exec.Command(
		filepath.Join(fixture.repositoryRoot, "scripts", "generate-protobuf.sh"),
		arguments...,
	)
	command.Dir = fixture.repositoryRoot
	command.Env = fixture.environment
	return command.CombinedOutput()
}

func (fixture protobufGenerationFixture) generationCount(t *testing.T, target string) int {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixture.stateRoot, target+"-count"))
	if err != nil {
		t.Fatal(err)
	}
	switch string(content) {
	case "1\n":
		return 1
	case "2\n":
		return 2
	case "3\n":
		return 3
	default:
		t.Fatalf("unexpected %s generation count %q", target, content)
		return 0
	}
}

func installFakeGo(t *testing.T, binRoot string) {
	t.Helper()
	const fakeGo = `#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "run" && "${2:-}" == "./cmd/protogen" ]]; then
	mkdir -p "$KENT_PROTOBUF_TEST_STATE_ROOT"
	"$KENT_PROTOBUF_REAL_GO" build -o "$KENT_PROTOBUF_TEST_STATE_ROOT/protogen" ./cmd/protogen
	shift 2
	exec "$KENT_PROTOBUF_TEST_STATE_ROOT/protogen" "$@"
fi
if [[ "${1:-}" == "run" && "${2:-}" == "./shared/apicontract/cmd/migrationlint" ]]; then
	exit 0
fi
if [[ "${1:-}" == "test" ]]; then
	if [[ "${KENT_PROTOBUF_TEST_DESCRIPTOR_POLICY_FAILURE:-0}" == "1" ]]; then
		echo "invalid descriptor metadata" >&2
		exit 1
	fi
	exit 0
fi
if [[ "${1:-}" == "tool" && "${2:-}" == "buf" && "${3:-}" == "lint" ]]; then
	if [[ "${KENT_PROTOBUF_TEST_LINT_FAILURE:-0}" == "1" ]]; then
		echo "invalid schema" >&2
		exit 1
	fi
	exit 0
fi
if [[ "${1:-}" != "tool" || "${2:-}" != "buf" || "${3:-}" != "generate" ]]; then
	echo "unexpected fake go invocation: $*" >&2
	exit 2
fi

target=
output=
while [[ $# -gt 0 ]]; do
	case "$1" in
	--template)
		case "${2:-}" in
		*buf.gen.go.yaml) target=go ;;
		*buf.gen.ts.yaml) target=ts ;;
		esac
		shift 2
		;;
	--output)
		output="${2:-}"
		shift 2
		;;
	*)
		shift
		;;
	esac
done
if [[ -z "$target" || -z "$output" ]]; then
	echo "missing target or output" >&2
	exit 2
fi
mkdir -p "$KENT_PROTOBUF_TEST_STATE_ROOT"
count_file="$KENT_PROTOBUF_TEST_STATE_ROOT/${target}-count"
count=0
if [[ -f "$count_file" ]]; then
	read -r count < "$count_file"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
if [[ "${KENT_PROTOBUF_TEST_NONDETERMINISTIC:-0}" == "1" ]]; then
	content="generated-$count"
else
	content="generated"
fi
case "$target" in
go)
	path="$output/shared/protoapi/gen/example.pb.go"
	;;
ts)
	path="$output/apps/desktop/packages/server-api-contract/src/gen/example_pb.ts"
	;;
esac
mkdir -p "$(dirname "$path")"
printf '%s\n' "$content" > "$path"
`
	writeFixtureFile(t, binRoot, "go", []byte(fakeGo))
	if err := os.Chmod(filepath.Join(binRoot, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, binRoot, "protoc-gen-es", []byte("#!/usr/bin/env bash\nexit 0\n"))
	if err := os.Chmod(filepath.Join(binRoot, "protoc-gen-es"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func realGo(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func copyFixturePath(t *testing.T, sourceRoot, destinationRoot, relativePath string) {
	t.Helper()
	sourcePath := filepath.Join(sourceRoot, relativePath)
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, destinationRoot, relativePath, content)
		if info.Mode()&0o111 != 0 {
			if err := os.Chmod(filepath.Join(destinationRoot, relativePath), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if err := filepath.WalkDir(sourcePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		writeFixtureFile(t, destinationRoot, relative, content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func copyFixtureFile(t *testing.T, sourceRoot, destinationRoot, relativePath string) {
	t.Helper()
	copyFixturePath(t, sourceRoot, destinationRoot, relativePath)
}

func writeFixtureFile(t *testing.T, root, relativePath string, content []byte) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFixtureFile(t *testing.T, root, relativePath string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, relativePath))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}
