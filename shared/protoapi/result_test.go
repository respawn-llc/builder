package protoapi_test

import (
	"bytes"
	"reflect"
	"testing"

	"core/shared/protoapi"
	fixturepb "core/shared/protoapi/gen/fixture"
	newerfixturepb "core/shared/protoapi/gen/fixture/newer"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestOperationResultClassifiesSuccess(t *testing.T) {
	result := &fixturepb.CreateResult{
		Outcome: &fixturepb.CreateResult_Success{
			Success: &fixturepb.CreateSuccess{ResourceId: "resource-1"},
		},
	}

	classified, err := protoapi.ClassifyOperationResult(result)
	if err != nil {
		t.Fatalf("classify result: %v", err)
	}
	if classified.Kind != protoapi.OperationOutcomeSuccess {
		t.Fatalf("outcome kind = %v, want success", classified.Kind)
	}
	if classified.Success == nil || classified.Failure != nil {
		t.Fatalf("classified result = %+v", classified)
	}
}

func TestOperationResultClassifiesKnownTypedFailures(t *testing.T) {
	tests := []struct {
		name       string
		failure    *fixturepb.CreateError
		code       string
		detailType any
	}{
		{
			name: "invalid input",
			failure: &fixturepb.CreateError{
				Code: "invalid_input",
				Detail: &fixturepb.CreateError_InvalidInput{
					InvalidInput: &fixturepb.InvalidInputDetails{Field: "name"},
				},
			},
			code:       "invalid_input",
			detailType: (*fixturepb.InvalidInputDetails)(nil),
		},
		{
			name: "resource conflict",
			failure: &fixturepb.CreateError{
				Code: "resource_conflict",
				Detail: &fixturepb.CreateError_ResourceConflict{
					ResourceConflict: &fixturepb.ResourceConflictDetails{ResourceId: "resource-1"},
				},
			},
			code:       "resource_conflict",
			detailType: (*fixturepb.ResourceConflictDetails)(nil),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := &fixturepb.CreateResult{
				Outcome: &fixturepb.CreateResult_Error{Error: test.failure},
			}
			classified, err := protoapi.ClassifyOperationResult(result)
			if err != nil {
				t.Fatalf("classify result: %v", err)
			}
			if classified.Kind != protoapi.OperationOutcomeKnownFailure {
				t.Fatalf("outcome kind = %v, want known failure", classified.Kind)
			}
			if classified.Success != nil || classified.Failure == nil {
				t.Fatalf("classified result = %+v", classified)
			}
			if classified.Failure.Code != test.code {
				t.Fatalf("failure code = %q", classified.Failure.Code)
			}
			if got, want := classified.Failure.Detail.Interface(), test.detailType; reflect.TypeOf(got) != reflect.TypeOf(want) {
				t.Fatalf("failure detail = %T, want %T", got, want)
			}
		})
	}
}

func TestOperationResultClassifiesUnknownCodeAsGenericFailure(t *testing.T) {
	result := &fixturepb.CreateResult{
		Outcome: &fixturepb.CreateResult_Error{
			Error: &fixturepb.CreateError{
				Code: "future_failure",
				Detail: &fixturepb.CreateError_InvalidInput{
					InvalidInput: &fixturepb.InvalidInputDetails{Field: "name"},
				},
			},
		},
	}

	classified, err := protoapi.ClassifyOperationResult(result)
	if err != nil {
		t.Fatalf("classify result: %v", err)
	}
	if classified.Kind != protoapi.OperationOutcomeGenericFailure {
		t.Fatalf("outcome kind = %v, want generic failure", classified.Kind)
	}
	if classified.Failure == nil || classified.Failure.Code != "future_failure" {
		t.Fatalf("classified result = %+v", classified)
	}
	if _, ok := classified.Failure.Detail.Interface().(*fixturepb.InvalidInputDetails); !ok {
		t.Fatalf("generic failure detail = %T", classified.Failure.Detail.Interface())
	}
}

