package migrationcheck

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/types"
	"reflect"
	"sort"
	"strings"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"core/shared/apicontract"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type FocusedProjectionFixtureName string

const (
	FocusedKENT345StrictJSON              FocusedProjectionFixtureName = "kent345_strict_json"
	FocusedKENT345CustomWire              FocusedProjectionFixtureName = "kent345_custom_wire"
	FocusedKENT345Hydration               FocusedProjectionFixtureName = "kent345_hydration"
	FocusedKENT345Uniqueness              FocusedProjectionFixtureName = "kent345_uniqueness"
	FocusedKENT345MixedValidators         FocusedProjectionFixtureName = "kent345_mixed_validators"
	FocusedKENT554NegotiationValidation   FocusedProjectionFixtureName = "kent554_negotiation_validation"
	FocusedKENT554NegotiationConstants    FocusedProjectionFixtureName = "kent554_negotiation_constants"
	FocusedKENT554RetainedCapabilityFacts FocusedProjectionFixtureName = "kent554_retained_capability_facts"
)

type FocusedProjectionFixture struct {
	Name  FocusedProjectionFixtureName
	Check func() error
}

type BoundedMigrationCoverage struct {
	Report                 Report
	Operations             []protoapi.Operation
	Classification         DeclarationClassification
	FocusedFixtures        []FocusedProjectionFixture
	WireExceptions         []WireException
	FieldRenames           []WireFieldRename
	ScalarMappings         []WireScalarMapping
	PresenceMappings       []WirePresenceMapping
	ExceptionalFingerprint string
	ClosedEnumFingerprint  string
}

type WirePresenceMapping struct {
	LegacyType reflect.Type
	Message    protoreflect.FullName
	Field      protoreflect.Name
	Optional   bool
}

type WireScalarMapping struct {
	Message protoreflect.FullName
	Field   protoreflect.Name
	Kind    protoreflect.Kind
}

type WireFieldRename struct {
	LegacyType      reflect.Type
	Message         protoreflect.FullName
	LegacyField     string
	DescriptorField protoreflect.Name
}

type WireException struct {
	LegacyType            reflect.Type
	Message               protoreflect.FullName
	LegacyFingerprint     string
	DescriptorFingerprint string
}

type CoverageIssueCode string

const (
	IssueCoveragePredecessorSet        CoverageIssueCode = "predecessor_set"
	IssueCoveragePredecessorDuplicate  CoverageIssueCode = "predecessor_duplicate"
	IssueCoveragePredecessorUnresolved CoverageIssueCode = "predecessor_unresolved"
	IssueCoverageRouteAssociation      CoverageIssueCode = "route_association"
	IssueCoverageRouteMetadata         CoverageIssueCode = "route_metadata"
	IssueCoverageProjectedWireFact     CoverageIssueCode = "projected_wire_fact_authored"
	IssueCoverageWireShape             CoverageIssueCode = "wire_shape"
	IssueCoverageWireException         CoverageIssueCode = "wire_exception"
	IssueCoverageDeclaration           CoverageIssueCode = "declaration"
	IssueCoverageFocusedFixture        CoverageIssueCode = "focused_fixture"
)

type CoverageIssue struct {
	Code   CoverageIssueCode
	Detail string
}

type CoverageError struct {
	Issues []CoverageIssue
}

func (e *CoverageError) Error() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "bounded migration coverage failed with %d issue(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(&diagnostic, "\n- %s: %s", issue.Code, issue.Detail)
	}
	return diagnostic.String()
}

// CheckBoundedMigrationCoverage composes the migration-only checks over the
// actual route, reflection, declaration, and generated descriptor authorities.
// It retains no copied route, wire-type, scalar, or validator inventory.
func CheckBoundedMigrationCoverage(coverage BoundedMigrationCoverage) error {
	issues := make([]CoverageIssue, 0)
	checkCoveragePredecessors(coverage.Report.Predecessors, &issues)
	checkCoverageOperations(
		coverage.Report.Routes,
		coverage.Operations,
		&issues,
	)
	checkCoverageWireShapes(
		coverage.Report.Routes,
		coverage.Operations,
		coverage.WireExceptions,
		coverage.FieldRenames,
		coverage.ScalarMappings,
		coverage.PresenceMappings,
		coverage.Report.Predecessors,
		coverage.Report.WireFields,
		coverage.Report.NamedScalars,
		coverage.Classification,
		&issues,
	)
	if err := CheckDeclarationClassifications(
		DeclarationReport{
			NamedScalars: coverage.Report.NamedScalars,
			Validators:   coverage.Report.Validators,
		},
		coverage.Classification,
	); err != nil {
		issues = append(issues, CoverageIssue{
			Code:   IssueCoverageDeclaration,
			Detail: err.Error(),
		})
	}
	checkAggregateFingerprint(
		"exceptional wire coverage",
		coverage.ExceptionalFingerprint,
		fingerprintWireExceptions(coverage.WireExceptions),
		&issues,
	)
	checkFocusedProjectionFixtures(coverage.FocusedFixtures, &issues)
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		return issues[left].Detail < issues[right].Detail
	})
	return &CoverageError{Issues: issues}
}

func checkAggregateFingerprint(
	name string,
	want string,
	got string,
	issues *[]CoverageIssue,
) {
	if want == "" || want != got {
		*issues = append(*issues, CoverageIssue{
			Code:   IssueCoverageDeclaration,
			Detail: fmt.Sprintf("%s fingerprint = %s, want %s", name, got, want),
		})
	}
}

func fingerprintWireExceptions(exceptions []WireException) string {
	sorted := append([]WireException(nil), exceptions...)
	sort.Slice(sorted, func(left, right int) bool {
		leftKey := legacySemanticTypePath(sorted[left].LegacyType) + "." + string(sorted[left].Message)
		rightKey := legacySemanticTypePath(sorted[right].LegacyType) + "." + string(sorted[right].Message)
		return leftKey < rightKey
	})
	var canonical strings.Builder
	for _, exception := range sorted {
		fmt.Fprintf(
			&canonical,
			"%s\t%s\t%s\t%s\n",
			legacySemanticTypePath(exception.LegacyType),
			exception.Message,
			exception.LegacyFingerprint,
			exception.DescriptorFingerprint,
		)
	}
	return fingerprintText(canonical.String())
}

