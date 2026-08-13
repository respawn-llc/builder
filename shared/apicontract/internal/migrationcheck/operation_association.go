package migrationcheck

import (
	"fmt"
	"sort"

	"core/shared/apicontract"
	"core/shared/protoapi"
)

// OperationDescriptor is the migration check's descriptor-facing boundary.
// The Protobuf adapter added by a later slice can project method descriptors
// into this shape without making fixtures depend on generated schemas.
type OperationDescriptor struct {
	Package        string
	Service        string
	Method         string
	LegacyWireName *string
	Kind           apicontract.Kind
	Event          *OperationReference
	Completion     *OperationReference
}

type OperationReference struct {
	Package string
	Service string
	Method  string
}

// OperationDescriptorSource permits both generated descriptor registries and
// small in-memory fixtures to exercise the same association behavior.
type OperationDescriptorSource interface {
	OperationDescriptors() []OperationDescriptor
}

type AssociationIssueCode string

const (
	IssueMissingLegacyDescriptor      AssociationIssueCode = "missing_legacy_descriptor"
	IssueDuplicateLegacyDescriptor    AssociationIssueCode = "duplicate_legacy_descriptor"
	IssueDuplicateLegacyRoute         AssociationIssueCode = "duplicate_legacy_route"
	IssueDescriptorWithoutLegacyName  AssociationIssueCode = "descriptor_without_legacy_name"
	IssueDescriptorWithoutLegacyRoute AssociationIssueCode = "descriptor_without_legacy_route"
	IssueWrongOperationKind           AssociationIssueCode = "wrong_operation_kind"
	IssueWrongEventAssociation        AssociationIssueCode = "wrong_event_association"
	IssueWrongCompletionAssociation   AssociationIssueCode = "wrong_completion_association"
	IssueUnexpectedEventAssociation   AssociationIssueCode = "unexpected_event_association"
	IssueUnexpectedCompletion         AssociationIssueCode = "unexpected_completion_association"
	IssueInvalidPackage               AssociationIssueCode = "invalid_package"
	IssueInvalidPascalCaseIdentifier  AssociationIssueCode = "invalid_pascal_case_identifier"
	IssueDuplicateActiveName          AssociationIssueCode = "duplicate_active_name"
	IssueActiveNameIsUnapprovedAlias  AssociationIssueCode = "active_name_is_unapproved_alias"
)

type AssociationIssue struct {
	Code       AssociationIssueCode
	LegacyName *string
	ActiveName *string
	Detail     string
}

type AssociationError struct {
	Issues []AssociationIssue
}

func (e *AssociationError) Error() string {
	return fmt.Sprintf("operation association failed with %d issue(s)", len(e.Issues))
}

// CheckOperationAssociations verifies the temporary legacy provenance join and
// the independently derived active operation identities. Legacy names are
// association evidence only; they are never inserted into the active index.
func CheckOperationAssociations(
	routes []apicontract.Route,
	source OperationDescriptorSource,
	unapprovedAliases []string,
) error {
	descriptors := source.OperationDescriptors()
	issues := make([]AssociationIssue, 0)

	routesByLegacyName := make(map[string][]apicontract.Route, len(routes))
	for _, route := range routes {
		routesByLegacyName[route.Method] = append(routesByLegacyName[route.Method], route)
	}
	for legacyName, matchingRoutes := range routesByLegacyName {
		if len(matchingRoutes) > 1 {
			name := legacyName
			issues = append(issues, AssociationIssue{
				Code:       IssueDuplicateLegacyRoute,
				LegacyName: &name,
			})
		}
	}

	descriptorsByLegacyName := make(map[string][]descriptorIdentity, len(descriptors))
	descriptorsByActiveName := make(map[string][]descriptorIdentity, len(descriptors))
	descriptorsByDeclaration := make(map[string]map[string]map[string][]descriptorIdentity)
	for index, descriptor := range descriptors {
		identity, descriptorIssues := deriveDescriptorIdentity(index, descriptor)
		issues = append(issues, descriptorIssues...)
		addDescriptorDeclaration(descriptorsByDeclaration, identity)
		if descriptor.LegacyWireName == nil {
			issues = append(issues, AssociationIssue{
				Code:       IssueDescriptorWithoutLegacyName,
				ActiveName: identity.activeName,
			})
		} else {
			descriptorsByLegacyName[*descriptor.LegacyWireName] = append(
				descriptorsByLegacyName[*descriptor.LegacyWireName],
				identity,
			)
		}
		if identity.activeName != nil {
			descriptorsByActiveName[*identity.activeName] = append(
				descriptorsByActiveName[*identity.activeName],
				identity,
			)
		}
	}

	for activeName, matchingDescriptors := range descriptorsByActiveName {
		if len(matchingDescriptors) > 1 {
			name := activeName
			issues = append(issues, AssociationIssue{
				Code:       IssueDuplicateActiveName,
				ActiveName: &name,
			})
		}
		for _, alias := range unapprovedAliases {
			if activeName == alias {
				name := activeName
				issues = append(issues, AssociationIssue{
					Code:       IssueActiveNameIsUnapprovedAlias,
					ActiveName: &name,
				})
			}
		}
	}

	for legacyName, matchingRoutes := range routesByLegacyName {
		matchingDescriptors := descriptorsByLegacyName[legacyName]
		if len(matchingDescriptors) == 0 {
			name := legacyName
			issues = append(issues, AssociationIssue{
				Code:       IssueMissingLegacyDescriptor,
				LegacyName: &name,
			})
			continue
		}
		if len(matchingDescriptors) > 1 {
			name := legacyName
			issues = append(issues, AssociationIssue{
				Code:       IssueDuplicateLegacyDescriptor,
				LegacyName: &name,
			})
			continue
		}
		if len(matchingRoutes) != 1 {
			continue
		}
		checkRouteAssociation(
			matchingRoutes[0],
			matchingDescriptors[0],
			descriptorsByLegacyName,
			descriptorsByDeclaration,
			&issues,
		)
	}

	for legacyName := range descriptorsByLegacyName {
		if _, exists := routesByLegacyName[legacyName]; !exists {
			name := legacyName
			issues = append(issues, AssociationIssue{
				Code:       IssueDescriptorWithoutLegacyRoute,
				LegacyName: &name,
			})
		}
	}

	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		if comparison := compareOptionalStrings(issues[left].LegacyName, issues[right].LegacyName); comparison != 0 {
			return comparison < 0
		}
		return compareOptionalStrings(issues[left].ActiveName, issues[right].ActiveName) < 0
	})
	return &AssociationError{Issues: issues}
}

