package migrationcheck

import (
	"math/big"
	"testing"
	"time"

	"core/shared/protoapi"
	workflowpb "core/shared/protoapi/gen/kent/api/workflow_definition"
	"core/shared/protocol"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWorkflowDefinitionRoutesAndDeferredWorkflowTaskHandoffAreDescriptorOwned(t *testing.T) {
	assertSchemaRoutes(t, "kent.api.workflow_definition", []string{
		protocol.MethodWorkflowCreate,
		protocol.MethodWorkflowCreateAndLinkProject,
		protocol.MethodWorkflowUpdate,
		protocol.MethodWorkflowList,
		protocol.MethodWorkflowGet,
		protocol.MethodWorkflowLinkProject,
		protocol.MethodWorkflowListProjectLinks,
		protocol.MethodWorkflowSetDefaultProjectLink,
		protocol.MethodWorkflowUnlinkProject,
		protocol.MethodWorkflowDeletePreview,
		protocol.MethodWorkflowDelete,
		protocol.MethodWorkflowValidate,
		protocol.MethodWorkflowScriptPathValidate,
		protocol.MethodWorkflowGraphValidateDraft,
		protocol.MethodWorkflowGraphDeriveWiring,
		protocol.MethodWorkflowGraphSavePreview,
		protocol.MethodWorkflowGraphSave,
		protocol.MethodWorkflowProjectLabelCreate,
		protocol.MethodWorkflowProjectLabelList,
		protocol.MethodWorkflowProjectLabelRename,
		protocol.MethodWorkflowProjectLabelDelete,
		protocol.MethodWorkflowProjectLabelReorder,
		protocol.MethodWorkflowSubscribe,
		protocol.MethodWorkflowEvent,
		protocol.MethodWorkflowComplete,
		protocol.MethodWorkflowSubscribeProject,
		protocol.MethodWorkflowProjectEvent,
		protocol.MethodWorkflowProjectComplete,
	})

	for _, legacyName := range []string{
		protocol.MethodWorkflowTaskLabelsGet,
		protocol.MethodWorkflowTaskLabelsUpdate,
		protocol.MethodWorkflowTaskCreate,
		protocol.MethodWorkflowTaskList,
		protocol.MethodWorkflowBoardGet,
		protocol.MethodWorkflowTaskGet,
		protocol.MethodWorkflowAttentionList,
	} {
		if operation := operationByLegacyName(t, legacyName); operation == nil {
			t.Errorf("Workflow Task route %s is missing after its schema handoff", legacyName)
		}
	}
}

func TestWorkflowDefinitionPreservesWholeGraphWriteAndOptimisticVersionFields(t *testing.T) {
	assertExactFields(t, (&workflowpb.GraphDraft{}).ProtoReflect().Descriptor(),
		"node_groups", "nodes", "transition_groups", "edges")
	assertExactFields(t, (&workflowpb.GraphSavePreviewRequest{}).ProtoReflect().Descriptor(),
		"workflow_id", "expected_version", "metadata", "graph")
	assertExactFields(t, (&workflowpb.GraphSaveRequest{}).ProtoReflect().Descriptor(),
		"workflow_id", "expected_version", "metadata", "graph", "confirmation")
	assertExactFields(t, (&workflowpb.GraphSavePreviewSuccess{}).ProtoReflect().Descriptor(),
		"current_version", "changed", "validation_results", "impact", "blockers", "can_save", "confirmation_required")
	assertExactFields(t, (&workflowpb.GraphSaveSuccess{}).ProtoReflect().Descriptor(),
		"saved", "changed", "definition", "current_version", "validation_results", "impact", "blockers", "can_save", "confirmation_required")

	for _, message := range []protoreflect.MessageDescriptor{
		(&workflowpb.GraphSavePreviewRequest{}).ProtoReflect().Descriptor(),
		(&workflowpb.GraphSaveRequest{}).ProtoReflect().Descriptor(),
	} {
		field := message.Fields().ByName("graph")
		if field == nil || field.Message() == nil ||
			field.Message().FullName() != (&workflowpb.GraphDraft{}).ProtoReflect().Descriptor().FullName() {
			t.Errorf("%s.graph does not carry the complete graph draft", message.FullName())
		}
	}
}

func TestWorkflowDefinitionClosedDomainsAndEventUnionAreExact(t *testing.T) {
	assertExactEnumValues(t, workflowpb.ValidationMode_WORKFLOW_VALIDATION_MODE_UNSPECIFIED.Descriptor(),
		"WORKFLOW_VALIDATION_MODE_UNSPECIFIED",
		"WORKFLOW_VALIDATION_MODE_DRAFT",
		"WORKFLOW_VALIDATION_MODE_TASK_CREATION",
		"WORKFLOW_VALIDATION_MODE_EXECUTION")
	assertExactEnumValues(t, workflowpb.ProjectLinkDefaultMode_WORKFLOW_PROJECT_LINK_DEFAULT_MODE_UNSPECIFIED.Descriptor(),
		"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_UNSPECIFIED",
		"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_NEVER",
		"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_ALWAYS",
		"WORKFLOW_PROJECT_LINK_DEFAULT_MODE_IF_PROJECT_HAS_NONE")
	assertExactEnumValues(t, workflowpb.ExecutionTargetMode_WORKFLOW_EXECUTION_TARGET_MODE_UNSPECIFIED.Descriptor(),
		"WORKFLOW_EXECUTION_TARGET_MODE_UNSPECIFIED",
		"WORKFLOW_EXECUTION_TARGET_MODE_NONE",
		"WORKFLOW_EXECUTION_TARGET_MODE_HEAD",
		"WORKFLOW_EXECUTION_TARGET_MODE_DEFAULT_BRANCH",
		"WORKFLOW_EXECUTION_TARGET_MODE_CUSTOM_REF",
		"WORKFLOW_EXECUTION_TARGET_MODE_ASK_ON_FIRST_EXECUTION")
	assertExactEnumValues(t, workflowpb.GraphEntityType_WORKFLOW_GRAPH_ENTITY_TYPE_UNSPECIFIED.Descriptor(),
		"WORKFLOW_GRAPH_ENTITY_TYPE_UNSPECIFIED",
		"WORKFLOW_GRAPH_ENTITY_TYPE_EDGE",
		"WORKFLOW_GRAPH_ENTITY_TYPE_NODE",
		"WORKFLOW_GRAPH_ENTITY_TYPE_NODE_GROUP",
		"WORKFLOW_GRAPH_ENTITY_TYPE_TRANSITION_GROUP")
	assertExactEnumValues(t, workflowpb.ProjectEventResource_WORKFLOW_PROJECT_EVENT_RESOURCE_UNSPECIFIED.Descriptor(),
		"WORKFLOW_PROJECT_EVENT_RESOURCE_UNSPECIFIED",
		"WORKFLOW_PROJECT_EVENT_RESOURCE_WORKFLOW",
		"WORKFLOW_PROJECT_EVENT_RESOURCE_WORKFLOW_LINK",
		"WORKFLOW_PROJECT_EVENT_RESOURCE_TASK",
		"WORKFLOW_PROJECT_EVENT_RESOURCE_LABEL")
	assertExactEnumValues(t, workflowpb.ProjectEventAction_WORKFLOW_PROJECT_EVENT_ACTION_UNSPECIFIED.Descriptor(),
		"WORKFLOW_PROJECT_EVENT_ACTION_UNSPECIFIED",
		"WORKFLOW_PROJECT_EVENT_ACTION_CREATED",
		"WORKFLOW_PROJECT_EVENT_ACTION_UPDATED",
		"WORKFLOW_PROJECT_EVENT_ACTION_RENAMED",
		"WORKFLOW_PROJECT_EVENT_ACTION_REORDERED",
		"WORKFLOW_PROJECT_EVENT_ACTION_DELETED",
		"WORKFLOW_PROJECT_EVENT_ACTION_GRAPH_SAVED",
		"WORKFLOW_PROJECT_EVENT_ACTION_LINKED",
		"WORKFLOW_PROJECT_EVENT_ACTION_DEFAULT_CHANGED",
		"WORKFLOW_PROJECT_EVENT_ACTION_UNLINKED",
		"WORKFLOW_PROJECT_EVENT_ACTION_STARTED",
		"WORKFLOW_PROJECT_EVENT_ACTION_INTERRUPTED",
		"WORKFLOW_PROJECT_EVENT_ACTION_RESUMED",
		"WORKFLOW_PROJECT_EVENT_ACTION_APPROVED",
		"WORKFLOW_PROJECT_EVENT_ACTION_MOVED",
		"WORKFLOW_PROJECT_EVENT_ACTION_CANCELED",
		"WORKFLOW_PROJECT_EVENT_ACTION_COMPLETED",
		"WORKFLOW_PROJECT_EVENT_ACTION_COMMENT_ADDED",
		"WORKFLOW_PROJECT_EVENT_ACTION_COMMENT_UPDATED",
		"WORKFLOW_PROJECT_EVENT_ACTION_COMMENT_DELETED",
		"WORKFLOW_PROJECT_EVENT_ACTION_QUESTION_WAITING",
		"WORKFLOW_PROJECT_EVENT_ACTION_QUESTION_CLEARED",
		"WORKFLOW_PROJECT_EVENT_ACTION_LABELS_CHANGED",
		"WORKFLOW_PROJECT_EVENT_ACTION_DEPENDENCIES_CHANGED")
}

func TestWorkflowDefinitionOperationErrorAssociationsAreResolved(t *testing.T) {
	for _, fixture := range []struct {
		message protoreflect.MessageDescriptor
		details []protoreflect.Name
	}{
		{(&workflowpb.CreateError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "internal_failure"}},
		{(&workflowpb.CreateAndLinkProjectError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "project_not_found", "internal_failure"}},
		{(&workflowpb.UpdateError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "workflow_not_found", "internal_failure"}},
		{(&workflowpb.ListError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "internal_failure"}},
		{(&workflowpb.GetError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "workflow_not_found", "internal_failure"}},
		{(&workflowpb.GraphSavePreviewError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "workflow_not_found", "internal_failure"}},
		{(&workflowpb.GraphSaveError{}).ProtoReflect().Descriptor(), []protoreflect.Name{"invalid_request", "workflow_not_found", "internal_failure"}},
		{(&workflowpb.ProjectLabelCreateError{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"invalid_name", "name_conflict", "catalog_limit", "project_not_found", "invalid_mutation", "internal_failure",
		}},
		{(&workflowpb.ProjectLabelCatalogError{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"invalid_request", "project_not_found", "internal_failure",
		}},
		{(&workflowpb.ProjectLabelRenameError{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"invalid_name", "name_conflict", "label_not_found", "project_not_found", "invalid_mutation", "internal_failure",
		}},
		{(&workflowpb.ProjectLabelDeleteError{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"label_not_found", "invalid_mutation", "project_not_found", "internal_failure",
		}},
		{(&workflowpb.ProjectLabelReorderError{}).ProtoReflect().Descriptor(), []protoreflect.Name{
			"invalid_mutation", "project_not_found", "internal_failure",
		}},
	} {
		if fixture.message.Fields().Get(0).Name() != "code" {
			t.Errorf("%s first field is not code", fixture.message.FullName())
		}
		assertMessageOneofFields(t, fixture.message, "detail", fixture.details...)
	}
}

func TestWorkflowDefinitionJavaScriptVisibleInt64FieldsAreSafe(t *testing.T) {
	const maxSafeInteger = int64(9007199254740991)
	wantSignedMinimum := big.NewInt(-maxSafeInteger)
	wantMaximum := big.NewInt(maxSafeInteger)
	for _, operation := range mustOperationsInPackage(t, "kent.api.workflow_definition") {
		for _, message := range []protoreflect.MessageDescriptor{
			operation.Descriptor.Input(),
			operation.Descriptor.Output(),
		} {
			walkMessageFields(message, func(field protoreflect.FieldDescriptor) {
				if field.ContainingMessage().ParentFile().Package() != "kent.api.workflow_definition" {
					return
				}
				switch field.Kind() {
				case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
					got := integerBounds(field, descriptorPolicyInt64)
					if got.Minimum == nil || got.Maximum == nil ||
						got.Minimum.Cmp(wantSignedMinimum) < 0 || got.Maximum.Cmp(wantMaximum) > 0 {
						t.Errorf("%s.%s signed bounds = %+v, want JavaScript safe range", field.ContainingMessage().FullName(), field.Name(), got)
					}
				case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
					got := integerBounds(field, descriptorPolicyUint64)
					if got.Minimum == nil || got.Maximum == nil ||
						got.Minimum.Sign() != 0 || got.Maximum.Cmp(wantMaximum) != 0 {
						t.Errorf("%s.%s unsigned bounds = %+v, want JavaScript safe range", field.ContainingMessage().FullName(), field.Name(), got)
					}
				}
			})
		}
	}
}

func TestWorkflowDefinitionGeneratedValidationCoversGraphAndLabelBoundaries(t *testing.T) {
	validUUID := "123e4567-e89b-42d3-a456-426614174000"
	request := &workflowpb.GraphSaveRequest{
		WorkflowId:      validUUID,
		ExpectedVersion: 1,
		Graph: &workflowpb.GraphDraft{
			Nodes: []*workflowpb.GraphDraftNode{{
				Id:          validUUID,
				Key:         "start",
				Kind:        workflowpb.NodeKind_WORKFLOW_NODE_KIND_START,
				DisplayName: "Start",
			}},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(request); err != nil {
		t.Fatalf("valid whole-graph save request: %v", err)
	}
	request.ExpectedVersion = -1
	if err := protoapi.ValidateGeneratedMessage(request); err == nil {
		t.Fatal("negative optimistic version was accepted")
	}

	label := &workflowpb.ProjectLabel{Id: validUUID, Name: "Priority"}
	if err := protoapi.ValidateGeneratedMessage(label); err != nil {
		t.Fatalf("valid project label: %v", err)
	}
	label.Id = "not-a-uuid"
	if err := protoapi.ValidateGeneratedMessage(label); err == nil {
		t.Fatal("non-canonical label UUID was accepted")
	}
}

func TestWorkflowDefinitionGeneratedValidationCoversCrossFieldGraphRules(t *testing.T) {
	validUUID := "123e4567-e89b-42d3-a456-426614174000"
	nonAgentRole := &workflowpb.GraphDraftNode{
		Kind:         workflowpb.NodeKind_WORKFLOW_NODE_KIND_START,
		SubagentRole: stringPointer(" "),
	}
	if err := protoapi.ValidateGeneratedMessage(nonAgentRole); err == nil {
		t.Fatal("non-agent node with a present blank subagent role was accepted")
	}
	nonAgentRole.SubagentRole = stringPointer("worker")
	if err := protoapi.ValidateGeneratedMessage(nonAgentRole); err == nil {
		t.Fatal("non-agent node with a subagent role was accepted")
	}
	nonAgentRole.SubagentRole = nil
	if err := protoapi.ValidateGeneratedMessage(nonAgentRole); err != nil {
		t.Fatalf("non-agent node without a subagent role: %v", err)
	}

	validContext := &workflowpb.ContextSource{
		Kind: workflowpb.ContextSourceKind_WORKFLOW_CONTEXT_SOURCE_KIND_IMMEDIATE_SOURCE,
	}
	if err := protoapi.ValidateGeneratedMessage(validContext); err != nil {
		t.Fatalf("valid immediate context source: %v", err)
	}

	selectedNode := &workflowpb.ContextSource{
		Kind: workflowpb.ContextSourceKind_WORKFLOW_CONTEXT_SOURCE_KIND_SELECTED_NODE,
	}
	if err := protoapi.ValidateGeneratedMessage(selectedNode); err == nil {
		t.Fatal("selected-node context source without node key was accepted")
	}
	selectedNode.NodeKey = stringPointer("prior_node")
	if err := protoapi.ValidateGeneratedMessage(selectedNode); err != nil {
		t.Fatalf("valid selected-node context source: %v", err)
	}

	selector := &workflowpb.SelectorApplicability{
		Available:        true,
		ParameterVisible: true,
		Reason:           workflowpb.SelectorApplicabilityReason_WORKFLOW_SELECTOR_APPLICABILITY_REASON_ELIGIBLE,
	}
	if err := protoapi.ValidateGeneratedMessage(selector); err != nil {
		t.Fatalf("valid eligible selector applicability: %v", err)
	}
	selector.Available = false
	if err := protoapi.ValidateGeneratedMessage(selector); err == nil {
		t.Fatal("eligible unavailable selector applicability was accepted")
	}

	edge := &workflowpb.GraphDraftEdge{
		Id:                validUUID,
		TransitionGroupId: validUUID,
		Key:               "next",
		TargetNodeId:      validUUID,
		AssigneeSelection: workflowpb.AssigneeSelection_WORKFLOW_ASSIGNEE_SELECTION_PREVIOUS_NODE,
		ThinkingSelection: workflowpb.ThinkingSelection_WORKFLOW_THINKING_SELECTION_CONFIGURED,
		ContextMode:       workflowpb.ContextMode_WORKFLOW_CONTEXT_MODE_NEW_SESSION,
		ContextSource:     validContext,
	}
	if err := protoapi.ValidateGeneratedMessage(edge); err == nil {
		t.Fatal("previous-node assignee selection without protected parameter was accepted")
	}
	edge.Parameters = []*workflowpb.Parameter{{
		Key:     "target_assignee",
		Purpose: workflowpb.ParameterPurpose_WORKFLOW_PARAMETER_PURPOSE_TARGET_ASSIGNEE,
	}}
	if err := protoapi.ValidateGeneratedMessage(edge); err != nil {
		t.Fatalf("valid protected assignee parameter: %v", err)
	}
	edge.Parameters = append(edge.Parameters, &workflowpb.Parameter{
		Key:     "duplicate_purpose",
		Purpose: workflowpb.ParameterPurpose_WORKFLOW_PARAMETER_PURPOSE_TARGET_ASSIGNEE,
	})
	if err := protoapi.ValidateGeneratedMessage(edge); err == nil {
		t.Fatal("duplicate protected parameter purpose was accepted")
	}

	reference := &workflowpb.GraphEntityReference{
		EntityType: workflowpb.GraphEntityType_WORKFLOW_GRAPH_ENTITY_TYPE_EDGE,
		EntityId:   validUUID,
	}
	if err := protoapi.ValidateGeneratedMessage(reference); err != nil {
		t.Fatalf("valid graph entity reference: %v", err)
	}
	reference.EntityId = "not-a-uuid"
	if err := protoapi.ValidateGeneratedMessage(reference); err == nil {
		t.Fatal("invalid graph entity reference ID was accepted")
	}
}

func TestWorkflowDefinitionGeneratedValidationCoversProjectEventInvariants(t *testing.T) {
	validUUID := "123e4567-e89b-42d3-a456-426614174000"
	projectID := "project-1"
	event := &workflowpb.ProjectEvent{
		WorkflowId:      stringPointer(validUUID),
		Resource:        workflowpb.ProjectEventResource_WORKFLOW_PROJECT_EVENT_RESOURCE_WORKFLOW,
		Action:          workflowpb.ProjectEventAction_WORKFLOW_PROJECT_EVENT_ACTION_UPDATED,
		PrimaryEntityId: validUUID,
		OccurredAt:      timestamppb.New(time.Unix(1_700_000_000, 0)),
	}
	if err := protoapi.ValidateGeneratedMessage(event); err != nil {
		t.Fatalf("valid workflow event: %v", err)
	}

	event.Action = workflowpb.ProjectEventAction_WORKFLOW_PROJECT_EVENT_ACTION_LINKED
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("workflow event with workflow-link action was accepted")
	}
	event.Action = workflowpb.ProjectEventAction_WORKFLOW_PROJECT_EVENT_ACTION_UPDATED
	event.WorkflowId = nil
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("workflow event without workflow ID was accepted")
	}

	event = &workflowpb.ProjectEvent{
		ProjectId:       &projectID,
		Resource:        workflowpb.ProjectEventResource_WORKFLOW_PROJECT_EVENT_RESOURCE_LABEL,
		Action:          workflowpb.ProjectEventAction_WORKFLOW_PROJECT_EVENT_ACTION_CREATED,
		PrimaryEntityId: validUUID,
		RelatedIds:      []string{validUUID},
		OccurredAt:      timestamppb.New(time.Unix(1_700_000_000, 0)),
	}
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("event with related ID repeating primary entity was accepted")
	}
	event.RelatedIds = []string{" related "}
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("event with untrimmed related ID was accepted")
	}
	event.RelatedIds = nil
	event.WorkflowId = stringPointer(validUUID)
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("label event with workflow ID was accepted")
	}
	event.WorkflowId = nil
	event.ProjectId = stringPointer(" project-1 ")
	if err := protoapi.ValidateGeneratedMessage(event); err == nil {
		t.Fatal("event with untrimmed project ID was accepted")
	}
}

func TestWorkflowDefinitionGeneratedValidationCoversLabelCatalogConstraints(t *testing.T) {
	validUUID := "123e4567-e89b-42d3-a456-426614174000"
	catalog := &workflowpb.ProjectLabelCatalog{
		ProjectId: "project-1",
		Labels: []*workflowpb.ProjectLabel{
			{Id: validUUID, Name: "Priority"},
			{Id: validUUID, Name: "Duplicate"},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(catalog); err == nil {
		t.Fatal("label catalog with duplicate IDs was accepted")
	}
	catalog.Labels = []*workflowpb.ProjectLabel{{Id: validUUID, Name: "   "}}
	if err := protoapi.ValidateGeneratedMessage(catalog); err == nil {
		t.Fatal("label catalog with blank label name was accepted")
	}
}

func TestWorkflowDefinitionReviewedValidatorSignoffsAreMessageLocal(t *testing.T) {
	want := map[Identity]struct{}{
		typeMethodIdentity("core/shared/serverapi", "WorkflowCreateAndLinkProjectRequest", "Validate"):   {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowCreateRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowDeletePreviewRequest", "Validate"):          {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowDeleteRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowDerivedEdgeWiring", "Validate"):             {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGetRequest", "Validate"):                    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphDeriveWiringRequest", "Validate"):      {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSaveBlocker", "Validate"):              {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSaveImpact", "Validate"):               {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSavePreviewRequest", "Validate"):       {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSavePreviewRequest", "ValidateRPC"):    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSavePreviewResponse", "Validate"):      {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSaveRequest", "Validate"):              {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSaveRequest", "ValidateRPC"):           {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphSaveResponse", "Validate"):             {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowGraphValidateDraftRequest", "Validate"):     {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowLinkProjectRequest", "Validate"):            {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowListProjectLinksRequest", "Validate"):       {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowListRequest", "Validate"):                   {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabel", "Validate"):                  {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelCatalog", "Validate"):           {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelCatalogRequest", "Validate"):    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelCatalogResponse", "Validate"):   {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelCreateRequest", "Validate"):     {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelCreateRequest", "ValidateRPC"):  {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelCreateResponse", "Validate"):    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelDeleteRequest", "Validate"):     {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelDeleteRequest", "ValidateRPC"):  {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelDeleteResponse", "Validate"):    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelRenameRequest", "Validate"):     {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelRenameRequest", "ValidateRPC"):  {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelRenameResponse", "Validate"):    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelReorderRequest", "Validate"):    {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelReorderRequest", "ValidateRPC"): {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectLabelReorderResponse", "Validate"):   {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowProjectSubscribeRequest", "Validate"):       {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowScriptPathValidateRequest", "Validate"):     {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowSelectorApplicability", "Validate"):         {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowSetDefaultProjectLinkRequest", "Validate"):  {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowSubscribeRequest", "Validate"):              {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowUnlinkProjectRequest", "Validate"):          {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowUpdateRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "WorkflowValidateRequest", "Validate"):               {},
	}
	for _, signoff := range ExecutionTargetDomainSignoffs() {
		if signoff.Domain != "workflow_definition" {
			continue
		}
		for _, validator := range signoff.Classification.Validators {
			if _, expected := want[validator.Identity]; !expected {
				continue
			}
			if validator.Kind != ValidatorMessageLocal || validator.Owner != nil {
				t.Errorf("%s owner = %+v kind=%s", validator.Identity, validator.Owner, validator.Kind)
			}
			delete(want, validator.Identity)
		}
	}
	for identity := range want {
		t.Errorf("missing message-local Workflow-definition validator sign-off %s", identity)
	}
}