func checkAssociatedClosedEnumCoverage(
	classification DeclarationClassification,
	namedScalars []NamedScalar,
	associations map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor,
	issues *[]CoverageIssue,
) {
	scalarsByIdentity := make(map[Identity]NamedScalar, len(namedScalars))
	for _, scalar := range namedScalars {
		scalarsByIdentity[scalar.Identity] = scalar
	}
	for _, scalar := range classification.Scalars {
		if scalar.Kind != ScalarClosedStringEnum {
			continue
		}
		discovered, exists := scalarsByIdentity[scalar.Identity]
		if !exists {
			continue
		}
		wanted := make(map[protoreflect.Name]struct{}, len(scalar.EnumMembers))
		for _, member := range scalar.EnumMembers {
			wanted[protoreflect.Name(member.DescriptorName)] = struct{}{}
		}
		associated := associations[discovered.Type]
		if len(associated) == 0 {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageDeclaration,
				Detail: fmt.Sprintf("%s has no route-reachable descriptor enum association", scalar.Identity),
			})
			continue
		}
		for name, descriptor := range associated {
			actual := enumMemberSet(descriptor)
			if !equalEnumMemberSets(wanted, actual) {
				*issues = append(*issues, CoverageIssue{
					Code: IssueCoverageDeclaration,
					Detail: fmt.Sprintf(
						"%s associated with %s has a different member set",
						scalar.Identity,
						name,
					),
				})
			}
		}
	}
}

func enumMemberSet(enum protoreflect.EnumDescriptor) map[protoreflect.Name]struct{} {
	result := make(map[protoreflect.Name]struct{})
	for index := 0; index < enum.Values().Len(); index++ {
		value := enum.Values().Get(index)
		if !isUnspecifiedEnumValue(value) {
			result[value.Name()] = struct{}{}
		}
	}
	return result
}

func collectMessageEnumMemberSets(
	messages protoreflect.MessageDescriptors,
	sets map[protoreflect.FullName]map[protoreflect.Name]struct{},
) {
	for index := 0; index < messages.Len(); index++ {
		message := messages.Get(index)
		collectEnumMemberSets(message.Enums(), sets)
		collectMessageEnumMemberSets(message.Messages(), sets)
	}
}

func collectEnumMemberSets(
	enums protoreflect.EnumDescriptors,
	sets map[protoreflect.FullName]map[protoreflect.Name]struct{},
) {
	for enumIndex := 0; enumIndex < enums.Len(); enumIndex++ {
		enum := enums.Get(enumIndex)
		members := make(map[protoreflect.Name]struct{})
		for valueIndex := 0; valueIndex < enum.Values().Len(); valueIndex++ {
			value := enum.Values().Get(valueIndex)
			if !isUnspecifiedEnumValue(value) {
				members[value.Name()] = struct{}{}
			}
		}
		sets[enum.FullName()] = members
	}
}

func equalEnumMemberSets(
	left map[protoreflect.Name]struct{},
	right map[protoreflect.Name]struct{},
) bool {
	if len(left) != len(right) {
		return false
	}
	for name := range left {
		if _, exists := right[name]; !exists {
			return false
		}
	}
	return true
}

func isUnspecifiedEnumValue(value protoreflect.EnumValueDescriptor) bool {
	name := string(value.Name())
	const suffix = "_UNSPECIFIED"
	if len(name) < len(suffix) {
		return false
	}
	for index := range suffix {
		if name[len(name)-len(suffix)+index] != suffix[index] {
			return false
		}
	}
	return true
}

type wireExceptionKey struct {
	LegacyType reflect.Type
	Message    protoreflect.FullName
}

type wireExceptionIndex map[wireExceptionKey]WireException