type descriptorIdentity struct {
	index      int
	descriptor OperationDescriptor
	activeName *string
}

func deriveDescriptorIdentity(index int, descriptor OperationDescriptor) (descriptorIdentity, []AssociationIssue) {
	if err := protoapi.ValidatePackageName(descriptor.Package); err != nil {
		return descriptorIdentity{index: index, descriptor: descriptor}, []AssociationIssue{{
			Code:   IssueInvalidPackage,
			Detail: fmt.Sprintf("descriptor %d: %v", index, err),
		}}
	}
	activeName, err := protoapi.ActiveOperationName(descriptor.Package, descriptor.Service, descriptor.Method)
	if err != nil {
		return descriptorIdentity{index: index, descriptor: descriptor}, []AssociationIssue{{
			Code:   IssueInvalidPascalCaseIdentifier,
			Detail: fmt.Sprintf("descriptor %d: %v", index, err),
		}}
	}
	return descriptorIdentity{index: index, descriptor: descriptor, activeName: &activeName}, nil
}

func checkRouteAssociation(
	route apicontract.Route,
	descriptor descriptorIdentity,
	descriptorsByLegacyName map[string][]descriptorIdentity,
	descriptorsByDeclaration map[string]map[string]map[string][]descriptorIdentity,
	issues *[]AssociationIssue,
) {
	legacyName := route.Method
	if descriptor.descriptor.Kind != route.Kind {
		*issues = append(*issues, AssociationIssue{
			Code:       IssueWrongOperationKind,
			LegacyName: &legacyName,
			ActiveName: descriptor.activeName,
		})
	}
	checkAssociatedOperation(
		route.EventMethod,
		descriptor.descriptor.Event,
		IssueWrongEventAssociation,
		IssueUnexpectedEventAssociation,
		legacyName,
		descriptorsByLegacyName,
		descriptorsByDeclaration,
		issues,
	)
	checkAssociatedOperation(
		route.CompleteMethod,
		descriptor.descriptor.Completion,
		IssueWrongCompletionAssociation,
		IssueUnexpectedCompletion,
		legacyName,
		descriptorsByLegacyName,
		descriptorsByDeclaration,
		issues,
	)
}

func checkAssociatedOperation(
	legacyAssociatedName string,
	reference *OperationReference,
	wrongCode AssociationIssueCode,
	unexpectedCode AssociationIssueCode,
	parentLegacyName string,
	descriptorsByLegacyName map[string][]descriptorIdentity,
	descriptorsByDeclaration map[string]map[string]map[string][]descriptorIdentity,
	issues *[]AssociationIssue,
) {
	if legacyAssociatedName == "" {
		if reference != nil {
			parent := parentLegacyName
			*issues = append(*issues, AssociationIssue{
				Code:       unexpectedCode,
				LegacyName: &parent,
			})
		}
		return
	}

	expectedDescriptors := descriptorsByLegacyName[legacyAssociatedName]
	if len(expectedDescriptors) != 1 || reference == nil {
		parent := parentLegacyName
		*issues = append(*issues, AssociationIssue{
			Code:       wrongCode,
			LegacyName: &parent,
		})
		return
	}
	referencedDescriptors := lookupDescriptorDeclaration(descriptorsByDeclaration, *reference)
	if len(referencedDescriptors) != 1 || referencedDescriptors[0].index != expectedDescriptors[0].index {
		parent := parentLegacyName
		*issues = append(*issues, AssociationIssue{
			Code:       wrongCode,
			LegacyName: &parent,
		})
	}
}

func addDescriptorDeclaration(
	descriptors map[string]map[string]map[string][]descriptorIdentity,
	identity descriptorIdentity,
) {
	services := descriptors[identity.descriptor.Package]
	if services == nil {
		services = make(map[string]map[string][]descriptorIdentity)
		descriptors[identity.descriptor.Package] = services
	}
	methods := services[identity.descriptor.Service]
	if methods == nil {
		methods = make(map[string][]descriptorIdentity)
		services[identity.descriptor.Service] = methods
	}
	methods[identity.descriptor.Method] = append(methods[identity.descriptor.Method], identity)
}

func lookupDescriptorDeclaration(
	descriptors map[string]map[string]map[string][]descriptorIdentity,
	reference OperationReference,
) []descriptorIdentity {
	services := descriptors[reference.Package]
	if services == nil {
		return nil
	}
	methods := services[reference.Service]
	if methods == nil {
		return nil
	}
	return methods[reference.Method]
}

func compareOptionalStrings(left *string, right *string) int {
	if left == nil {
		if right == nil {
			return 0
		}
		return -1
	}
	if right == nil {
		return 1
	}
	switch {
	case *left < *right:
		return -1
	case *left > *right:
		return 1
	default:
		return 0
	}
}
