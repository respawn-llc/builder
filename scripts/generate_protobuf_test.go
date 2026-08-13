package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

const (
	generatedGoRelativePath         = "shared/protoapi/gen/example.pb.go"
	generatedTypeScriptRelativePath = "apps/desktop/packages/server-api-contract/src/gen/example_pb.ts"
)

func TestProtobufGeneratedOutputCheckRejectsRepositoryDrift(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(t *testing.T, repositoryRoot string)
	}{
		{
			name: "stale content",
			mutate: func(t *testing.T, repositoryRoot string) {
				t.Helper()
				writeFixtureFile(t, repositoryRoot, generatedGoRelativePath, []byte("stale generated Go content\n"))
			},
		},
		{
			name: "extra generated file",
			mutate: func(t *testing.T, repositoryRoot string) {
				t.Helper()
				writeFixtureFile(
					t,
					repositoryRoot,
					"shared/protoapi/gen/unexpected.pb.go",
					[]byte("unexpected generated file\n"),
				)
			},
		},
		{
			name: "deleted generated file",
			mutate: func(t *testing.T, repositoryRoot string) {
				t.Helper()
				if err := os.Remove(filepath.Join(repositoryRoot, generatedTypeScriptRelativePath)); err != nil {
					t.Fatalf("delete generated TypeScript fixture: %v", err)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newProtobufGenerationFixture(t)
			requireProtobufGeneratedOutputCheckPasses(t, fixture)

			testCase.mutate(t, fixture.repositoryRoot)

			if output, err := fixture.runCheck(); err == nil {
				t.Fatalf("generated-output check accepted %s\n%s", testCase.name, output)
			}
		})
	}
}

func TestProtobufGenerationIsByteIdentical(t *testing.T) {
	fixture := newProtobufGenerationFixture(t)

	if output, err := fixture.run("generate"); err != nil {
		t.Fatalf("first generation: %v\n%s", err, output)
	}
	firstGo := readFixtureFile(t, fixture.repositoryRoot, generatedGoRelativePath)
	firstTypeScript := readFixtureFile(t, fixture.repositoryRoot, generatedTypeScriptRelativePath)

	if output, err := fixture.run("generate"); err != nil {
		t.Fatalf("second generation: %v\n%s", err, output)
	}
	if got := readFixtureFile(t, fixture.repositoryRoot, generatedGoRelativePath); string(got) != string(firstGo) {
		t.Fatal("two generations produced different Go bytes")
	}
	if got := readFixtureFile(t, fixture.repositoryRoot, generatedTypeScriptRelativePath); string(got) != string(firstTypeScript) {
		t.Fatal("two generations produced different TypeScript bytes")
	}
}

func TestProtobufGenerationInterruptionRestoresBothOutputTrees(t *testing.T) {
	fixture := newProtobufGenerationFixture(t)
	originalGo := readFixtureFile(t, fixture.repositoryRoot, generatedGoRelativePath)
	originalTypeScript := readFixtureFile(t, fixture.repositoryRoot, generatedTypeScriptRelativePath)
	installInterruptingMv(t, fixture.fakeBinRoot)

	if output, err := fixture.run("generate"); err == nil {
		t.Fatalf("interrupted generation unexpectedly succeeded\n%s", output)
	}
	if got := readFixtureFile(t, fixture.repositoryRoot, generatedGoRelativePath); string(got) != string(originalGo) {
		t.Fatal("interrupted generation did not restore the Go output tree")
	}
	if got := readFixtureFile(t, fixture.repositoryRoot, generatedTypeScriptRelativePath); string(got) != string(originalTypeScript) {
		t.Fatal("interrupted generation did not restore the TypeScript output tree")
	}
}

type protobufGenerationFixture struct {
	repositoryRoot string
	expectedRoot   string
	fakeBinRoot    string
}

