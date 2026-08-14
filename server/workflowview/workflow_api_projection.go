package workflowview

import (
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func DerivedWiring(def workflow.Definition, catalogs ...workflow.TargetAgentCatalog) serverapi.WorkflowDerivedWiring {
	var catalog workflow.TargetAgentCatalog
	if len(catalogs) > 0 {
		catalog = catalogs[0]
	}
	derived := workflow.DeriveWiringWithCatalog(def, catalog)
	resp := serverapi.WorkflowDerivedWiring{
		Diagnostics: ValidationErrors(workflow.WorkflowIDPointer(def.ID), derived.Diagnostics),
	}
	for _, node := range def.Nodes {
		nodeID := workflow.NodeIDOf(node)
		resp.Nodes = append(resp.Nodes, serverapi.WorkflowDerivedNodeWiring{
			NodeID:                  string(nodeID),
			PossibleProvisionFields: OutputFields(derived.PossibleProvisionFieldsForNode(nodeID)),
			JoinOutputFields:        OutputFields(derived.JoinOutputFieldsForNode(nodeID)),
		})
	}
	for _, group := range def.TransitionGroups {
		resp.TransitionGroups = append(resp.TransitionGroups, serverapi.WorkflowDerivedTransitionGroupWiring{
			TransitionGroupID:       string(group.ID),
			RequiredProvisionFields: OutputFields(derived.RequiredProvisionFieldsForTransitionGroup(group.ID)),
		})
	}
	for _, edge := range def.Edges {
		applicability := derived.SelectorApplicabilityForEdge(edge.ID)
		resp.Edges = append(resp.Edges, serverapi.WorkflowDerivedEdgeWiring{
			EdgeID:                         string(edge.ID),
			InputBindings:                  InputBindings(derived.InputBindingsForEdge(edge.ID)),
			RequiredProvisionFields:        OutputFields(derived.RequiredProvisionFieldsForEdge(edge.ID)),
			RequiredProviderFields:         OutputFields(derived.RequiredProviderFieldsForJoinEdge(edge.ID)),
			AssigneeSelectionApplicability: selectorApplicability(applicability.Assignee),
			ThinkingSelectionApplicability: selectorApplicability(applicability.Thinking),
		})
	}
	return resp
}

func selectorApplicability(fact workflow.SelectorApplicability) serverapi.WorkflowSelectorApplicability {
	return serverapi.WorkflowSelectorApplicability{
		Available:        fact.Available,
		ParameterVisible: fact.ParameterVisible,
		Reason:           serverapi.WorkflowSelectorApplicabilityReason(fact.Reason),
	}
}

func ValidationErrors(inheritedWorkflowID *runtimeids.WorkflowID, errs []workflow.ValidationError) []serverapi.WorkflowValidationError {
	out := make([]serverapi.WorkflowValidationError, 0, len(errs))
	for _, err := range errs {
		relatedIDs := append([]string(nil), err.RelatedIDs...)
		for _, entity := range err.RelatedEntities {
			relatedIDs = append(relatedIDs, entity.EntityID)
		}
		projected := serverapi.WorkflowValidationError{
			Code:              string(err.Code),
			Message:           err.Message,
			WorkflowID:        err.WorkflowID,
			NodeID:            string(err.NodeID),
			TransitionGroupID: string(err.TransitionGroupID),
			EdgeID:            string(err.EdgeID),
			Details:           validationErrorDetails(err),
			RelatedIDs:        relatedIDs,
			BlocksContext:     err.BlocksContext,
		}
		if projected.WorkflowID == nil {
			projected.WorkflowID = inheritedWorkflowID
		}
		out = append(out, projected)
	}
	return out
}

func validationErrorDetails(err workflow.ValidationError) *serverapi.WorkflowValidationErrorDetails {
	var requiredTool *string
	if err.RequiredTool != nil {
		value := string(*err.RequiredTool)
		requiredTool = &value
	}
	details := serverapi.WorkflowValidationErrorDetails{
		FieldName:      err.FieldName,
		InputName:      err.InputName,
		Placeholder:    err.Placeholder,
		ProviderEdgeID: string(err.ProviderEdgeID),
		Role:           err.AgentRole,
		RequiredTool:   requiredTool,
	}
	if details.FieldName == "" && details.InputName == "" && details.Placeholder == "" && details.ProviderEdgeID == "" && details.Role == nil && details.RequiredTool == nil {
		return nil
	}
	return &details
}

func OutputFields(in []workflow.OutputField) []serverapi.WorkflowOutputField {
	out := make([]serverapi.WorkflowOutputField, 0, len(in))
	for _, field := range in {
		out = append(out, serverapi.WorkflowOutputField{Name: field.Name, Description: field.Description})
	}
	return out
}

func InputBindings(in []workflow.InputBinding) []serverapi.WorkflowInputBinding {
	out := make([]serverapi.WorkflowInputBinding, 0, len(in))
	for _, binding := range in {
		out = append(out, serverapi.WorkflowInputBinding{Name: binding.Name, Source: string(binding.Source), Field: binding.Field})
	}
	return out
}