func checkCoverageWireShapes(
	routes []apicontract.Route,
	operations []protoapi.Operation,
	exceptions []WireException,
	fieldRenames []WireFieldRename,
	scalarMappings []WireScalarMapping,
	presenceMappings []WirePresenceMapping,
	predecessors []ResolvedIdentity,
	wireFields map[WireFieldObjectKey]*types.Var,
	namedScalars []NamedScalar,
	classification DeclarationClassification,
	issues *[]CoverageIssue,
) {
	exceptionIndex := make(wireExceptionIndex, len(exceptions))
	for _, exception := range exceptions {
		legacyType := dereferenceType(exception.LegacyType)
		if legacyType == nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "exception has no legacy type",
			})
			continue
		}
		if exception.Message == "" {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "exception has no descriptor message for " + legacySemanticTypePath(legacyType),
			})
			continue
		}
		key := wireExceptionKey{LegacyType: legacyType, Message: exception.Message}
		if _, exists := exceptionIndex[key]; exists {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "duplicate " + legacySemanticTypePath(legacyType) + " -> " + string(exception.Message),
			})
			continue
		}
		exceptionIndex[key] = exception
	}

	operationsByLegacyName := make(map[string]protoapi.Operation, len(operations))
	for _, operation := range operations {
		if operation.Descriptor.ParentFile().Package() == "fixture" ||
			operation.LegacyWireName == nil {
			continue
		}
		operationsByLegacyName[*operation.LegacyWireName] = operation
	}
	usedExceptions := make(map[wireExceptionKey]protoreflect.MessageDescriptor, len(exceptions))
	renameIndex := buildWireFieldRenameIndex(fieldRenames, issues)
	usedRenames := make(map[wireFieldRenameKey]struct{}, len(fieldRenames))
	scalarMappingIndex := buildWireScalarMappingIndex(scalarMappings, issues)
	usedScalarMappings := make(map[wireScalarMappingKey]struct{}, len(scalarMappings))
	presenceMappingIndex := buildWirePresenceMappingIndex(presenceMappings, issues)
	usedPresenceMappings := make(map[wirePresenceMappingKey]struct{}, len(presenceMappings))
	projectedObjects := resolvedProjectedObjects(predecessors, issues)
	scalarObjects := make(map[Identity]*types.TypeName, len(namedScalars))
	for _, scalar := range namedScalars {
		scalarObjects[scalar.Identity] = scalar.Type
	}
	enumAssociations := make(map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor)
	visited := make(map[wireTypeMessagePair]struct{})
	for _, route := range routes {
		operation, exists := operationsByLegacyName[route.Method]
		if !exists {
			continue
		}
		compareWireRoot(
			route.Method+".request",
			route.RequestType,
			operation.Descriptor.Input(),
			exceptionIndex,
			usedExceptions,
			renameIndex,
			usedRenames,
			scalarMappingIndex,
			usedScalarMappings,
			presenceMappingIndex,
			usedPresenceMappings,
			projectedObjects,
			wireFields,
			scalarObjects,
			enumAssociations,
			visited,
			issues,
		)
		if route.Kind != apicontract.KindNotification {
			compareWireRoot(
				route.Method+".response",
				route.ResponseType,
				operation.Descriptor.Output(),
				exceptionIndex,
				usedExceptions,
				renameIndex,
				usedRenames,
				scalarMappingIndex,
				usedScalarMappings,
				presenceMappingIndex,
				usedPresenceMappings,
				projectedObjects,
				wireFields,
				scalarObjects,
				enumAssociations,
				visited,
				issues,
			)
		}
		if route.EventType != nil && operation.Event != nil {
			compareWireRoot(
				route.Method+".event",
				route.EventType,
				operation.Event.Descriptor.Input(),
				exceptionIndex,
				usedExceptions,
				renameIndex,
				usedRenames,
				scalarMappingIndex,
				usedScalarMappings,
				presenceMappingIndex,
				usedPresenceMappings,
				projectedObjects,
				wireFields,
				scalarObjects,
				enumAssociations,
				visited,
				issues,
			)
		}
		if route.CompleteType != nil && operation.Completion != nil {
			compareWireRoot(
				route.Method+".completion",
				route.CompleteType,
				operation.Completion.Descriptor.Input(),
				exceptionIndex,
				usedExceptions,
				renameIndex,
				usedRenames,
				scalarMappingIndex,
				usedScalarMappings,
				presenceMappingIndex,
				usedPresenceMappings,
				projectedObjects,
				wireFields,
				scalarObjects,
				enumAssociations,
				visited,
				issues,
			)
		}
	}
	for key, exception := range exceptionIndex {
		message, used := usedExceptions[key]
		if !used {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "unused " + legacySemanticTypePath(key.LegacyType) + " -> " + string(key.Message),
			})
			continue
		}
		if err := checkWireExceptionFingerprint(key.LegacyType, message, exception); err != nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: legacySemanticTypePath(key.LegacyType) + " -> " + string(key.Message) + ": " + err.Error(),
			})
		}
	}
	for key := range renameIndex {
		if _, used := usedRenames[key]; !used {
			*issues = append(*issues, CoverageIssue{
				Code: IssueCoverageWireException,
				Detail: fmt.Sprintf(
					"unused field rename %s.%s -> %s.%s",
					legacySemanticTypePath(key.LegacyType),
					key.LegacyField,
					key.Message,
					renameIndex[key],
				),
			})
		}
	}
	for key := range scalarMappingIndex {
		if _, used := usedScalarMappings[key]; !used {
			*issues = append(*issues, CoverageIssue{
				Code: IssueCoverageWireException,
				Detail: fmt.Sprintf(
					"unused scalar mapping %s.%s",
					key.Message,
					key.Field,
				),
			})
		}
	}
	for key := range presenceMappingIndex {
		if _, used := usedPresenceMappings[key]; !used {
			*issues = append(*issues, CoverageIssue{
				Code: IssueCoverageWireException,
				Detail: fmt.Sprintf(
					"unused presence mapping %s -> %s.%s",
					legacySemanticTypePath(key.LegacyType),
					key.Message,
					key.Field,
				),
			})
		}
	}
}

type wirePresenceMappingKey struct {
	LegacyType reflect.Type
	Message    protoreflect.FullName
	Field      protoreflect.Name
}

type wirePresenceMappingIndex map[wirePresenceMappingKey]bool

func buildWirePresenceMappingIndex(
	mappings []WirePresenceMapping,
	issues *[]CoverageIssue,
) wirePresenceMappingIndex {
	result := make(wirePresenceMappingIndex, len(mappings))
	for _, mapping := range mappings {
		key := wirePresenceMappingKey{
			LegacyType: dereferenceType(mapping.LegacyType),
			Message:    mapping.Message,
			Field:      mapping.Field,
		}
		if key.LegacyType == nil || key.Message == "" || key.Field == "" {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "incomplete presence mapping classification",
			})
			continue
		}
		if _, duplicate := result[key]; duplicate {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "duplicate presence mapping " + legacySemanticTypePath(key.LegacyType),
			})
			continue
		}
		result[key] = mapping.Optional
	}
	return result
}

type wireScalarMappingKey struct {
	Message protoreflect.FullName
	Field   protoreflect.Name
}

type wireScalarMappingIndex map[wireScalarMappingKey]protoreflect.Kind

func buildWireScalarMappingIndex(
	mappings []WireScalarMapping,
	issues *[]CoverageIssue,
) wireScalarMappingIndex {
	result := make(wireScalarMappingIndex, len(mappings))
	for _, mapping := range mappings {
		key := wireScalarMappingKey{
			Message: mapping.Message,
			Field:   mapping.Field,
		}
		if key.Message == "" || key.Field == "" ||
			mapping.Kind == 0 {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "incomplete scalar mapping classification",
			})
			continue
		}
		if _, duplicate := result[key]; duplicate {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "duplicate scalar mapping " + string(key.Message) + "." + string(key.Field),
			})
			continue
		}
		result[key] = mapping.Kind
	}
	return result
}

func resolvedProjectedObjects(
	predecessors []ResolvedIdentity,
	issues *[]CoverageIssue,
) map[types.Object]Identity {
	result := make(map[types.Object]Identity, len(predecessors))
	for _, predecessor := range predecessors {
		if predecessor.Object == nil {
			continue
		}
		if previous, duplicate := result[predecessor.Object]; duplicate {
			*issues = append(*issues, CoverageIssue{
				Code: IssueCoveragePredecessorDuplicate,
				Detail: fmt.Sprintf(
					"go/types object resolves both %s and %s",
					previous,
					predecessor.Identity,
				),
			})
			continue
		}
		result[predecessor.Object] = predecessor.Identity
	}
	return result
}

type wireFieldRenameKey struct {
	LegacyType  reflect.Type
	Message     protoreflect.FullName
	LegacyField string
}

type wireFieldRenameIndex map[wireFieldRenameKey]protoreflect.Name

func buildWireFieldRenameIndex(
	renames []WireFieldRename,
	issues *[]CoverageIssue,
) wireFieldRenameIndex {
	result := make(wireFieldRenameIndex, len(renames))
	for _, rename := range renames {
		legacyType := dereferenceType(rename.LegacyType)
		key := wireFieldRenameKey{
			LegacyType:  legacyType,
			Message:     rename.Message,
			LegacyField: rename.LegacyField,
		}
		if legacyType == nil || rename.Message == "" ||
			rename.LegacyField == "" || rename.DescriptorField == "" {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "incomplete field rename classification",
			})
			continue
		}
		if _, duplicate := result[key]; duplicate {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireException,
				Detail: "duplicate field rename " + legacySemanticTypePath(legacyType) + "." + rename.LegacyField,
			})
			continue
		}
		result[key] = rename.DescriptorField
	}
	return result
}

