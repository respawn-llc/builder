package migrationcheck

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"core/shared/protoapi"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	runpromptpb "core/shared/protoapi/gen/kent/api/run_prompt"
	runtimepb "core/shared/protoapi/gen/kent/api/runtime"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	transcriptpb "core/shared/protoapi/gen/kent/api/transcript"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestBoundedMigrationCoverageAccountsForActualTarget(t *testing.T) {
	if err := CheckBoundedMigrationCoverage(actualTargetCoverage(t)); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedMigrationCoverageRejectsPredecessorMutation(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.Report.Predecessors = coverage.Report.Predecessors[1:]
		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoveragePredecessorSet)
	})
	t.Run("added", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		added := coverage.Report.Predecessors[0]
		added.Identity = typeIdentity("fixture", "AddedIdentity")
		coverage.Report.Predecessors = append(coverage.Report.Predecessors, added)
		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoveragePredecessorSet)
	})
	t.Run("substituted", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.Report.Predecessors[0].Identity = typeIdentity("fixture", "SubstitutedIdentity")
		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoveragePredecessorSet)
	})
	t.Run("duplicated", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.Report.Predecessors = append(
			coverage.Report.Predecessors,
			coverage.Report.Predecessors[0],
		)
		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoveragePredecessorDuplicate)
	})
}

func TestBoundedMigrationCoverageRejectsRouteMetadataMutation(t *testing.T) {
	coverage := actualTargetCoverage(t)
	coverage.Report.Routes[0].Auth = "mutated"

	assertCoverageIssue(
		t,
		CheckBoundedMigrationCoverage(coverage),
		IssueCoverageRouteMetadata,
	)
}

func TestBoundedMigrationCoverageRejectsMissingDescriptorOperation(t *testing.T) {
	coverage := actualTargetCoverage(t)
	for index, operation := range coverage.Operations {
		if operation.Descriptor.ParentFile().Package() == "fixture" {
			continue
		}
		coverage.Operations = append(
			coverage.Operations[:index],
			coverage.Operations[index+1:]...,
		)
		break
	}

	assertCoverageIssue(
		t,
		CheckBoundedMigrationCoverage(coverage),
		IssueCoverageRouteAssociation,
	)
}

func TestBoundedMigrationCoverageRejectsMissingFocusedFixture(t *testing.T) {
	coverage := actualTargetCoverage(t)
	coverage.FocusedFixtures = coverage.FocusedFixtures[1:]

	assertCoverageIssue(
		t,
		CheckBoundedMigrationCoverage(coverage),
		IssueCoverageFocusedFixture,
	)
}

func TestBoundedMigrationCoverageRejectsFailingFocusedFixture(t *testing.T) {
	coverage := actualTargetCoverage(t)
	coverage.FocusedFixtures[0].Check = func() error {
		return errors.New("mutated focused behavior")
	}

	assertCoverageIssue(
		t,
		CheckBoundedMigrationCoverage(coverage),
		IssueCoverageFocusedFixture,
	)
}

func TestBoundedMigrationCoverageRejectsScalarAndValidatorMutation(t *testing.T) {
	t.Run("scalar constant", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		for index := range coverage.Classification.Scalars {
			if coverage.Classification.Scalars[index].Kind != ScalarClosedStringEnum {
				continue
			}
			coverage.Classification.Scalars[index].EnumMembers =
				coverage.Classification.Scalars[index].EnumMembers[1:]
			assertCoverageIssue(
				t,
				CheckBoundedMigrationCoverage(coverage),
				IssueCoverageDeclaration,
			)
			return
		}
		t.Fatal("execution-target classification contains no closed enum")
	})

	t.Run("validator fingerprint", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.Classification.Validators[0].Fingerprint = "mutated"
		assertCoverageIssue(
			t,
			CheckBoundedMigrationCoverage(coverage),
			IssueCoverageDeclaration,
		)
	})
}

