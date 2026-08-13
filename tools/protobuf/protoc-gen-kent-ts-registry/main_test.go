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

func TestGenerateDerivesAggregateExportsFromSchemaFileGraph(t *testing.T) {
	foundation := schemaFile("kent/api/shared/foundation.proto", "kent.api.shared")
	project := schemaFile("kent/api/project/project.proto", "kent.api.project")

	foundationOnly := generateRegistry(t, foundation)
	if !strings.Contains(
		foundationOnly,
		`export * as schema_kent_api_shared_foundation from "../kent/api/shared/foundation_pb.js";`,
	) {
		t.Fatal("aggregate omitted the foundation schema export")
	}
	if strings.Contains(foundationOnly, "project_pb.js") {
		t.Fatal("aggregate exported a schema domain absent from the generation graph")
	}

	withProject := generateRegistry(t, foundation, project)
	if !strings.Contains(
		withProject,
		`import { file_kent_api_project_project } from "../kent/api/project/project_pb.js";`,
	) {
		t.Fatal("adding a schema domain did not add its generated descriptor import")
	}
	if !strings.Contains(
		withProject,
		`export * as schema_kent_api_project_project from "../kent/api/project/project_pb.js";`,
	) {
		t.Fatal("adding a schema domain did not add its generated export")
	}
	if !strings.Contains(withProject, "  file_kent_api_project_project,") {
		t.Fatal("adding a schema domain did not add its descriptor to the aggregate")
	}

	withoutFoundation := generateRegistry(t, project)
	if strings.Contains(withoutFoundation, "foundation_pb.js") {
		t.Fatal("removing a schema domain did not remove its generated import/export")
	}
	if strings.Contains(withoutFoundation, "file_kent_api_shared_foundation,") {
		t.Fatal("removing a schema domain did not remove its descriptor from the aggregate")
	}
}

func TestGenerateSeparatesPublicDomainAndTestFixtureExports(t *testing.T) {
	foundation := schemaFile("kent/api/shared/foundation.proto", "kent.api.shared")
	fixture := schemaFile("fixture/method_policy_fixture.proto", "fixture")

	production := generateRegistryFile(t, "registry/registry.ts", foundation, fixture)
	if strings.Contains(production, "fixture/method_policy_fixture_pb.js") {
		t.Fatal("public registry exported a test fixture schema")
	}
	if !strings.Contains(production, "kent/api/shared/foundation_pb.js") {
		t.Fatal("public registry omitted a Kent domain schema")
	}

	testOnly := generateRegistryFile(t, "test-registry/registry.ts", foundation, fixture)
	if strings.Contains(testOnly, "kent/api/shared/foundation_pb.js") {
		t.Fatal("test-only registry exported a Kent domain schema")
	}
	if !strings.Contains(testOnly, "fixture/method_policy_fixture_pb.js") {
		t.Fatal("test-only registry omitted a fixture schema")
	}
}

func TestGenerateRejectsUnclassifiedGeneratedSchema(t *testing.T) {
	unknown := schemaFile("other/unknown.proto", "other")

	request := generatorRequest(unknown)
	plugin, err := (protogen.Options{}).New(request)
	if err != nil {
		t.Fatalf("create generator plugin: %v", err)
	}
	if err := generate(plugin); err == nil {
		t.Fatal("generator accepted a schema outside the Kent domain and fixture roots")
	}
}

func TestGenerateProvidesDescriptorAggregate(t *testing.T) {
	foundation := schemaFile("kent/api/shared/foundation.proto", "kent.api.shared")

	registry := generateRegistry(t, foundation)
	if !strings.Contains(registry, "export const fileDescriptors: readonly GenFile[]") {
		t.Fatal("aggregate omitted the descriptor collection")
	}
	if !strings.Contains(registry, "file_kent_api_shared_foundation,") {
		t.Fatal("aggregate descriptor collection omitted the generated file")
	}
	if !strings.Contains(registry, "export const descriptorPaths: readonly string[]") {
		t.Fatal("aggregate omitted schema-path reporting")
	}
}

func TestGenerateOrdersAggregateBySchemaPath(t *testing.T) {
	project := schemaFile("kent/api/project/project.proto", "kent.api.project")
	foundation := schemaFile("kent/api/shared/foundation.proto", "kent.api.shared")

	forward := generateRegistry(t, project, foundation)
	reversed := generateRegistry(t, foundation, project)
	if forward != reversed {
		t.Fatal("aggregate output depends on input file order")
	}
}

func generateRegistry(t *testing.T, files ...*descriptorpb.FileDescriptorProto) string {
	return generateRegistryFile(t, "registry/registry.ts", files...)
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

func schemaFile(name string, protobufPackage string) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String(name),
		Package: proto.String(protobufPackage),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("core/shared/protoapi/gen/" + pathWithoutExtension(name)),
		},
	}
}

func pathWithoutExtension(name string) string {
	return strings.TrimSuffix(name, ".proto")
}