func checkWireExceptionFingerprint(
	legacyType reflect.Type,
	message protoreflect.MessageDescriptor,
	exception WireException,
) error {
	legacyFingerprint := fingerprintExceptionalLegacyType(legacyType)
	descriptorFingerprint := fingerprintExceptionalDescriptor(message)
	if exception.LegacyFingerprint == "" || exception.DescriptorFingerprint == "" {
		return fmt.Errorf(
			"focused fixture fingerprints are required; legacy=%s descriptor=%s",
			legacyFingerprint,
			descriptorFingerprint,
		)
	}
	if exception.LegacyFingerprint != legacyFingerprint {
		return fmt.Errorf(
			"legacy focused fixture changed: got %s, want %s",
			legacyFingerprint,
			exception.LegacyFingerprint,
		)
	}
	if exception.DescriptorFingerprint != descriptorFingerprint {
		return fmt.Errorf(
			"descriptor focused fixture changed: got %s, want %s",
			descriptorFingerprint,
			exception.DescriptorFingerprint,
		)
	}
	return nil
}

func fingerprintExceptionalLegacyType(legacyType reflect.Type) string {
	var canonical strings.Builder
	fmt.Fprintf(&canonical, "type\t%s\t%s\n", legacySemanticTypePath(legacyType), legacyType.Kind())
	for index := 0; index < legacyType.NumField(); index++ {
		field := legacyType.Field(index)
		fmt.Fprintf(
			&canonical,
			"field\t%s\t%s\t%s\t%s\n",
			field.Name,
			field.Type,
			field.PkgPath,
			field.Tag.Get("json"),
		)
	}
	fmt.Fprintf(
		&canonical,
		"wire\t%t\t%t\n",
		legacyType.Implements(reflect.TypeFor[json.Marshaler]()) ||
			reflect.PointerTo(legacyType).Implements(reflect.TypeFor[json.Marshaler]()),
		legacyType.Implements(reflect.TypeFor[json.Unmarshaler]()) ||
			reflect.PointerTo(legacyType).Implements(reflect.TypeFor[json.Unmarshaler]()),
	)
	return fingerprintText(canonical.String())
}

func fingerprintExceptionalDescriptor(message protoreflect.MessageDescriptor) string {
	var canonical strings.Builder
	fmt.Fprintf(&canonical, "message\t%s\n", message.FullName())
	for index := 0; index < message.Oneofs().Len(); index++ {
		oneof := message.Oneofs().Get(index)
		fmt.Fprintf(&canonical, "oneof\t%s\t%t\n", oneof.Name(), oneof.IsSynthetic())
	}
	for index := 0; index < message.Fields().Len(); index++ {
		field := message.Fields().Get(index)
		oneof := protoreflect.Name("")
		if field.ContainingOneof() != nil {
			oneof = field.ContainingOneof().Name()
		}
		options, err := proto.MarshalOptions{Deterministic: true}.Marshal(field.Options())
		if err != nil {
			panic(fmt.Sprintf("marshal descriptor options for %s: %v", field.FullName(), err))
		}
		fmt.Fprintf(
			&canonical,
			"field\t%s\t%d\t%s\t%s\t%t\t%t\t%t\t%s\t%x\n",
			field.Name(),
			field.Number(),
			field.Kind(),
			field.Cardinality(),
			field.HasPresence(),
			field.IsList(),
			field.IsMap(),
			oneof,
			options,
		)
	}
	return fingerprintText(canonical.String())
}

func fingerprintText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type wireTypeMessagePair struct {
	legacyType reflect.Type
	message    protoreflect.FullName
}

func compareWireRoot(
	path string,
	legacyType reflect.Type,
	message protoreflect.MessageDescriptor,
	exceptions wireExceptionIndex,
	usedExceptions map[wireExceptionKey]protoreflect.MessageDescriptor,
	renames wireFieldRenameIndex,
	usedRenames map[wireFieldRenameKey]struct{},
	scalarMappings wireScalarMappingIndex,
	usedScalarMappings map[wireScalarMappingKey]struct{},
	presenceMappings wirePresenceMappingIndex,
	usedPresenceMappings map[wirePresenceMappingKey]struct{},
	projectedObjects map[types.Object]Identity,
	wireFields map[WireFieldObjectKey]*types.Var,
	scalarObjects map[Identity]*types.TypeName,
	enumAssociations map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor,
	visited map[wireTypeMessagePair]struct{},
	issues *[]CoverageIssue,
) {
	legacyType = dereferenceType(legacyType)
	if legacyType == nil || message == nil {
		*issues = append(*issues, CoverageIssue{
			Code:   IssueCoverageWireShape,
			Detail: path + ": missing legacy or descriptor root",
		})
		return
	}
	key := wireExceptionKey{LegacyType: legacyType, Message: message.FullName()}
	if _, exceptional := exceptions[key]; exceptional {
		usedExceptions[key] = message
		return
	}
	compareWireMessage(path, legacyType, unwrapResultSuccess(message), exceptions, usedExceptions, renames, usedRenames, scalarMappings, usedScalarMappings, presenceMappings, usedPresenceMappings, projectedObjects, wireFields, scalarObjects, enumAssociations, visited, issues)
}

func unwrapResultSuccess(message protoreflect.MessageDescriptor) protoreflect.MessageDescriptor {
	outcome := message.Oneofs().ByName("outcome")
	if outcome == nil {
		return message
	}
	success := message.Fields().ByName("success")
	if success == nil || success.Message() == nil {
		return message
	}
	return success.Message()
}