func TestBoundedMigrationCoverageRejectsOrdinaryWireMutation(t *testing.T) {
	coverage := actualTargetCoverage(t)
	operationIndex := -1
	for index, operation := range coverage.Operations {
		if operation.LegacyWireName != nil &&
			*operation.LegacyWireName == "workflow.task.search" {
			operationIndex = index
			break
		}
	}
	if operationIndex < 0 {
		t.Fatal("workflow.task.search descriptor operation not found")
	}
	operation := coverage.Operations[operationIndex]
	operation.Descriptor = descriptorMethodWithRemovedInputField(
		t,
		operation.Descriptor,
		"query",
	)
	coverage.Operations[operationIndex] = operation

	assertCoverageIssue(
		t,
		CheckBoundedMigrationCoverage(coverage),
		IssueCoverageWireShape,
	)
}

func TestBoundedMigrationCoverageRejectsScalarWidthAndPresenceMutations(t *testing.T) {
	t.Run("scalar width", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		operationIndex := operationIndexByLegacyName(t, coverage, "workflow.task.search")
		operation := coverage.Operations[operationIndex]
		operation.Descriptor = descriptorMethodWithMutatedInputField(
			t,
			operation.Descriptor,
			"page_size",
			func(field *descriptorpb.FieldDescriptorProto) {
				field.Type = descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum()
			},
		)
		coverage.Operations[operationIndex] = operation

		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoverageWireShape)
	})
	t.Run("presence", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		operationIndex := operationIndexByLegacyName(t, coverage, "workflow.task.search")
		operation := coverage.Operations[operationIndex]
		operation.Descriptor = descriptorMethodWithMutatedInputField(
			t,
			operation.Descriptor,
			"query",
			func(field *descriptorpb.FieldDescriptorProto) {
				field.Proto3Optional = proto.Bool(true)
				field.OneofIndex = proto.Int32(int32(operation.Descriptor.Input().Oneofs().Len()))
			},
			func(message *descriptorpb.DescriptorProto) {
				message.OneofDecl = append(message.OneofDecl, &descriptorpb.OneofDescriptorProto{
					Name: proto.String("_query"),
				})
			},
		)
		coverage.Operations[operationIndex] = operation

		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoverageWireShape)
	})
}

func TestBoundedMigrationCoverageRejectsReviewedFingerprintMutations(t *testing.T) {
	t.Run("exceptional descriptor", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.WireExceptions[0].DescriptorFingerprint = "mutated"
		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoverageWireException)
	})
	t.Run("closed enum descriptor set", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.Classification.Scalars[0].EnumMembers = nil
		assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), IssueCoverageDeclaration)
	})
}

func TestBoundedMigrationCoverageRejectsExceptionalCoverageMutation(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.WireExceptions = coverage.WireExceptions[1:]
		assertCoverageIssue(
			t,
			CheckBoundedMigrationCoverage(coverage),
			IssueCoverageWireShape,
		)
	})
	t.Run("wrong descriptor association", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.WireExceptions[0].Message = "kent.api.onboarding.FinalizeRequest"
		assertCoverageIssue(
			t,
			CheckBoundedMigrationCoverage(coverage),
			IssueCoverageWireException,
		)
	})
	t.Run("wrong focused fingerprint", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.WireExceptions[0].DescriptorFingerprint = "mutated"
		assertCoverageIssue(
			t,
			CheckBoundedMigrationCoverage(coverage),
			IssueCoverageWireException,
		)
	})
	t.Run("removed field rename", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.FieldRenames = coverage.FieldRenames[1:]
		assertCoverageIssue(
			t,
			CheckBoundedMigrationCoverage(coverage),
			IssueCoverageWireShape,
		)
	})
	t.Run("substituted field rename", func(t *testing.T) {
		coverage := actualTargetCoverage(t)
		coverage.FieldRenames[0].DescriptorField = "oauth_state"
		assertCoverageIssue(
			t,
			CheckBoundedMigrationCoverage(coverage),
			IssueCoverageWireShape,
		)
	})
}

