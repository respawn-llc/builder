package workflowstore

import (
	"context"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func (s *Store) currentNodeCompletionOutputIssues(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	group workflow.TransitionGroup,
	source workflow.Node,
	targets []currentNodeCompletionTarget,
	currentSource workflow.CurrentNode,
	values map[string]string,
) ([]CompletionValidationIssue, error) {
	wiring := workflow.DeriveWiring(definition)
	required := []workflow.OutputField{}
	knownFields := []workflow.OutputField{}
	for _, target := range targets {
		planned, err := s.planTransitionParameterContract(
			ctx,
			q,
			definition,
			target.Edge,
			source,
			target.Node,
			&currentSource,
			transitionBranchKeyForCurrentNode(currentSource.Reference),
			false,
			true,
		)
		if err != nil {
			return nil, err
		}
		for _, parameter := range planned.KnownParameters {
			knownFields = appendCompletionOutputField(knownFields, workflow.OutputField{
				Name:        parameter.Key,
				Description: parameter.Description,
			})
		}
		for _, parameter := range planned.Parameters {
			required = appendCompletionOutputField(required, workflow.OutputField{
				Name:        parameter.Key,
				Description: parameter.Description,
			})
		}
	}
	if source.Kind() == workflow.NodeKindJoin {
		required = wiring.CurrentNodeOutputFieldsForTransitionGroup(group.ID)
		knownFields = append([]workflow.OutputField(nil), required...)
	}
	for _, target := range targets {
		if target.Node.Kind() != workflow.NodeKindJoin {
			continue
		}
		for _, field := range wiring.RequiredProviderFieldsForJoinEdge(target.Edge.ID) {
			knownFields = appendCompletionOutputField(knownFields, field)
			required = appendCompletionOutputField(required, field)
		}
	}
	known := make(map[string]struct{}, len(knownFields))
	for _, field := range knownFields {
		name := strings.TrimSpace(field.Name)
		if name != "" {
			known[name] = struct{}{}
		}
	}
	issues := []CompletionValidationIssue{}
	for _, name := range sortedStringKeys(values) {
		field := strings.TrimSpace(name)
		if field == "" {
			continue
		}
		if _, exists := known[field]; !exists {
			issues = append(issues, CompletionValidationIssue{
				Code:    CompletionCodeUnknownOutputField,
				Field:   field,
				Message: "output field is not declared by source node",
			})
		}
	}
	for _, field := range required {
		name := strings.TrimSpace(field.Name)
		if name != "" && strings.TrimSpace(values[name]) == "" {
			code := CompletionCodeRequiredOutputMissing
			message := "required output is missing"
			if parameter, ok := transitionParameterForOutputField(targets, name); ok &&
				workflow.CanonicalParameterPurpose(parameter.Purpose) == workflow.ParameterPurposeTargetAssignee {
				code = CompletionCodeUnavailableTargetAgentRole
				message = "a selectable target Agent role is required"
			}
			issues = append(issues, CompletionValidationIssue{Code: code, Field: name, Message: message})
		}
	}
	return issues, nil
}

func transitionParameterForOutputField(targets []currentNodeCompletionTarget, field string) (workflow.Parameter, bool) {
	for _, target := range targets {
		for _, parameter := range target.Edge.Parameters {
			if strings.TrimSpace(parameter.Key) == strings.TrimSpace(field) {
				return parameter, true
			}
		}
	}
	return workflow.Parameter{}, false
}

func appendCompletionOutputField(fields []workflow.OutputField, field workflow.OutputField) []workflow.OutputField {
	if completionOutputFieldPresent(fields, field.Name) {
		return fields
	}
	return append(fields, field)
}

func completionOutputFieldPresent(fields []workflow.OutputField, name string) bool {
	for _, field := range fields {
		if strings.TrimSpace(field.Name) == strings.TrimSpace(name) {
			return true
		}
	}
	return false
}