func compareWireMessage(
	path string,
	legacyType reflect.Type,
	message protoreflect.MessageDescriptor,
	exceptions wireExceptionIndex,
	usedExceptions map[wireExceptionKey]protoreflect.MessageDescriptor,
	renames wireFieldRenameIndex,
	usedRenames map[wireFieldRenameKey]struct{},
	scalarMappings wireScalarMappingIndex,
	usedScalarMappings map[wireScalarMappingKey]struct{},
	presenceMappings wirePresenceMappingIndex,
	usedPresenceMappings map[wirePresenceMappingKey]struct{},
	projectedObjects map[types.Object]Identity,
	wireFields map[WireFieldObjectKey]*types.Var,
	scalarObjects map[Identity]*types.TypeName,
	enumAssociations map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor,
	visited map[wireTypeMessagePair]struct{},
	issues *[]CoverageIssue,
) {
	legacyType = dereferenceType(legacyType)
	if legacyType == nil || message == nil {
		*issues = append(*issues, CoverageIssue{Code: IssueCoverageWireShape, Detail: path + ": missing nested type"})
		return
	}
	key := wireExceptionKey{LegacyType: legacyType, Message: message.FullName()}
	if _, exceptional := exceptions[key]; exceptional {
		usedExceptions[key] = message
		return
	}
	pair := wireTypeMessagePair{legacyType: legacyType, message: message.FullName()}
	if _, exists := visited[pair]; exists {
		return
	}
	visited[pair] = struct{}{}
	if legacyType.Kind() != reflect.Struct {
		*issues = append(*issues, CoverageIssue{
			Code:   IssueCoverageWireShape,
			Detail: fmt.Sprintf("%s: %s is not a message-shaped Go type", path, legacySemanticTypePath(legacyType)),
		})
		return
	}

	descriptorFields := make(map[protoreflect.Name]protoreflect.FieldDescriptor, message.Fields().Len())
	for index := 0; index < message.Fields().Len(); index++ {
		field := message.Fields().Get(index)
		descriptorFields[field.Name()] = field
	}
	for index := 0; index < legacyType.NumField(); index++ {
		field := legacyType.Field(index)
		if field.PkgPath != "" || field.Tag.Get("json") == "-" {
			continue
		}
		objectKey := WireFieldObjectKey{
			PackagePath: legacyType.PkgPath(),
			TypeName:    reflectedDeclarationName(legacyType.Name()),
			FieldName:   field.Name,
		}
		fieldObject := wireFields[objectKey]
		if fieldObject == nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoveragePredecessorUnresolved,
				Detail: fmt.Sprintf("wire field go/types object does not resolve: %s.%s.%s", objectKey.PackagePath, objectKey.TypeName, objectKey.FieldName),
			})
			continue
		}
		if _, projected := projectedObjects[fieldObject]; projected {
			descriptorName, err := protoapi.PascalCaseToLowerSnake(field.Name)
			if err == nil {
				if authored := descriptorFields[protoreflect.Name(descriptorName)]; authored != nil {
					*issues = append(*issues, CoverageIssue{
						Code:   IssueCoverageProjectedWireFact,
						Detail: path + "." + field.Name + " -> " + string(authored.FullName()),
					})
				}
			}
			continue
		}
		descriptorName, err := associatedDescriptorFieldName(
			legacyType,
			message,
			field,
			descriptorFields,
			renames,
			usedRenames,
		)
		if err != nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireShape,
				Detail: path + "." + field.Name + ": " + err.Error(),
			})
			continue
		}
		descriptorField := descriptorFields[descriptorName]
		if descriptorField == nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireShape,
				Detail: path + "." + field.Name + " (" + legacySemanticTypePath(legacyType) + ") missing " + string(message.FullName()) + "." + string(descriptorName),
			})
			continue
		}
		delete(descriptorFields, descriptorName)
		if jsonName, explicit := explicitLegacyJSONName(field); explicit &&
			jsonName != string(descriptorField.Name()) {
			renameKey := wireFieldRenameKey{
				LegacyType:  legacyType,
				Message:     message.FullName(),
				LegacyField: field.Name,
			}
			if renamed, classified := renames[renameKey]; !classified || renamed != descriptorField.Name() {
				*issues = append(*issues, CoverageIssue{
					Code: IssueCoverageWireShape,
					Detail: fmt.Sprintf(
						"%s.%s: legacy JSON name %q does not match %s without an exact rename classification",
						path,
						field.Name,
						jsonName,
						descriptorField.FullName(),
					),
				})
			}
		}
		compareWireField(path+"."+field.Name, legacyType, field.Type, descriptorField, exceptions, usedExceptions, renames, usedRenames, scalarMappings, usedScalarMappings, presenceMappings, usedPresenceMappings, projectedObjects, wireFields, scalarObjects, enumAssociations, visited, issues)
	}
	for _, field := range descriptorFields {
		*issues = append(*issues, CoverageIssue{
			Code:   IssueCoverageWireShape,
			Detail: path + " (" + legacySemanticTypePath(legacyType) + "): unexpected " + string(field.FullName()),
		})
	}
}

func associatedDescriptorFieldName(
	legacyType reflect.Type,
	message protoreflect.MessageDescriptor,
	field reflect.StructField,
	descriptorFields map[protoreflect.Name]protoreflect.FieldDescriptor,
	renames wireFieldRenameIndex,
	usedRenames map[wireFieldRenameKey]struct{},
) (protoreflect.Name, error) {
	derived, err := protoapi.PascalCaseToLowerSnake(field.Name)
	if err != nil {
		return "", err
	}
	if _, exists := descriptorFields[protoreflect.Name(derived)]; exists {
		return protoreflect.Name(derived), nil
	}
	key := wireFieldRenameKey{
		LegacyType:  legacyType,
		Message:     message.FullName(),
		LegacyField: field.Name,
	}
	if renamed, exists := renames[key]; exists {
		usedRenames[key] = struct{}{}
		return renamed, nil
	}
	return protoreflect.Name(derived), nil
}

func explicitLegacyJSONName(field reflect.StructField) (string, bool) {
	tag, exists := field.Tag.Lookup("json")
	if !exists {
		return "", false
	}
	end := 0
	for end < len(tag) && tag[end] != ',' {
		end++
	}
	if end == 0 || tag[:end] == "-" {
		return "", false
	}
	return tag[:end], true
}

