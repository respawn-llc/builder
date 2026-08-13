package scripts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

const (
	protovalidateGoModule           = "buf.build/go/protovalidate"
	protovalidateGoDescriptorModule = "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go"
	protovalidateNPMModule          = "@bufbuild/protovalidate"
	protovalidateBufModule          = "buf.build/bufbuild/protovalidate"
)

func TestProtovalidateStackUsesOneRelease(t *testing.T) {
	repositoryRoot := filepath.Clean("..")
	schemaVersion := readProtovalidateSchemaVersion(t, filepath.Join(repositoryRoot, "buf.yaml"))
	schemaCommit := readProtovalidateSchemaCommit(t, filepath.Join(repositoryRoot, "buf.lock"))
	wantRuntimeVersion := strings.TrimPrefix(schemaVersion, "v")

	for _, modulePath := range []string{
		filepath.Join(repositoryRoot, "go.mod"),
		filepath.Join(repositoryRoot, "tools", "protobuf", "go.mod"),
	} {
		if got := readGoModuleVersion(t, modulePath, protovalidateGoModule); got != schemaVersion {
			t.Errorf("%s pins %s %s, want %s", modulePath, protovalidateGoModule, got, schemaVersion)
		}
	}

	rootModulePath := filepath.Join(repositoryRoot, "go.mod")
	descriptorVersion := readGoModuleVersion(t, rootModulePath, protovalidateGoDescriptorModule)
	if descriptorCommit := generatedModuleCommit(t, descriptorVersion); !strings.HasPrefix(schemaCommit, descriptorCommit) {
		t.Errorf(
			"%s pins %s at schema commit %s, want %s",
			rootModulePath,
			protovalidateGoDescriptorModule,
			descriptorCommit,
			schemaCommit,
		)
	}

	for _, packagePath := range []string{
		filepath.Join(repositoryRoot, "tools", "protobuf", "package.json"),
		filepath.Join(repositoryRoot, "apps", "desktop", "packages", "server-api-contract", "package.json"),
	} {
		if got := readNPMDependencyVersion(t, packagePath, protovalidateNPMModule); got != wantRuntimeVersion {
			t.Errorf("%s pins %s %s, want %s", packagePath, protovalidateNPMModule, got, wantRuntimeVersion)
		}
	}
}

func readProtovalidateSchemaVersion(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var config struct {
		Dependencies []string `yaml:"deps"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	for _, dependency := range config.Dependencies {
		module, version, found := strings.Cut(dependency, ":")
		if found && module == protovalidateBufModule {
			return version
		}
	}
	t.Fatalf("%s does not pin %s", path, protovalidateBufModule)
	return ""
}

func readProtovalidateSchemaCommit(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lock struct {
		Dependencies []struct {
			Name   string `yaml:"name"`
			Commit string `yaml:"commit"`
		} `yaml:"deps"`
	}
	if err := yaml.Unmarshal(data, &lock); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	for _, dependency := range lock.Dependencies {
		if dependency.Name == protovalidateBufModule {
			if dependency.Commit == "" {
				t.Fatalf("%s has no commit for %s", path, protovalidateBufModule)
			}
			return dependency.Commit
		}
	}
	t.Fatalf("%s does not lock %s", path, protovalidateBufModule)
	return ""
}

func generatedModuleCommit(t *testing.T, version string) string {
	t.Helper()
	parts := strings.Split(version, "-")
	if len(parts) != 3 {
		t.Fatalf("generated module version %q is not a three-part pseudo-version", version)
	}
	commit, _, found := strings.Cut(parts[2], ".")
	if !found || commit == "" {
		t.Fatalf("generated module version %q has no commit revision", version)
	}
	return commit
}

func readGoModuleVersion(t *testing.T, path, dependency string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	module, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	for _, requirement := range module.Require {
		if requirement.Mod.Path == dependency {
			return requirement.Mod.Version
		}
	}
	t.Fatalf("%s does not pin %s", path, dependency)
	return ""
}

func readNPMDependencyVersion(t *testing.T, path, dependency string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	version, found := manifest.Dependencies[dependency]
	if !found {
		t.Fatalf("%s does not pin %s", path, dependency)
	}
	return version
}