func TestBoundedMigrationCoverageRejectsProjectedFieldAuthoredInDescriptor(t *testing.T) {
	coverage := actualTargetCoverage(t)
	projectedField := reflect.TypeFor[struct {
		ClientRequestID string
	}]().Field(0)
	if !isProjectedIdentity(fieldIdentity(
		"core/shared/serverapi",
		"RuntimeSubmitUserTurnRequest",
		projectedField.Name,
	)) {
		t.Fatal("test mutation is not a locked projected identity")
	}

	operationIndex := -1
	for index, operation := range coverage.Operations {
		if operation.LegacyWireName != nil &&
			*operation.LegacyWireName == "runtime.submitUserTurn" {
			operationIndex = index
			break
		}
	}
	if operationIndex < 0 {
		t.Fatal("runtime.submitUserTurn descriptor operation not found")
	}
	operation := coverage.Operations[operationIndex]
	operation.Descriptor = descriptorMethodWithAddedInputStringField(
		t,
		operation.Descriptor,
		"client_request_id",
	)
	coverage.Operations[operationIndex] = operation

	assertCoverageIssue(
		t,
		CheckBoundedMigrationCoverage(coverage),
		IssueCoverageProjectedWireFact,
	)
}

func TestBoundedMigrationCoverageRejectsRouteWireRootMutations(t *testing.T) {
	tests := []struct {
		name       string
		legacyName string
		root       descriptorMethodRoot
		field      protoreflect.Name
		issue      CoverageIssueCode
	}{
		{name: "request", legacyName: "workflow.task.search", root: descriptorMethodInput, field: "query", issue: IssueCoverageWireShape},
		{name: "response", legacyName: "workflow.task.search", root: descriptorMethodOutput, field: "success", issue: IssueCoverageWireShape},
		{name: "event", legacyName: "run.prompt.progress", root: descriptorMethodInput, field: "session_started", issue: IssueCoverageWireException},
		{name: "completion", legacyName: "worktree.setup.complete", root: descriptorMethodInput, field: "diagnostic", issue: IssueCoverageWireException},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coverage := actualTargetCoverage(t)
			index := operationIndexByLegacyName(t, coverage, test.legacyName)
			operation := coverage.Operations[index]
			if test.name == "completion" {
				mutated := descriptorMethodWithMutatedInputField(
					t,
					operation.Descriptor,
					test.field,
					func(field *descriptorpb.FieldDescriptorProto) {
						field.Type = descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum()
					},
				)
				subscribeIndex := operationIndexByLegacyName(t, coverage, "worktree.setup.subscribe")
				coverage.Operations[subscribeIndex].Completion = &protoapi.OperationAssociation{
					ActiveName: operation.ActiveName,
					Descriptor: mutated,
				}
			} else {
				operation.Descriptor = descriptorMethodWithRemovedRootField(
					t,
					operation.Descriptor,
					test.root,
					test.field,
				)
				coverage.Operations[index] = operation
			}
			assertCoverageIssue(t, CheckBoundedMigrationCoverage(coverage), test.issue)
		})
	}
}

func actualTargetCoverage(t *testing.T) BoundedMigrationCoverage {
	t.Helper()
	report, err := InspectExecutionTarget()
	if err != nil {
		t.Fatal(err)
	}
	classification, err := MergeDomainDeclarationSignoffs(ExecutionTargetDomainSignoffs())
	if err != nil {
		t.Fatal(err)
	}
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	wireExceptions := PopulateWireExceptionFingerprints(actualTargetWireExceptions(), operations)
	coverage := BoundedMigrationCoverage{
		Report:           report,
		Operations:       operations,
		Classification:   classification,
		WireExceptions:   wireExceptions,
		FieldRenames:     actualTargetFieldRenames(),
		ScalarMappings:   actualTargetScalarMappings(),
		PresenceMappings: actualTargetPresenceMappings(),
		FocusedFixtures: []FocusedProjectionFixture{
			{Name: FocusedKENT345StrictJSON, Check: checkKENT345StrictJSONFixture},
			{Name: FocusedKENT345CustomWire, Check: checkKENT345CustomWireFixture},
			{Name: FocusedKENT345Hydration, Check: checkKENT345HydrationFixture},
			{Name: FocusedKENT345Uniqueness, Check: checkKENT345UniquenessFixture},
			{Name: FocusedKENT345MixedValidators, Check: checkKENT345MixedValidatorsFixture},
			{Name: FocusedKENT554NegotiationValidation, Check: checkKENT554NegotiationValidationFixture},
			{Name: FocusedKENT554NegotiationConstants, Check: checkKENT554NegotiationConstantsFixture},
			{Name: FocusedKENT554RetainedCapabilityFacts, Check: checkKENT554RetainedCapabilityFactsFixture},
		},
	}
	coverage.ExceptionalFingerprint = fingerprintWireExceptions(wireExceptions)
	return coverage
}