func compareWireField(
	path string,
	legacyOwner reflect.Type,
	legacyType reflect.Type,
	field protoreflect.FieldDescriptor,
	exceptions wireExceptionIndex,
	usedExceptions map[wireExceptionKey]protoreflect.MessageDescriptor,
	renames wireFieldRenameIndex,
	usedRenames map[wireFieldRenameKey]struct{},
	scalarMappings wireScalarMappingIndex,
	usedScalarMappings map[wireScalarMappingKey]struct{},
	presenceMappings wirePresenceMappingIndex,
	usedPresenceMappings map[wirePresenceMappingKey]struct{},
	projectedObjects map[types.Object]Identity,
	wireFields map[WireFieldObjectKey]*types.Var,
	scalarObjects map[Identity]*types.TypeName,
	enumAssociations map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor,
	visited map[wireTypeMessagePair]struct{},
	issues *[]CoverageIssue,
) {
	legacyPresence := legacyType.Kind() == reflect.Pointer
	presenceKey := wirePresenceMappingKey{
		LegacyType: legacyOwner,
		Message:    field.ContainingMessage().FullName(),
		Field:      field.Name(),
	}
	if mappedPresence, exists := presenceMappings[presenceKey]; exists {
		usedPresenceMappings[presenceKey] = struct{}{}
		legacyPresence = mappedPresence
	}
	legacyType = dereferenceType(legacyType)
	if descriptorFieldOptional(field) != legacyPresence &&
		!field.IsList() &&
		!field.IsMap() {
		*issues = append(*issues, CoverageIssue{
			Code: IssueCoverageWireShape,
			Detail: fmt.Sprintf(
				"%s: presence mismatch (%s -> %s.%s)",
				path,
				legacySemanticTypePath(legacyOwner),
				field.ContainingMessage().FullName(),
				field.Name(),
			),
		})
	}
	if legacyType.Kind() == reflect.Slice &&
		legacyType.Elem().Kind() != reflect.Uint8 &&
		!marshalsAsText(legacyType) {
		if !field.IsList() {
			*issues = append(*issues, CoverageIssue{Code: IssueCoverageWireShape, Detail: path + ": repeated mismatch"})
			return
		}
		legacyType = dereferenceType(legacyType.Elem())
	} else if field.IsList() {
		*issues = append(*issues, CoverageIssue{Code: IssueCoverageWireShape, Detail: path + ": unexpected repeated field"})
		return
	}
	if legacyType.Kind() == reflect.Map {
		if !field.IsMap() {
			*issues = append(*issues, CoverageIssue{Code: IssueCoverageWireShape, Detail: path + ": map mismatch"})
			return
		}
		if !wireScalarCompatible(legacyType.Key(), field.MapKey(), scalarMappings, usedScalarMappings) {
			*issues = append(*issues, CoverageIssue{Code: IssueCoverageWireShape, Detail: path + ": map key mismatch"})
		}
		compareWireValue(path+"[]", legacyType.Elem(), field.MapValue(), exceptions, usedExceptions, renames, usedRenames, scalarMappings, usedScalarMappings, presenceMappings, usedPresenceMappings, projectedObjects, wireFields, scalarObjects, enumAssociations, visited, issues)
		return
	}
	if field.IsMap() {
		*issues = append(*issues, CoverageIssue{Code: IssueCoverageWireShape, Detail: path + ": unexpected map field"})
		return
	}
	compareWireValue(path, legacyType, field, exceptions, usedExceptions, renames, usedRenames, scalarMappings, usedScalarMappings, presenceMappings, usedPresenceMappings, projectedObjects, wireFields, scalarObjects, enumAssociations, visited, issues)
}

func descriptorFieldOptional(field protoreflect.FieldDescriptor) bool {
	if field.IsList() || field.IsMap() {
		return false
	}
	if field.HasOptionalKeyword() || field.ContainingOneof() != nil {
		return true
	}
	options := field.Options()
	if !proto.HasExtension(options, validate.E_Field) {
		return false
	}
	rules, ok := proto.GetExtension(options, validate.E_Field).(*validate.FieldRules)
	return ok && !rules.GetRequired() && field.Message() != nil
}

func compareWireValue(
	path string,
	legacyType reflect.Type,
	field protoreflect.FieldDescriptor,
	exceptions wireExceptionIndex,
	usedExceptions map[wireExceptionKey]protoreflect.MessageDescriptor,
	renames wireFieldRenameIndex,
	usedRenames map[wireFieldRenameKey]struct{},
	scalarMappings wireScalarMappingIndex,
	usedScalarMappings map[wireScalarMappingKey]struct{},
	presenceMappings wirePresenceMappingIndex,
	usedPresenceMappings map[wirePresenceMappingKey]struct{},
	projectedObjects map[types.Object]Identity,
	wireFields map[WireFieldObjectKey]*types.Var,
	scalarObjects map[Identity]*types.TypeName,
	enumAssociations map[*types.TypeName]map[protoreflect.FullName]protoreflect.EnumDescriptor,
	visited map[wireTypeMessagePair]struct{},
	issues *[]CoverageIssue,
) {
	legacyType = dereferenceType(legacyType)
	if field.Message() != nil {
		key := wireExceptionKey{LegacyType: legacyType, Message: field.Message().FullName()}
		if _, exceptional := exceptions[key]; exceptional {
			usedExceptions[key] = field.Message()
			return
		}
		if isApprovedWellKnownMapping(legacyType, field.Message().FullName()) {
			return
		}
		if scalarWireType(legacyType) {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageWireShape,
				Detail: fmt.Sprintf("%s: Go %s is incompatible with Protobuf %s", path, legacyType, field.Message().FullName()),
			})
			return
		}
		compareWireMessage(path, legacyType, field.Message(), exceptions, usedExceptions, renames, usedRenames, scalarMappings, usedScalarMappings, presenceMappings, usedPresenceMappings, projectedObjects, wireFields, scalarObjects, enumAssociations, visited, issues)
		return
	}
	if field.Enum() != nil && legacyType.PkgPath() != "" && legacyType.Name() != "" {
		identity := typeIdentity(legacyType.PkgPath(), reflectedDeclarationName(legacyType.Name()))
		if scalar := scalarObjects[identity]; scalar != nil {
			if enumAssociations[scalar] == nil {
				enumAssociations[scalar] = make(map[protoreflect.FullName]protoreflect.EnumDescriptor)
			}
			enumAssociations[scalar][field.Enum().FullName()] = field.Enum()
		}
	}
	if !wireScalarCompatible(legacyType, field, scalarMappings, usedScalarMappings) {
		*issues = append(*issues, CoverageIssue{
			Code: IssueCoverageWireShape,
			Detail: fmt.Sprintf(
				"%s: Go %s is incompatible with Protobuf %s (%s.%s)",
				path,
				legacyType,
				field.Kind(),
				field.ContainingMessage().FullName(),
				field.Name(),
			),
		})
	}
}

