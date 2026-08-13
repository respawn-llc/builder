package main

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func TestGenerateDerivesAggregateImportsFromSchemaFileGraph(t *testing.T) {
	foundation := schemaFile(
		"kent/api/shared/foundation.proto",
		"kent.api.shared",
		"core/shared/protoapi/gen/kent/api/shared;sharedpb",
	)
	project := schemaFile(
		"kent/api/project/project.proto",
		"kent.api.project",
		"core/shared/protoapi/gen/kent/api/project;projectpb",
	)

	foundationOnly := generateRegistry(t, foundation)
	if !strings.Contains(foundationOnly, `"core/shared/protoapi/gen/kent/api/shared"`) {
		t.Fatal("aggregate omitted the foundation domain import")
	}
	if strings.Contains(foundationOnly, `"core/shared/protoapi/gen/kent/api/project"`) {
		t.Fatal("aggregate imported a schema domain absent from the generation graph")
	}

	withProject := generateRegistry(t, foundation, project)
	if !strings.Contains(withProject, `"core/shared/protoapi/gen/kent/api/project"`) {
		t.Fatal("adding a schema domain did not add its generated import")
	}
	if !strings.Contains(withProject, `"kent/api/project/project.proto"`) {
		t.Fatal("adding a schema file did not add its descriptor path")
	}
	if !strings.Contains(withProject, "func Paths(yield func(string) bool)") {
		t.Fatal("aggregate omitted schema-path reporting")
	}

	withoutFoundation := generateRegistry(t, project)
	if strings.Contains(withoutFoundation, `"core/shared/protoapi/gen/kent/api/shared"`) {
		t.Fatal("removing a schema domain did not remove its generated import")
	}
	if strings.Contains(withoutFoundation, `"kent/api/shared/foundation.proto"`) {
		t.Fatal("removing a schema file did not remove its descriptor path")
	}
}

func TestGenerateSeparatesKentDomainAndFixtureRegistries(t *testing.T) {
	foundation := schemaFile(
		"kent/api/shared/foundation.proto",
		"kent.api.shared",
		"core/shared/protoapi/gen/kent/api/shared;sharedpb",
	)
	fixture := schemaFile(
		"fixture/method_policy_fixture.proto",
		"fixture",
		"core/shared/protoapi/gen/fixture;fixturepb",
	)

	production := generateRegistryFile(t, "registry/registry.pb.go", foundation, fixture)
	if strings.Contains(production, `"core/shared/protoapi/gen/fixture"`) {
		t.Fatal("production registry imported a test fixture package")
	}
	if strings.Contains(production, `"fixture/method_policy_fixture.proto"`) {
		t.Fatal("production registry included a test fixture descriptor")
	}
	if !strings.Contains(production, `"kent/api/shared/foundation.proto"`) {
		t.Fatal("production registry omitted a Kent domain descriptor")
	}

	testOnly := generateRegistryFile(t, "testregistry/registry.pb.go", foundation, fixture)
	if strings.Contains(testOnly, `"core/shared/protoapi/gen/kent/api/shared"`) {
		t.Fatal("test-only registry imported a Kent domain package")
	}
	if !strings.Contains(testOnly, `"fixture/method_policy_fixture.proto"`) {
		t.Fatal("test-only registry omitted a fixture descriptor")
	}
}

func TestGenerateRejectsUnclassifiedGeneratedSchema(t *testing.T) {
	unknown := schemaFile(
		"other/unknown.proto",
		"other",
		"core/shared/protoapi/gen/other;otherpb",
	)

	request := generatorRequest(unknown)
	plugin, err := (protogen.Options{}).New(request)
	if err != nil {
		t.Fatalf("create generator plugin: %v", err)
	}
	if err := generate(plugin); err == nil {
		t.Fatal("generator accepted a schema outside the Kent domain and fixture roots")
	}
}

func TestGenerateOrdersAggregateBySchemaPath(t *testing.T) {
	project := schemaFile(
		"kent/api/project/project.proto",
		"kent.api.project",
		"core/shared/protoapi/gen/kent/api/project;projectpb",
	)
	foundation := schemaFile(
		"kent/api/shared/foundation.proto",
		"kent.api.shared",
		"core/shared/protoapi/gen/kent/api/shared;sharedpb",
	)

	forward := generateRegistry(t, project, foundation)
	reversed := generateRegistry(t, foundation, project)
	if forward != reversed {
		t.Fatal("aggregate output depends on input file order")
	}
}

func generateRegistry(t *testing.T, files ...*descriptorpb.FileDescriptorProto) string {
	return generateRegistryFile(t, "registry/registry.pb.go", files...)
}

func generateRegistryFile(
	t *testing.T,
	outputName string,
	files ...*descriptorpb.FileDescriptorProto,
) string {
	t.Helper()
	request := generatorRequest(files...)
	plugin, err := (protogen.Options{}).New(request)
	if err != nil {
		t.Fatalf("create generator plugin: %v", err)
	}
	if err := generate(plugin); err != nil {
		t.Fatalf("generate aggregate: %v", err)
	}
	response := plugin.Response()
	if response.GetError() != "" {
		t.Fatalf("generator response: %s", response.GetError())
	}
	for _, file := range response.File {
		if file.GetName() == outputName {
			return file.GetContent()
		}
	}
	t.Fatalf("generator did not emit %s", outputName)
	return ""
}

func generatorRequest(files ...*descriptorpb.FileDescriptorProto) *pluginpb.CodeGeneratorRequest {
	fileNames := make([]string, 0, len(files))
	for _, file := range files {
		fileNames = append(fileNames, file.GetName())
	}
	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: slices.Clone(fileNames),
		ProtoFile:      slices.Clone(files),
	}
}

func schemaFile(
	name string,
	protobufPackage string,
	goPackage string,
) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String(name),
		Package: proto.String(protobufPackage),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String(goPackage)},
	}
}