func checkKENT345StrictJSONFixture() error {
	for _, message := range []proto.Message{
		&runpromptpb.Request{},
		&sessionlaunchpb.SessionPlanRequest{},
		&runtimepb.SubmitUserTurnRequest{},
	} {
		if message.ProtoReflect().Descriptor().Fields().ByName("client_request_id") != nil {
			return fmt.Errorf("%s retains client_request_id", message.ProtoReflect().Descriptor().FullName())
		}
		if err := protoapi.DecodeGeneratedMessage(nil, message); err == nil {
			return fmt.Errorf("%s accepted an invalid empty request", message.ProtoReflect().Descriptor().FullName())
		}
	}
	return nil
}

func checkKENT345CustomWireFixture() error {
	sessionID, err := runtimeids.ParseSessionID("55555555-5555-4555-8555-555555555555")
	if err != nil {
		return err
	}
	queueItemID, err := runtimeids.ParseQueueItemID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		return err
	}
	message := &runtimepb.LiveSteerSuccess{
		QueueItemId: queueItemID.String(),
		Text:        "continue",
	}
	encoded, err := protoapi.EncodeGeneratedMessage(message)
	if err != nil {
		return err
	}
	var decoded runtimepb.LiveSteerSuccess
	if err := protoapi.DecodeGeneratedMessage(encoded, &decoded); err != nil {
		return err
	}
	if decoded.QueueItemId != queueItemID.String() {
		return fmt.Errorf("Queue Item identity round-trip = %q", decoded.QueueItemId)
	}
	request := &runtimepb.SubmitUserTurnRequest{SessionId: sessionID.String()}
	if request.ProtoReflect().Descriptor().Fields().ByName("session_id") == nil {
		return errors.New("retained Session identity is absent")
	}
	return nil
}