func newProtobufGenerationFixture(t *testing.T) protobufGenerationFixture {
	t.Helper()

	sourceRoot := repositoryRoot(t)
	fixtureRoot := t.TempDir()
	fixture := protobufGenerationFixture{
		repositoryRoot: filepath.Join(fixtureRoot, "repository"),
		expectedRoot:   filepath.Join(fixtureRoot, "expected"),
		fakeBinRoot:    filepath.Join(fixtureRoot, "bin"),
	}

	if err := os.MkdirAll(filepath.Join(fixture.repositoryRoot, "scripts"), 0o755); err != nil {
		t.Fatalf("create fixture scripts directory: %v", err)
	}
	entrypoint, err := os.ReadFile(filepath.Join(sourceRoot, "scripts", "generate-protobuf.sh"))
	if err != nil {
		t.Fatalf("read Protobuf generation entry point: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.repositoryRoot, "scripts", "generate-protobuf.sh"),
		entrypoint,
		0o755,
	); err != nil {
		t.Fatalf("install Protobuf generation entry point in fixture: %v", err)
	}
	copyProtobufGenerationInputs(t, sourceRoot, fixture.repositoryRoot)

	writeFixtureFile(t, fixture.expectedRoot, generatedGoRelativePath, []byte("generated Go content\n"))
	writeFixtureFile(
		t,
		fixture.expectedRoot,
		generatedTypeScriptRelativePath,
		[]byte("generated TypeScript content\n"),
	)
	copyFixtureFile(t, fixture.expectedRoot, fixture.repositoryRoot, generatedGoRelativePath)
	copyFixtureFile(t, fixture.expectedRoot, fixture.repositoryRoot, generatedTypeScriptRelativePath)
	installFakeGo(t, fixture.fakeBinRoot)

	return fixture
}

func (fixture protobufGenerationFixture) runCheck() ([]byte, error) {
	return fixture.run("check")
}

func (fixture protobufGenerationFixture) run(mode string) ([]byte, error) {
	command := exec.Command(
		filepath.Join(fixture.repositoryRoot, "scripts", "generate-protobuf.sh"),
		mode,
	)
	command.Dir = fixture.repositoryRoot
	command.Env = append(
		os.Environ(),
		"PATH="+fixture.fakeBinRoot+string(os.PathListSeparator)+os.Getenv("PATH"),
		"KENT_PROTOBUF_TEST_EXPECTED_ROOT="+fixture.expectedRoot,
		"KENT_PROTOBUF_TEST_MV_STATE="+filepath.Join(fixture.expectedRoot, "mv-state"),
	)
	return command.CombinedOutput()
}

func requireProtobufGeneratedOutputCheckPasses(t *testing.T, fixture protobufGenerationFixture) {
	t.Helper()
	if output, err := fixture.runCheck(); err != nil {
		t.Fatalf("check canonical generated-output fixture: %v\n%s", err, output)
	}
}

func installFakeGo(t *testing.T, binRoot string) {
	t.Helper()
	const fakeGo = `#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "test" ]]; then
	if [[ "${KENT_PROTOBUF_TEST_DESCRIPTOR_POLICY_FAILURE:-0}" == "1" ]]; then
		echo "invalid descriptor metadata" >&2
		exit 1
	fi
	exit 0
fi

if [[ "${1:-}" == "run" && "${2:-}" == "./shared/apicontract/cmd/migrationlint" ]]; then
	exit 0
fi

if [[ "${1:-}" != "tool" || "${2:-}" != "buf" ]]; then
	echo "fake go supports only: go test, go run migrationlint, or go tool buf" >&2
	exit 2
fi
case "${3:-}" in
lint)
	if [[ "${KENT_PROTOBUF_TEST_LINT_FAILURE:-0}" == "1" ]]; then
		echo "invalid schema" >&2
		exit 1
	fi
	exit 0
	;;
generate)
	shift 3
	;;
*)
	echo "fake go supports only: go tool buf lint or generate" >&2
	exit 2
	;;
esac

output=
while [[ $# -gt 0 ]]; do
	case "$1" in
	--output | -o)
		output="${2:-}"
		shift 2
		;;
	*)
		shift
		;;
	esac
done

if [[ -z "$output" ]]; then
	echo "buf generate output directory is required" >&2
	exit 2
fi

mkdir -p "$output"
cp -R "$KENT_PROTOBUF_TEST_EXPECTED_ROOT/." "$output/"
`
	writeFixtureFile(t, binRoot, "go", []byte(fakeGo))
	if err := os.Chmod(filepath.Join(binRoot, "go"), 0o755); err != nil {
		t.Fatalf("make fake go executable: %v", err)
	}
}

func installInterruptingMv(t *testing.T, binRoot string) {
	t.Helper()
	const interruptingMv = `#!/usr/bin/env bash
set -euo pipefail

state_file="${KENT_PROTOBUF_TEST_MV_STATE:?}"
call_count=0
if [[ -f "$state_file" ]]; then
	read -r call_count < "$state_file"
fi
call_count=$((call_count + 1))
echo "$call_count" > "$state_file"

if [[ "$call_count" -eq 4 ]]; then
	kill -TERM "$PPID"
	sleep 1
	exit 143
fi

exec /bin/mv "$@"
`
	writeFixtureFile(t, binRoot, "mv", []byte(interruptingMv))
	if err := os.Chmod(filepath.Join(binRoot, "mv"), 0o755); err != nil {
		t.Fatalf("make interrupting mv executable: %v", err)
	}
}

func copyProtobufGenerationInputs(t *testing.T, sourceRoot string, destinationRoot string) {
	t.Helper()
	for _, relativePath := range []string{
		"api/proto",
		"buf.yaml",
		"buf.gen.yaml",
		"buf.lock",
		"go.mod",
		"tools/protobuf/go.mod",
		"tools/protobuf/go.sum",
	} {
		sourcePath := filepath.Join(sourceRoot, relativePath)
		info, err := os.Stat(sourcePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("inspect Protobuf generation input %s: %v", relativePath, err)
		}
		if info.IsDir() {
			copyFixtureDirectory(t, sourcePath, filepath.Join(destinationRoot, relativePath))
			continue
		}
		copyFixtureFile(t, sourceRoot, destinationRoot, relativePath)
	}
}

func copyFixtureDirectory(t *testing.T, sourceRoot string, destinationRoot string) {
	t.Helper()
	if err := filepath.WalkDir(sourceRoot, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		destinationPath := filepath.Join(destinationRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destinationPath, 0o755)
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		return os.WriteFile(destinationPath, content, 0o644)
	}); err != nil {
		t.Fatalf("copy Protobuf generation input directory %s: %v", sourceRoot, err)
	}
}

func copyFixtureFile(t *testing.T, sourceRoot string, destinationRoot string, relativePath string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(sourceRoot, relativePath))
	if err != nil {
		t.Fatalf("read fixture file %s: %v", relativePath, err)
	}
	writeFixtureFile(t, destinationRoot, relativePath, content)
}

func writeFixtureFile(t *testing.T, root string, relativePath string, content []byte) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory for %s: %v", relativePath, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture file %s: %v", relativePath, err)
	}
}

func readFixtureFile(t *testing.T, root string, relativePath string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, relativePath))
	if err != nil {
		t.Fatalf("read fixture file %s: %v", relativePath, err)
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
		t.Fatalf("resolve repository root %s: %v", root, err)
	}
	return root
}