func TestOperationResultPreservesFutureUnknownErrorDetail(t *testing.T) {
	future := &newerfixturepb.FutureCreateResult{
		Outcome: &newerfixturepb.FutureCreateResult_Error{
			Error: &newerfixturepb.FutureCreateError{
				Code: "future_failure",
				Detail: &newerfixturepb.FutureCreateError_FutureConflict{
					FutureConflict: &newerfixturepb.FutureConflictDetails{
						ResourceId: "resource-1",
						Generation: 7,
					},
				},
			},
		},
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(future)
	if err != nil {
		t.Fatalf("marshal future result: %v", err)
	}

	current := new(fixturepb.CreateResult)
	if err := proto.Unmarshal(encoded, current); err != nil {
		t.Fatalf("unmarshal future result with current descriptor: %v", err)
	}
	classified, err := protoapi.ClassifyOperationResult(current)
	if err != nil {
		t.Fatalf("classify future result: %v", err)
	}
	if classified.Kind != protoapi.OperationOutcomeGenericFailure {
		t.Fatalf("outcome kind = %v, want generic failure", classified.Kind)
	}
	if classified.Failure == nil || classified.Failure.Code != "future_failure" {
		t.Fatalf("classified result = %+v", classified)
	}
	if classified.Failure.Detail != nil {
		t.Fatalf("unknown future detail was interpreted as %s", classified.Failure.Detail.Descriptor().FullName())
	}

	reencoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(current)
	if err != nil {
		t.Fatalf("re-marshal current result: %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatalf("re-encoded bytes = %x, want preserved future bytes %x", reencoded, encoded)
	}
}

func TestOperationResultRejectsMalformedOutcomesAndErrors(t *testing.T) {
	tests := []struct {
		name   string
		result *fixturepb.CreateResult
	}{
		{name: "missing outcome", result: &fixturepb.CreateResult{}},
		{
			name: "empty code",
			result: &fixturepb.CreateResult{
				Outcome: &fixturepb.CreateResult_Error{
					Error: &fixturepb.CreateError{
						Detail: &fixturepb.CreateError_InvalidInput{
							InvalidInput: &fixturepb.InvalidInputDetails{Field: "name"},
						},
					},
				},
			},
		},
		{
			name: "known code missing detail",
			result: &fixturepb.CreateResult{
				Outcome: &fixturepb.CreateResult_Error{
					Error: &fixturepb.CreateError{Code: "invalid_input"},
				},
			},
		},
		{
			name: "known code wrong detail",
			result: &fixturepb.CreateResult{
				Outcome: &fixturepb.CreateResult_Error{
					Error: &fixturepb.CreateError{
						Code: "invalid_input",
						Detail: &fixturepb.CreateError_ResourceConflict{
							ResourceConflict: &fixturepb.ResourceConflictDetails{ResourceId: "resource-1"},
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := protoapi.ClassifyOperationResult(test.result); err == nil {
				t.Fatal("malformed operation result unexpectedly classified")
			}
		})
	}
}

func TestSubscriptionStartResultIsSeparateFromPostAcknowledgementMessages(t *testing.T) {
	start := &fixturepb.WatchStartResult{
		Outcome: &fixturepb.WatchStartResult_Success{
			Success: &fixturepb.WatchAcknowledgement{},
		},
	}
	classified, err := protoapi.ClassifyOperationResult(start)
	if err != nil {
		t.Fatalf("classify subscription start: %v", err)
	}
	if classified.Kind != protoapi.OperationOutcomeSuccess {
		t.Fatalf("subscription start kind = %v", classified.Kind)
	}

	event := (&fixturepb.WatchEvent{Value: "event-1"}).ProtoReflect().Descriptor()
	completion := (&fixturepb.WatchCompletion{EventCount: 1}).ProtoReflect().Descriptor()
	if protoapi.IsOperationResultDescriptor(event) {
		t.Fatal("post-ack event was classified as an operation result")
	}
	if protoapi.IsOperationResultDescriptor(completion) {
		t.Fatal("post-ack completion was classified as an operation result")
	}

	service := fixturepb.File_fixture_method_policy_fixture_proto.
		Services().
		ByName(protoreflect.Name("NamingService"))
	startMethod := service.Methods().ByName(protoreflect.Name("Watch"))
	eventMethod := service.Methods().ByName(protoreflect.Name("WatchEvent"))
	completionMethod := service.Methods().ByName(protoreflect.Name("WatchComplete"))
	if startMethod.Output() != start.ProtoReflect().Descriptor() {
		t.Fatalf("subscription start output = %s", startMethod.Output().FullName())
	}
	if eventMethod.Input() != event {
		t.Fatalf("subscription event input = %s", eventMethod.Input().FullName())
	}
	if completionMethod.Input() != completion {
		t.Fatalf("subscription completion input = %s", completionMethod.Input().FullName())
	}
}

func TestTransportFailureIsDistinctFromOperationFailureWithoutStringParsing(t *testing.T) {
	transportFailure := &sharedpb.TransportFailure{
		Code: sharedpb.TransportFailureCode_TRANSPORT_FAILURE_CODE_INVALID_PAYLOAD,
	}
	if protoapi.IsOperationResultDescriptor(transportFailure.ProtoReflect().Descriptor()) {
		t.Fatal("transport failure was classified as an operation result")
	}
	if _, err := protoapi.ClassifyOperationResult(transportFailure); err == nil {
		t.Fatal("transport failure unexpectedly classified as an operation result")
	}
}

func TestOperationDescriptorsDeclareResultConventions(t *testing.T) {
	service := fixturepb.File_fixture_method_policy_fixture_proto.
		Services().
		ByName(protoreflect.Name("NamingService"))
	for _, methodName := range []protoreflect.Name{"HTTP2Server", "APIStatus", "Watch"} {
		method := service.Methods().ByName(methodName)
		if method == nil {
			t.Fatalf("method %s not found", methodName)
		}
		if !protoapi.IsOperationResultDescriptor(method.Output()) {
			t.Fatalf("%s output %s does not declare the result convention", methodName, method.Output().FullName())
		}
	}
}
