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
	t.Helper()
	fileNames := make([]string, 0, len(files))
	for _, file := range files {
		fileNames = append(fileNames, file.GetName())
	}
	request := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: slices.Clone(fileNames),
		ProtoFile:      slices.Clone(files),
	}
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
		if file.GetName() == "registry/registry.pb.go" {
			return file.GetContent()
		}
	}
	t.Fatal("generator did not emit the aggregate registry")
	return ""
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