func checkKENT345HydrationFixture() error {
	first := "first"
	second := "second"
	for _, message := range []*transcriptpb.QueuedMessageState{
		{
			QueueItemId: "11111111-1111-4111-8111-111111111111",
			Status:      transcriptpb.QueuedMessageStatus_QUEUED_MESSAGE_STATUS_ACCEPTED,
			Text:        &first,
		},
		{
			QueueItemId: "22222222-2222-4222-8222-222222222222",
			Status:      transcriptpb.QueuedMessageStatus_QUEUED_MESSAGE_STATUS_ACCEPTED,
			Text:        &second,
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(message); err != nil {
			return err
		}
	}
	hydration := (&transcriptpb.Hydration{}).ProtoReflect().Descriptor()
	if hydration.Fields().ByName("queued_messages") == nil {
		return errors.New("generated hydration omits queued messages")
	}
	return nil
}

func checkKENT345UniquenessFixture() error {
	if err := protoapi.ValidateGeneratedMessage(&transcriptpb.UserMessageFlushed{
		StepId: "44444444-4444-4444-8444-444444444444",
		QueueItemIds: []string{
			"11111111-1111-4111-8111-111111111111",
			"11111111-1111-4111-8111-111111111111",
		},
	}); err == nil {
		return errors.New("generated transcript event accepted duplicate Queue Item identity")
	}
	return nil
}

func checkKENT345MixedValidatorsFixture() error {
	if err := protoapi.ValidateGeneratedMessage(&sessionlaunchpb.SessionPlanRequest{
		Mode: sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_HEADLESS,
	}); err == nil {
		return errors.New("generated Session plan accepted missing retained intent")
	}
	if err := protoapi.ValidateGeneratedMessage(&runtimepb.SubmitUserTurnRequest{
		SessionId: "session-1",
		Input: &runtimepb.UserTurnInput{
			Input: &runtimepb.UserTurnInput_Text{Text: "continue"},
		},
	}); err != nil {
		return err
	}
	if err := protoapi.ValidateGeneratedMessage(&runtimepb.SubmitUserTurnRequest{
		Input: &runtimepb.UserTurnInput{
			Input: &runtimepb.UserTurnInput_Text{Text: "continue"},
		},
	}); err == nil {
		return errors.New("mixed validator lost retained Session identity requirement")
	}
	return nil
}

func checkKENT554NegotiationValidationFixture() error {
	if err := protoapi.ValidateGeneratedMessage(&connectionpb.HandshakeRequest{
		ProtocolVersion: protocol.Version,
	}); err != nil {
		return err
	}
	descriptor := (&connectionpb.HandshakeRequest{}).ProtoReflect().Descriptor()
	if descriptor.Fields().ByName("client_capabilities") != nil {
		return errors.New("generated handshake retains client capabilities")
	}
	legacy := protocol.HandshakeRequest{
		ProtocolVersion:    protocol.Version,
		ClientCapabilities: &protocol.ClientCapabilities{},
	}
	if err := legacy.Validate(); err == nil {
		return errors.New("legacy handshake no longer exercises capability negotiation")
	}
	return nil
}

func checkKENT554NegotiationConstantsFixture() error {
	if protocol.MethodCapabilityFactsGet != "capability.facts.get" {
		return fmt.Errorf("retained capability operation = %q", protocol.MethodCapabilityFactsGet)
	}
	return nil
}

func checkKENT554RetainedCapabilityFactsFixture() error {
	blank := " "
	if err := (serverapi.CapabilityFactsRequest{WorkspaceRoot: &blank}).Validate(); err == nil {
		return errors.New("retained capability facts accepted a blank workspace root")
	}
	generatedBlank := " "
	if err := protoapi.ValidateGeneratedMessage(&capabilitypb.GetFactsRequest{
		WorkspaceRoot:          &generatedBlank,
		ExplicitLlmProviderIds: []string{"openai", ""},
	}); err == nil {
		return errors.New("generated capability facts accepted an empty provider identity")
	}
	descriptor := (&capabilitypb.GetFactsRequest{}).ProtoReflect().Descriptor()
	if descriptor.Fields().ByName("explicit_llm_provider_ids") == nil {
		return errors.New("retained provider capability facts are absent")
	}
	if serverapi.ImportErrorItemKindSkill != "skill" {
		return fmt.Errorf("retained import capability constant = %q", serverapi.ImportErrorItemKindSkill)
	}
	return nil
}

func descriptorMethodWithRemovedInputField(
	t *testing.T,
	method protoreflect.MethodDescriptor,
	fieldName protoreflect.Name,
) protoreflect.MethodDescriptor {
	t.Helper()
	return mutateDescriptorMethodInput(t, method, func(message *descriptorpb.DescriptorProto) {
		for index, field := range message.Field {
			if field.GetName() != string(fieldName) {
				continue
			}
			message.Field = append(message.Field[:index], message.Field[index+1:]...)
			return
		}
		t.Fatalf("%s input field %s not found", method.FullName(), fieldName)
	})
}

type descriptorMethodRoot int

const (
	descriptorMethodInput descriptorMethodRoot = iota
	descriptorMethodOutput
)

func descriptorMethodWithRemovedRootField(
	t *testing.T,
	method protoreflect.MethodDescriptor,
	root descriptorMethodRoot,
	fieldName protoreflect.Name,
) protoreflect.MethodDescriptor {
	t.Helper()
	return mutateDescriptorMethodRoot(t, method, root, func(message *descriptorpb.DescriptorProto) {
		for index, field := range message.Field {
			if field.GetName() != string(fieldName) {
				continue
			}
			message.Field = append(message.Field[:index], message.Field[index+1:]...)
			return
		}
		t.Fatalf("%s field %s not found", method.FullName(), fieldName)
	})
}

func descriptorMethodWithAddedInputStringField(
	t *testing.T,
	method protoreflect.MethodDescriptor,
	fieldName protoreflect.Name,
) protoreflect.MethodDescriptor {
	t.Helper()
	return mutateDescriptorMethodInput(t, method, func(message *descriptorpb.DescriptorProto) {
		number := int32(1)
		for _, field := range message.Field {
			if field.GetNumber() >= number {
				number = field.GetNumber() + 1
			}
		}
		message.Field = append(message.Field, &descriptorpb.FieldDescriptorProto{
			Name:   proto.String(string(fieldName)),
			Number: proto.Int32(number),
			Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		})
	})
}

func descriptorMethodWithMutatedInputField(
	t *testing.T,
	method protoreflect.MethodDescriptor,
	fieldName protoreflect.Name,
	mutateField func(*descriptorpb.FieldDescriptorProto),
	mutateMessage ...func(*descriptorpb.DescriptorProto),
) protoreflect.MethodDescriptor {
	t.Helper()
	return mutateDescriptorMethodInput(t, method, func(message *descriptorpb.DescriptorProto) {
		for _, mutate := range mutateMessage {
			mutate(message)
		}
		for _, field := range message.Field {
			if field.GetName() == string(fieldName) {
				mutateField(field)
				return
			}
		}
		t.Fatalf("%s input field %s not found", method.FullName(), fieldName)
	})
}

func operationIndexByLegacyName(
	t *testing.T,
	coverage BoundedMigrationCoverage,
	name string,
) int {
	t.Helper()
	for index, operation := range coverage.Operations {
		if operation.LegacyWireName != nil && *operation.LegacyWireName == name {
			return index
		}
	}
	t.Fatalf("%s descriptor operation not found", name)
	return -1
}

func mutateDescriptorMethodInput(
	t *testing.T,
	method protoreflect.MethodDescriptor,
	mutate func(*descriptorpb.DescriptorProto),
) protoreflect.MethodDescriptor {
	t.Helper()
	return mutateDescriptorMethodRoot(t, method, descriptorMethodInput, mutate)
}

func mutateDescriptorMethodRoot(
	t *testing.T,
	method protoreflect.MethodDescriptor,
	root descriptorMethodRoot,
	mutate func(*descriptorpb.DescriptorProto),
) protoreflect.MethodDescriptor {
	t.Helper()
	fileProto := protodesc.ToFileDescriptorProto(method.ParentFile())
	messageName := method.Input().FullName()
	if root == descriptorMethodOutput {
		messageName = method.Output().FullName()
	}
	message := findMessageProto(t, fileProto, messageName)
	mutate(message)
	file, err := protodesc.NewFile(fileProto, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build mutated descriptor file: %v", err)
	}
	service := file.Services().ByName(method.Parent().Name())
	if service == nil {
		t.Fatalf("mutated descriptor service %s not found", method.Parent().Name())
	}
	mutated := service.Methods().ByName(method.Name())
	if mutated == nil {
		t.Fatalf("mutated descriptor method %s not found", method.Name())
	}
	return mutated
}

func findMessageProto(
	t *testing.T,
	file *descriptorpb.FileDescriptorProto,
	name protoreflect.FullName,
) *descriptorpb.DescriptorProto {
	t.Helper()
	prefix := protoreflect.FullName(file.GetPackage())
	var visit func(protoreflect.FullName, []*descriptorpb.DescriptorProto) *descriptorpb.DescriptorProto
	visit = func(parent protoreflect.FullName, messages []*descriptorpb.DescriptorProto) *descriptorpb.DescriptorProto {
		for _, message := range messages {
			fullName := parent.Append(protoreflect.Name(message.GetName()))
			if fullName == name {
				return message
			}
			if nested := visit(fullName, message.NestedType); nested != nil {
				return nested
			}
		}
		return nil
	}
	message := visit(prefix, file.MessageType)
	if message == nil {
		t.Fatalf("message %s not found in %s", name, file.GetName())
	}
	return message
}

func assertCoverageIssue(t *testing.T, err error, code CoverageIssueCode) {
	t.Helper()
	var coverageError *CoverageError
	if !errors.As(err, &coverageError) {
		t.Fatalf("error = %v, want *CoverageError", err)
	}
	for _, issue := range coverageError.Issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("coverage issues = %+v, want %s", coverageError.Issues, code)
}

func assertFocusedProjectionFixture(t *testing.T, fixture FocusedProjectionFixtureName) {
	t.Helper()
	if _, exists := requiredFocusedProjectionFixtures()[fixture]; !exists {
		t.Fatalf("focused projection fixture %q is not part of bounded coverage", fixture)
	}
}
