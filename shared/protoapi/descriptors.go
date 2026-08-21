package protoapi

import (
	_ "embed"
	"fmt"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

//go:embed gen/kent_api.binpb
var descriptorSetBytes []byte

var descriptorOperations = sync.OnceValues(loadDescriptorOperations)

func Operations() ([]Operation, error) {
	operations, err := descriptorOperations()
	return append([]Operation(nil), operations...), err
}

func loadDescriptorOperations() ([]Operation, error) {
	var descriptorSet descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(descriptorSetBytes, &descriptorSet); err != nil {
		return nil, fmt.Errorf("decode generated descriptor set: %w", err)
	}
	files, err := protodesc.NewFiles(&descriptorSet)
	if err != nil {
		return nil, fmt.Errorf("index generated descriptor set: %w", err)
	}
	var operations []Operation
	var loadErr error
	files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package().Parent() != "kent.api" {
			return true
		}
		services := file.Services()
		for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
			methods := services.Get(serviceIndex).Methods()
			for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
				operation, err := OperationFromDescriptor(methods.Get(methodIndex))
				if err != nil {
					loadErr = err
					return false
				}
				operations = append(operations, operation)
			}
		}
		return true
	})
	if loadErr != nil {
		return nil, loadErr
	}
	sort.Slice(operations, func(left, right int) bool {
		return operations[left].Name < operations[right].Name
	})
	for index := 1; index < len(operations); index++ {
		if operations[index-1].Name == operations[index].Name {
			return nil, fmt.Errorf("duplicate operation name %q", operations[index].Name)
		}
	}
	return operations, nil
}