func wireScalarCompatible(
	legacyType reflect.Type,
	field protoreflect.FieldDescriptor,
	mappings wireScalarMappingIndex,
	usedMappings map[wireScalarMappingKey]struct{},
) bool {
	legacyType = dereferenceType(legacyType)
	if marshalsAsText(legacyType) {
		return field.Kind() == protoreflect.StringKind
	}
	key := wireScalarMappingKey{
		Message: field.ContainingMessage().FullName(),
		Field:   field.Name(),
	}
	if mappedKind, exists := mappings[key]; exists {
		usedMappings[key] = struct{}{}
		return field.Kind() == mappedKind
	}
	switch legacyType.Kind() {
	case reflect.Bool:
		return field.Kind() == protoreflect.BoolKind
	case reflect.Int:
		return field.Kind() == protoreflect.Int64Kind
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return field.Kind() == protoreflect.Int32Kind ||
			field.Kind() == protoreflect.Sint32Kind ||
			field.Kind() == protoreflect.Sfixed32Kind ||
			field.Kind() == protoreflect.EnumKind
	case reflect.Int64:
		return field.Kind() == protoreflect.Int64Kind ||
			field.Kind() == protoreflect.Sint64Kind ||
			field.Kind() == protoreflect.Sfixed64Kind
	case reflect.Uint:
		return field.Kind() == protoreflect.Uint64Kind
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return field.Kind() == protoreflect.Uint32Kind ||
			field.Kind() == protoreflect.Fixed32Kind ||
			field.Kind() == protoreflect.EnumKind
	case reflect.Uint64:
		return field.Kind() == protoreflect.Uint64Kind ||
			field.Kind() == protoreflect.Fixed64Kind
	case reflect.Float32:
		return field.Kind() == protoreflect.FloatKind
	case reflect.Float64:
		return field.Kind() == protoreflect.DoubleKind
	case reflect.String:
		return field.Kind() == protoreflect.StringKind || field.Kind() == protoreflect.EnumKind
	case reflect.Slice:
		return legacyType.Elem().Kind() == reflect.Uint8 && field.Kind() == protoreflect.BytesKind
	default:
		return false
	}
}

func scalarWireType(legacyType reflect.Type) bool {
	legacyType = dereferenceType(legacyType)
	return marshalsAsText(legacyType) || legacyType.Kind() != reflect.Struct
}

func marshalsAsText(legacyType reflect.Type) bool {
	legacyType = dereferenceType(legacyType)
	textMarshaler := reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshaler := reflect.TypeFor[json.Marshaler]()
	return legacyType.Implements(textMarshaler) ||
		reflect.PointerTo(legacyType).Implements(textMarshaler) ||
		legacyType.Implements(jsonMarshaler) ||
		reflect.PointerTo(legacyType).Implements(jsonMarshaler)
}

func isApprovedWellKnownMapping(legacyType reflect.Type, message protoreflect.FullName) bool {
	if legacyType.PkgPath() == "time" && legacyType.Name() == "Time" {
		return message == "google.protobuf.Timestamp"
	}
	if legacyType.PkgPath() == "time" && legacyType.Name() == "Duration" {
		return message == "google.protobuf.Duration"
	}
	return legacyType.Kind() == reflect.Struct &&
		legacyType.NumField() == 0 &&
		message == "google.protobuf.Empty"
}

func isProjectedIdentity(identity Identity) bool {
	_, exists := identitySet(lockedPredecessorIdentities)[identity]
	return exists
}

func projectedTypeIdentity(legacyType reflect.Type) (Identity, bool) {
	if legacyType.PkgPath() == "" || legacyType.Name() == "" {
		return Identity{}, false
	}
	identity := typeIdentity(legacyType.PkgPath(), reflectedDeclarationName(legacyType.Name()))
	return identity, isProjectedIdentity(identity)
}

func checkCoveragePredecessors(predecessors []ResolvedIdentity, issues *[]CoverageIssue) {
	want := identitySet(lockedPredecessorIdentities)
	seen := make(map[Identity]int, len(predecessors))
	for _, predecessor := range predecessors {
		seen[predecessor.Identity]++
		if predecessor.Object == nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoveragePredecessorUnresolved,
				Detail: predecessor.Identity.String(),
			})
		}
	}
	for identity, count := range seen {
		if count != 1 {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoveragePredecessorDuplicate,
				Detail: fmt.Sprintf("%s resolved %d times", identity, count),
			})
		}
		if _, exists := want[identity]; !exists {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoveragePredecessorSet,
				Detail: "unexpected " + identity.String(),
			})
		}
	}
	for identity := range want {
		if seen[identity] == 0 {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoveragePredecessorSet,
				Detail: "missing " + identity.String(),
			})
		}
	}
}

type operationDescriptorSlice []OperationDescriptor

func (descriptors operationDescriptorSlice) OperationDescriptors() []OperationDescriptor {
	return append([]OperationDescriptor(nil), descriptors...)
}

func checkCoverageOperations(
	routes []apicontract.Route,
	operations []protoapi.Operation,
	issues *[]CoverageIssue,
) {
	descriptors := make(operationDescriptorSlice, 0, len(operations))
	operationsByLegacyName := make(map[string]protoapi.Operation, len(operations))
	for _, operation := range operations {
		if operation.Descriptor.ParentFile().Package() == "fixture" {
			continue
		}
		if operation.LegacyWireName != nil {
			operationsByLegacyName[*operation.LegacyWireName] = operation
		}
		kind, err := legacyOperationKind(operation.Options.Kind)
		if err != nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageRouteMetadata,
				Detail: fmt.Sprintf("%s: %v", operation.Descriptor.FullName(), err),
			})
			continue
		}
		descriptors = append(descriptors, OperationDescriptor{
			Package:        string(operation.Descriptor.ParentFile().Package()),
			Service:        string(operation.Descriptor.Parent().Name()),
			Method:         string(operation.Descriptor.Name()),
			LegacyWireName: operation.LegacyWireName,
			Kind:           kind,
			Event:          operationAssociationRef(operation.Event),
			Completion:     operationAssociationRef(operation.Completion),
		})
	}
	if err := CheckOperationAssociations(routes, descriptors, nil); err != nil {
		*issues = append(*issues, CoverageIssue{
			Code:   IssueCoverageRouteAssociation,
			Detail: err.Error(),
		})
	}
	for _, route := range routes {
		operation, exists := operationsByLegacyName[route.Method]
		if !exists {
			continue
		}
		checkRouteMetadata(route, operation, issues)
	}
}

