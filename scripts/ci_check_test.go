package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtobufCICheckRejectsInvalidSchema(t *testing.T) {
	fixture := newProtobufCIFixture(t)

	output, err := fixture.run(t, "KENT_PROTOBUF_TEST_LINT_FAILURE=1")
	if err == nil {
		t.Fatalf("Protobuf CI check accepted invalid schema\n%s", output)
	}
}

func TestProtobufCICheckRejectsGeneratedTreeDriftIndependently(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(t *testing.T, repositoryRoot string)
	}{
		{
			name: "stale content",
			mutate: func(t *testing.T, repositoryRoot string) {
				writeFixtureFile(t, repositoryRoot, generatedGoRelativePath, []byte("stale\n"))
			},
		},
		{
			name: "extra generated file",
			mutate: func(t *testing.T, repositoryRoot string) {
				writeFixtureFile(
					t,
					repositoryRoot,
					"shared/protoapi/gen/extra.pb.go",
					[]byte("extra\n"),
				)
			},
		},
		{
			name: "deleted generated file",
			mutate: func(t *testing.T, repositoryRoot string) {
				if err := os.Remove(filepath.Join(repositoryRoot, generatedTypeScriptRelativePath)); err != nil {
					t.Fatalf("delete generated TypeScript fixture: %v", err)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newProtobufCIFixture(t)
			testCase.mutate(t, fixture.repositoryRoot)

			if output, err := fixture.run(t); err == nil {
				t.Fatalf("Protobuf CI check accepted %s\n%s", testCase.name, output)
			}
		})
	}
}

func TestProtobufCICheckRejectsInvalidDescriptorMetadata(t *testing.T) {
	fixture := newProtobufCIFixture(t)

	output, err := fixture.run(t, "KENT_PROTOBUF_TEST_DESCRIPTOR_POLICY_FAILURE=1")
	if err == nil {
		t.Fatalf("Protobuf CI check accepted invalid descriptor metadata\n%s", output)
	}
}

func TestProtobufCICheckRequiresProtocolVersionEditWithSchemaChange(t *testing.T) {
	fixture := newProtobufCIFixture(t)
	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"api/proto/kent/api/shared/added.proto",
		[]byte("syntax = \"proto3\";\n"),
	)

	if output, err := fixture.run(t); err == nil {
		t.Fatalf("Protobuf CI check accepted schema change without protocol version edit\n%s", output)
	}

	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"shared/protocol/version.json",
		[]byte("{\"version\":\"2\"}\n"),
	)
	if output, err := fixture.run(t); err != nil {
		t.Fatalf("Protobuf CI check rejected schema and protocol version edits: %v\n%s", err, output)
	}
}

func TestProtobufCICheckRequiresProtocolVersionEditWithTrackedSchemaChange(t *testing.T) {
	fixture := newProtobufCIFixture(t)
	const schemaPath = "api/proto/fixture/method_policy_fixture.proto"
	schema := readFixtureFile(t, fixture.repositoryRoot, schemaPath)
	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		schemaPath,
		append(schema, []byte("\n// tracked schema change\n")...),
	)

	if output, err := fixture.run(t); err == nil {
		t.Fatalf("Protobuf CI check accepted tracked schema change without protocol version edit\n%s", output)
	}

	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"shared/protocol/version.json",
		[]byte("{\"version\":\"2\"}\n"),
	)
	if output, err := fixture.run(t); err != nil {
		t.Fatalf("Protobuf CI check rejected tracked schema and protocol version edits: %v\n%s", err, output)
	}
}

func TestProtobufCICheckAllowsUnchangedSchemaWithoutVersionEdit(t *testing.T) {
	fixture := newProtobufCIFixture(t)

	if output, err := fixture.run(t); err != nil {
		t.Fatalf("Protobuf CI check rejected unchanged schema: %v\n%s", err, output)
	}
}

type protobufCIFixture struct {
	repositoryRoot string
	expectedRoot   string
	fakeBinRoot    string
	baseRevision   string
}

func newProtobufCIFixture(t *testing.T) protobufCIFixture {
	t.Helper()

	generationFixture := newProtobufGenerationFixture(t)
	fixture := protobufCIFixture{
		repositoryRoot: generationFixture.repositoryRoot,
		expectedRoot:   generationFixture.expectedRoot,
		fakeBinRoot:    generationFixture.fakeBinRoot,
	}

	sourceRoot := repositoryRoot(t)
	copyFixtureFile(t, sourceRoot, fixture.repositoryRoot, "scripts/ci-check.sh")
	copyFixtureFile(t, sourceRoot, fixture.repositoryRoot, "scripts/check-protobuf-schema-version.sh")
	if err := os.Chmod(filepath.Join(fixture.repositoryRoot, "scripts", "ci-check.sh"), 0o755); err != nil {
		t.Fatalf("make CI entry point executable: %v", err)
	}
	if err := os.Chmod(
		filepath.Join(fixture.repositoryRoot, "scripts", "check-protobuf-schema-version.sh"),
		0o755,
	); err != nil {
		t.Fatalf("make schema/version check executable: %v", err)
	}
	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"shared/protocol/version.json",
		[]byte("{\"version\":\"1\"}\n"),
	)

	runFixtureCommand(t, fixture.repositoryRoot, "git", "init", "-q")
	runFixtureCommand(t, fixture.repositoryRoot, "git", "add", ".")
	runFixtureCommand(
		t,
		fixture.repositoryRoot,
		"git",
		"-c",
		"user.name=Kent Test",
		"-c",
		"user.email=kent-test@example.invalid",
		"commit",
		"-qm",
		"fixture",
	)
	fixture.baseRevision = strings.TrimSpace(
		string(runFixtureCommand(t, fixture.repositoryRoot, "git", "rev-parse", "HEAD")),
	)

	return fixture
}

func (fixture protobufCIFixture) run(t *testing.T, extraEnvironment ...string) ([]byte, error) {
	t.Helper()

	command := exec.Command(
		filepath.Join(fixture.repositoryRoot, "scripts", "ci-check.sh"),
		"protobuf",
	)
	command.Dir = fixture.repositoryRoot
	command.Env = append(
		os.Environ(),
		append([]string{
			"PATH=" + fixture.fakeBinRoot + string(os.PathListSeparator) + os.Getenv("PATH"),
			"KENT_CI_BASE_REVISION=" + fixture.baseRevision,
			"KENT_PROTOBUF_TEST_EXPECTED_ROOT=" + fixture.expectedRoot,
		}, extraEnvironment...)...,
	)
	return command.CombinedOutput()
}

func runFixtureCommand(t *testing.T, directory string, name string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, arguments, err, output)
	}
	return output
}
