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

func TestProtobufCICheckRejectsNondeterministicGeneration(t *testing.T) {
	fixture := newProtobufCIFixture(t)
	if output, err := fixture.run(t, "KENT_PROTOBUF_TEST_NONDETERMINISTIC=1"); err == nil {
		t.Fatalf("Protobuf CI check accepted nondeterministic generation\n%s", output)
	}
}

func TestProtobufCICheckRejectsInvalidDescriptorMetadata(t *testing.T) {
	fixture := newProtobufCIFixture(t)

	output, err := fixture.run(t, "KENT_PROTOBUF_TEST_DESCRIPTOR_POLICY_FAILURE=1")
	if err == nil {
		t.Fatalf("Protobuf CI check accepted invalid descriptor metadata\n%s", output)
	}
}

func TestProtobufCICheckRequiresStrictProtocolVersionIncreaseWithSchemaChange(t *testing.T) {
	testCases := []struct {
		name           string
		currentVersion []byte
	}{
		{name: "same", currentVersion: []byte("{\"version\":\"1\"}\n")},
		{name: "downgrade", currentVersion: []byte("{\"version\":\"0\"}\n")},
		{name: "malformed base", currentVersion: []byte("{\"version\":\"2\"}\n")},
		{name: "malformed current", currentVersion: []byte("{")},
		{name: "non-numeric base", currentVersion: []byte("{\"version\":\"2\"}\n")},
		{name: "non-numeric current", currentVersion: []byte("{\"version\":\"next\"}\n")},
		{name: "non-canonical base", currentVersion: []byte("{\"version\":\"2\"}\n")},
		{name: "non-canonical current", currentVersion: []byte("{\"version\":\"02\"}\n")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newProtobufCIFixture(t)
			switch testCase.name {
			case "malformed base":
				fixture.setBaseVersion(t, []byte("{"))
			case "non-numeric base":
				fixture.setBaseVersion(t, []byte("{\"version\":\"old\"}\n"))
			case "non-canonical base":
				fixture.setBaseVersion(t, []byte("{\"version\":\"01\"}\n"))
			}
			fixture.addSchema(t)
			writeFixtureFile(
				t,
				fixture.repositoryRoot,
				"shared/protocol/version.json",
				testCase.currentVersion,
			)

			if output, err := fixture.run(t); err == nil {
				t.Fatalf("Protobuf CI check accepted %s protocol version\n%s", testCase.name, output)
			}
		})
	}
}

func TestProtobufCICheckAcceptsStrictProtocolVersionIncreaseWithSchemaChange(t *testing.T) {
	fixture := newProtobufCIFixture(t)
	fixture.addSchema(t)
	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"shared/protocol/version.json",
		[]byte("{\"version\":\"2\"}\n"),
	)

	if output, err := fixture.run(t); err != nil {
		t.Fatalf("Protobuf CI check rejected strict protocol version increase: %v\n%s", err, output)
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
	environment    []string
	baseRevision   string
}

func newProtobufCIFixture(t *testing.T) protobufCIFixture {
	t.Helper()

	generationFixture := newProtobufGenerationFixture(t)
	fixture := protobufCIFixture{
		repositoryRoot: generationFixture.repositoryRoot,
		environment:    generationFixture.environment,
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
	command.Env = append([]string(nil), fixture.environment...)
	command.Env = append(command.Env, "KENT_CI_BASE_REVISION="+fixture.baseRevision)
	command.Env = append(command.Env, extraEnvironment...)
	return command.CombinedOutput()
}

func (fixture *protobufCIFixture) setBaseVersion(t *testing.T, version []byte) {
	t.Helper()

	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"shared/protocol/version.json",
		version,
	)
	runFixtureCommand(t, fixture.repositoryRoot, "git", "add", "shared/protocol/version.json")
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
		"change base protocol version",
	)
	fixture.baseRevision = strings.TrimSpace(
		string(runFixtureCommand(t, fixture.repositoryRoot, "git", "rev-parse", "HEAD")),
	)
}

func (fixture protobufCIFixture) addSchema(t *testing.T) {
	t.Helper()

	writeFixtureFile(
		t,
		fixture.repositoryRoot,
		"api/proto/kent/api/shared/added.proto",
		[]byte("syntax = \"proto3\";\n"),
	)
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