func checkRouteMetadata(route apicontract.Route, operation protoapi.Operation, issues *[]CoverageIssue) {
	options := operation.Options
	if options.AuthenticationStage != generatedAuthenticationStage(route.Auth) {
		addRouteMetadataIssue(route, "authentication stage", issues)
	}
	if options.ScopePolicy != generatedScopePolicy(route.Scope) {
		addRouteMetadataIssue(route, "scope policy", issues)
	}
	if route.Kind == apicontract.KindUnary {
		if options.UnaryConnection != generatedUnaryConnection(route.Connection) {
			addRouteMetadataIssue(route, "unary connection", issues)
		}
	} else if options.UnaryConnection != sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED {
		addRouteMetadataIssue(route, "non-unary connection", issues)
	}
	if operation.Descriptor.Input() == nil || operation.Descriptor.Output() == nil {
		addRouteMetadataIssue(route, "request/result wire descriptor", issues)
	}
}

func addRouteMetadataIssue(route apicontract.Route, fact string, issues *[]CoverageIssue) {
	*issues = append(*issues, CoverageIssue{
		Code:   IssueCoverageRouteMetadata,
		Detail: route.Method + ": " + fact,
	})
}

func legacyOperationKind(kind sharedpb.OperationKind) (apicontract.Kind, error) {
	switch kind {
	case sharedpb.OperationKind_OPERATION_KIND_UNARY:
		return apicontract.KindUnary, nil
	case sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION:
		return apicontract.KindSubscription, nil
	case sharedpb.OperationKind_OPERATION_KIND_PROGRESS:
		return apicontract.KindProgress, nil
	case sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION:
		return apicontract.KindNotification, nil
	default:
		return "", fmt.Errorf("operation kind %d is invalid", kind)
	}
}

func operationAssociationRef(association *protoapi.OperationAssociation) *OperationReference {
	if association == nil {
		return nil
	}
	method := association.Descriptor
	return &OperationReference{
		Package: string(method.ParentFile().Package()),
		Service: string(method.Parent().Name()),
		Method:  string(method.Name()),
	}
}

func generatedAuthenticationStage(auth apicontract.AuthPolicy) sharedpb.AuthenticationStage {
	switch auth {
	case apicontract.AuthNone:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_NONE
	case apicontract.AuthPreServerAuth:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_PRE_SERVER
	case apicontract.AuthServer:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER
	default:
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_UNSPECIFIED
	}
}

func generatedScopePolicy(scope apicontract.ScopePolicy) sharedpb.ScopePolicy {
	switch scope {
	case apicontract.ScopeNone:
		return sharedpb.ScopePolicy_SCOPE_POLICY_NONE
	case apicontract.ScopeAttachProject:
		return sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_PROJECT
	case apicontract.ScopeAttachSession:
		return sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_SESSION
	case apicontract.ScopeProjectView:
		return sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_VIEW
	case apicontract.ScopeProjectWorkspace:
		return sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_WORKSPACE
	case apicontract.ScopeProjectWorkspaceBinding:
		return sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_WORKSPACE_BINDING
	case apicontract.ScopeSessionActiveProject:
		return sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT
	case apicontract.ScopeSessionActiveProjectIfSet:
		return sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ACTIVE_PROJECT_IF_SET
	case apicontract.ScopeSessionAttachedProject:
		return sharedpb.ScopePolicy_SCOPE_POLICY_SESSION_ATTACHED_PROJECT
	case apicontract.ScopeAttachedSession:
		return sharedpb.ScopePolicy_SCOPE_POLICY_ATTACHED_SESSION
	case apicontract.ScopeGoalSession:
		return sharedpb.ScopePolicy_SCOPE_POLICY_GOAL_SESSION
	case apicontract.ScopeRuntimeLiveSessionRequired:
		return sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_REQUIRED
	case apicontract.ScopeRuntimeLiveSessionOptional:
		return sharedpb.ScopePolicy_SCOPE_POLICY_RUNTIME_LIVE_SESSION_OPTIONAL
	case apicontract.ScopeProcessActiveProject:
		return sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_ACTIVE_PROJECT
	case apicontract.ScopeProcessListActiveProject:
		return sharedpb.ScopePolicy_SCOPE_POLICY_PROCESS_LIST_ACTIVE_PROJECT
	case apicontract.ScopeNotification:
		return sharedpb.ScopePolicy_SCOPE_POLICY_NOTIFICATION
	default:
		return sharedpb.ScopePolicy_SCOPE_POLICY_UNSPECIFIED
	}
}

func generatedUnaryConnection(connection apicontract.ConnectionStrategy) sharedpb.UnaryConnection {
	switch connection {
	case apicontract.ConnectionControl, apicontract.ConnectionUnscoped:
		return sharedpb.UnaryConnection_UNARY_CONNECTION_MULTIPLEXED
	case apicontract.ConnectionDedicated:
		return sharedpb.UnaryConnection_UNARY_CONNECTION_DEDICATED
	default:
		return sharedpb.UnaryConnection_UNARY_CONNECTION_UNSPECIFIED
	}
}

func checkFocusedProjectionFixtures(fixtures []FocusedProjectionFixture, issues *[]CoverageIssue) {
	want := requiredFocusedProjectionFixtures()
	seen := make(map[FocusedProjectionFixtureName]int, len(fixtures))
	for _, fixture := range fixtures {
		seen[fixture.Name]++
		if fixture.Check == nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageFocusedFixture,
				Detail: "missing behavior check for " + string(fixture.Name),
			})
			continue
		}
		if err := fixture.Check(); err != nil {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageFocusedFixture,
				Detail: string(fixture.Name) + ": " + err.Error(),
			})
		}
	}
	for fixture, count := range seen {
		if _, exists := want[fixture]; !exists {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageFocusedFixture,
				Detail: "unexpected " + string(fixture),
			})
			continue
		}
		if count != 1 {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageFocusedFixture,
				Detail: fmt.Sprintf("%s occurs %d times", fixture, count),
			})
		}
	}
	for fixture := range want {
		if seen[fixture] == 0 {
			*issues = append(*issues, CoverageIssue{
				Code:   IssueCoverageFocusedFixture,
				Detail: "missing " + string(fixture),
			})
		}
	}
}

func requiredFocusedProjectionFixtures() map[FocusedProjectionFixtureName]struct{} {
	return map[FocusedProjectionFixtureName]struct{}{
		FocusedKENT345StrictJSON:              {},
		FocusedKENT345CustomWire:              {},
		FocusedKENT345Hydration:               {},
		FocusedKENT345Uniqueness:              {},
		FocusedKENT345MixedValidators:         {},
		FocusedKENT554NegotiationValidation:   {},
		FocusedKENT554NegotiationConstants:    {},
		FocusedKENT554RetainedCapabilityFacts: {},
	}
}
