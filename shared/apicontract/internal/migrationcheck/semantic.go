package migrationcheck

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"core/shared/protoapi"
)

// SemanticKind is the migration helper's descriptor-neutral scalar boundary.
// A later schema slice adapts real Protobuf field descriptors into this shape.
type SemanticKind string

const (
	SemanticBool    SemanticKind = "bool"
	SemanticInt8    SemanticKind = "int8"
	SemanticInt16   SemanticKind = "int16"
	SemanticInt32   SemanticKind = "int32"
	SemanticInt64   SemanticKind = "int64"
	SemanticUint8   SemanticKind = "uint8"
	SemanticUint16  SemanticKind = "uint16"
	SemanticUint32  SemanticKind = "uint32"
	SemanticUint64  SemanticKind = "uint64"
	SemanticFloat32 SemanticKind = "float32"
	SemanticFloat64 SemanticKind = "float64"
	SemanticString  SemanticKind = "string"
	SemanticBytes   SemanticKind = "bytes"
	SemanticMessage SemanticKind = "message"
)

type SemanticValueDescriptor struct {
	Kind        SemanticKind
	MessagePath string
	Presence    bool
}

type SemanticFieldDescriptor struct {
	Name           string
	LegacyJSONName string
	Kind           SemanticKind
	MessagePath    string
	Presence       bool
	Repeated       bool
	MapKey         *SemanticValueDescriptor
	MapValue       *SemanticValueDescriptor
	Oneof          string
}

type SemanticMessageDescriptor struct {
	Path   string
	Fields []SemanticFieldDescriptor
}

type SemanticDescriptorSet struct {
	RootMessagePath string
	Messages        map[string]SemanticMessageDescriptor
}

type ExceptionalSemanticMapping struct {
	LegacyPath     string
	DescriptorPath string
	Presence       bool
	Oneof          string
}

type ExceptionalSemanticFixture interface {
	LegacyType() reflect.Type
	Descriptor() SemanticMessageDescriptor
	Mappings() []ExceptionalSemanticMapping
}

type SemanticIssueCode string

const (
	IssueMissingDescriptorMessage   SemanticIssueCode = "missing_descriptor_message"
	IssueMissingDescriptorField     SemanticIssueCode = "missing_descriptor_field"
	IssueUnexpectedDescriptorField  SemanticIssueCode = "unexpected_descriptor_field"
	IssueWrongLegacyJSONName        SemanticIssueCode = "wrong_legacy_json_name"
	IssueWrongPresence              SemanticIssueCode = "wrong_presence"
	IssueWrongScalarKind            SemanticIssueCode = "wrong_scalar_kind"
	IssueWrongCollectionShape       SemanticIssueCode = "wrong_collection_shape"
	IssueWrongMapShape              SemanticIssueCode = "wrong_map_shape"
	IssueWrongNestedMessage         SemanticIssueCode = "wrong_nested_message"
	IssueRequiresExceptionalFixture SemanticIssueCode = "requires_exceptional_fixture"
	IssueMissingExceptionalMapping  SemanticIssueCode = "missing_exceptional_mapping"
	IssueUnexpectedExceptionalField SemanticIssueCode = "unexpected_exceptional_field"
	IssueDuplicateExceptionalLegacy SemanticIssueCode = "duplicate_exceptional_legacy_path"
	IssueDuplicateExceptionalTarget SemanticIssueCode = "duplicate_exceptional_descriptor_path"
	IssueWrongExceptionalPresence   SemanticIssueCode = "wrong_exceptional_presence"
	IssueWrongExceptionalOneof      SemanticIssueCode = "wrong_exceptional_oneof"
)

type SemanticIssue struct {
	Code           SemanticIssueCode
	LegacyPath     string
	DescriptorPath string
}

type SemanticError struct {
	Issues []SemanticIssue
}

func (e *SemanticError) Error() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "migration semantic check failed with %d issue(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(
			&diagnostic,
			"\n- %s: legacy %s; descriptor %s",
			issue.Code,
			issue.LegacyPath,
			issue.DescriptorPath,
		)
	}
	return diagnostic.String()
}

// CheckOrdinaryMigrationSemantics recursively compares one ordinary exported
// JSON field graph with a descriptor-neutral message graph.
func CheckOrdinaryMigrationSemantics(legacyType reflect.Type, descriptors SemanticDescriptorSet) error {
	legacyType = dereferenceType(legacyType)
	rootDescriptorPath := descriptors.RootMessagePath
	if rootDescriptorPath == "" {
		rootDescriptorPath = "<missing descriptor>"
	}
	if requiresExceptionalFixture(legacyType) {
		return semanticIssues([]SemanticIssue{{
			Code:           IssueRequiresExceptionalFixture,
			LegacyPath:     legacySemanticTypePath(legacyType),
			DescriptorPath: rootDescriptorPath,
		}})
	}

	issues := make([]SemanticIssue, 0)
	visited := make(map[semanticTypeMessagePair]struct{})
	compareOrdinaryMessage(legacyType, descriptors.RootMessagePath, descriptors, visited, &issues)
	return semanticIssues(issues)
}

// CheckExceptionalMigrationSemantics verifies the explicitly enumerated
// legacy-variant-to-descriptor-field mapping for custom wire behavior and
// intentional reshapes.
func CheckExceptionalMigrationSemantics(fixture ExceptionalSemanticFixture) error {
	descriptor := fixture.Descriptor()
	mappings := fixture.Mappings()
	fieldsByPath := make(map[string]SemanticFieldDescriptor, len(descriptor.Fields))
	for _, field := range descriptor.Fields {
		fieldsByPath[descriptorFieldPath(descriptor.Path, field.Name)] = field
	}

	issues := make([]SemanticIssue, 0)
	mapped := make(map[string]struct{}, len(mappings))
	legacyMappingCounts := make(map[string]int, len(mappings))
	descriptorMappingCounts := make(map[string]int, len(mappings))
	for _, mapping := range mappings {
		legacyMappingCounts[mapping.LegacyPath]++
		descriptorMappingCounts[mapping.DescriptorPath]++
	}
	for _, mapping := range mappings {
		if legacyMappingCounts[mapping.LegacyPath] > 1 {
			if legacyMappingCounts[mapping.LegacyPath] == 2 {
				issues = append(issues, SemanticIssue{
					Code:           IssueDuplicateExceptionalLegacy,
					LegacyPath:     mapping.LegacyPath,
					DescriptorPath: mapping.DescriptorPath,
				})
			}
			legacyMappingCounts[mapping.LegacyPath]--
			continue
		}
		if descriptorMappingCounts[mapping.DescriptorPath] > 1 {
			if descriptorMappingCounts[mapping.DescriptorPath] == 2 {
				issues = append(issues, SemanticIssue{
					Code:           IssueDuplicateExceptionalTarget,
					LegacyPath:     mapping.LegacyPath,
					DescriptorPath: mapping.DescriptorPath,
				})
			}
			descriptorMappingCounts[mapping.DescriptorPath]--
			continue
		}
		field, exists := fieldsByPath[mapping.DescriptorPath]
		if !exists {
			issues = append(issues, SemanticIssue{
				Code:           IssueMissingExceptionalMapping,
				LegacyPath:     mapping.LegacyPath,
				DescriptorPath: mapping.DescriptorPath,
			})
			continue
		}
		mapped[mapping.DescriptorPath] = struct{}{}
		if field.Presence != mapping.Presence {
			issues = append(issues, SemanticIssue{
				Code:           IssueWrongExceptionalPresence,
				LegacyPath:     mapping.LegacyPath,
				DescriptorPath: mapping.DescriptorPath,
			})
		}
		if field.Oneof != mapping.Oneof {
			issues = append(issues, SemanticIssue{
				Code:           IssueWrongExceptionalOneof,
				LegacyPath:     mapping.LegacyPath,
				DescriptorPath: mapping.DescriptorPath,
			})
		}
	}
	for fieldPath := range fieldsByPath {
		if _, exists := mapped[fieldPath]; exists {
			continue
		}
		issues = append(issues, SemanticIssue{
			Code:           IssueUnexpectedExceptionalField,
			LegacyPath:     legacySemanticTypePath(fixture.LegacyType()),
			DescriptorPath: fieldPath,
		})
	}
	return semanticIssues(issues)
}

type semanticTypeMessagePair struct {
	legacyType     reflect.Type
	descriptorPath string
}

func compareOrdinaryMessage(
	legacyType reflect.Type,
	descriptorPath string,
	descriptors SemanticDescriptorSet,
	visited map[semanticTypeMessagePair]struct{},
	issues *[]SemanticIssue,
) {
	legacyType = dereferenceType(legacyType)
	pair := semanticTypeMessagePair{legacyType: legacyType, descriptorPath: descriptorPath}
	if _, exists := visited[pair]; exists {
		return
	}
	visited[pair] = struct{}{}

	message, exists := descriptors.Messages[descriptorPath]
	if !exists {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueMissingDescriptorMessage,
			LegacyPath:     legacySemanticTypePath(legacyType),
			DescriptorPath: descriptorPathOrMissing(descriptorPath),
		})
		return
	}

	legacyFields := ordinaryJSONFields(legacyType)
	descriptorFields := make(map[string]SemanticFieldDescriptor, len(message.Fields))
	for _, field := range message.Fields {
		descriptorFields[field.Name] = field
	}

	for _, legacyField := range legacyFields {
		descriptorName, err := protoapi.PascalCaseToLowerSnake(legacyField.Name)
		if err != nil {
			panic(fmt.Sprintf("exported Go field has invalid descriptor identifier %s: %v", legacySemanticPath(legacyType, legacyField.Name), err))
		}
		descriptorPath := descriptorFieldPath(message.Path, descriptorName)
		field, exists := descriptorFields[descriptorName]
		if legacyField.Tag.Get("json") == "-" {
			if exists {
				*issues = append(*issues, SemanticIssue{
					Code:           IssueUnexpectedDescriptorField,
					LegacyPath:     legacySemanticPath(legacyType, legacyField.Name),
					DescriptorPath: descriptorPath,
				})
			}
			delete(descriptorFields, descriptorName)
			continue
		}
		if !exists {
			*issues = append(*issues, SemanticIssue{
				Code:           IssueMissingDescriptorField,
				LegacyPath:     legacySemanticPath(legacyType, legacyField.Name),
				DescriptorPath: descriptorPath,
			})
			continue
		}
		delete(descriptorFields, descriptorName)
		compareOrdinaryField(legacyType, legacyField, field, descriptorPath, descriptors, visited, issues)
	}
	for fieldName := range descriptorFields {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueUnexpectedDescriptorField,
			LegacyPath:     legacySemanticTypePath(legacyType),
			DescriptorPath: descriptorFieldPath(message.Path, fieldName),
		})
	}
}

func compareOrdinaryField(
	owner reflect.Type,
	legacyField reflect.StructField,
	descriptor SemanticFieldDescriptor,
	descriptorPath string,
	descriptors SemanticDescriptorSet,
	visited map[semanticTypeMessagePair]struct{},
	issues *[]SemanticIssue,
) {
	legacyPath := legacySemanticPath(owner, legacyField.Name)
	jsonName := legacyJSONName(legacyField)
	if descriptor.LegacyJSONName != jsonName {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongLegacyJSONName,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
	}

	legacyType := legacyField.Type
	if legacyType.Kind() == reflect.Map {
		if descriptor.MapKey == nil || descriptor.MapValue == nil {
			*issues = append(*issues, SemanticIssue{
				Code:           IssueWrongMapShape,
				LegacyPath:     legacyPath,
				DescriptorPath: descriptorPath,
			})
			return
		}
		compareMapValue(legacyPath, descriptorPath, legacyType.Key(), *descriptor.MapKey, descriptors, visited, issues)
		compareMapValue(legacyPath, descriptorPath, legacyType.Elem(), *descriptor.MapValue, descriptors, visited, issues)
		return
	}
	if descriptor.MapKey != nil || descriptor.MapValue != nil {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongMapShape,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
		return
	}

	repeated := legacyType.Kind() == reflect.Slice && legacyType.Elem().Kind() != reflect.Uint8
	if descriptor.Repeated != repeated {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongCollectionShape,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
		return
	}
	if repeated {
		legacyType = legacyType.Elem()
	}
	compareValue(
		legacyPath,
		descriptorPath,
		legacyType,
		SemanticValueDescriptor{
			Kind:        descriptor.Kind,
			MessagePath: descriptor.MessagePath,
			Presence:    descriptor.Presence,
		},
		descriptors,
		visited,
		issues,
	)
}

func compareMapValue(
	legacyPath string,
	descriptorPath string,
	legacyType reflect.Type,
	descriptor SemanticValueDescriptor,
	descriptors SemanticDescriptorSet,
	visited map[semanticTypeMessagePair]struct{},
	issues *[]SemanticIssue,
) {
	before := len(*issues)
	compareValue(legacyPath, descriptorPath, legacyType, descriptor, descriptors, visited, issues)
	for index := before; index < len(*issues); index++ {
		if (*issues)[index].LegacyPath == legacyPath &&
			(*issues)[index].DescriptorPath == descriptorPath {
			(*issues)[index].Code = IssueWrongMapShape
		}
	}
}

func compareValue(
	legacyPath string,
	descriptorPath string,
	legacyType reflect.Type,
	descriptor SemanticValueDescriptor,
	descriptors SemanticDescriptorSet,
	visited map[semanticTypeMessagePair]struct{},
	issues *[]SemanticIssue,
) {
	presence := legacyType.Kind() == reflect.Pointer
	if descriptor.Presence != presence {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongPresence,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
	}
	legacyType = dereferenceType(legacyType)
	kind := semanticKindOf(legacyType)
	if descriptor.Kind != kind {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongScalarKind,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
		return
	}
	if kind != SemanticMessage {
		return
	}
	if descriptor.MessagePath == "" {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongNestedMessage,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
		return
	}
	if _, exists := descriptors.Messages[descriptor.MessagePath]; !exists {
		*issues = append(*issues, SemanticIssue{
			Code:           IssueWrongNestedMessage,
			LegacyPath:     legacyPath,
			DescriptorPath: descriptorPath,
		})
		return
	}
	compareOrdinaryMessage(legacyType, descriptor.MessagePath, descriptors, visited, issues)
}

func ordinaryJSONFields(legacyType reflect.Type) []reflect.StructField {
	fields := make([]reflect.StructField, 0, legacyType.NumField())
	for index := range legacyType.NumField() {
		field := legacyType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

func requiresExceptionalFixture(legacyType reflect.Type) bool {
	marshalerType := reflect.TypeFor[json.Marshaler]()
	unmarshalerType := reflect.TypeFor[json.Unmarshaler]()
	pointerType := reflect.PointerTo(legacyType)
	if legacyType.Implements(marshalerType) ||
		pointerType.Implements(marshalerType) ||
		legacyType.Implements(unmarshalerType) ||
		pointerType.Implements(unmarshalerType) {
		return true
	}
	for index := range legacyType.NumField() {
		if legacyType.Field(index).PkgPath != "" {
			return true
		}
	}
	return false
}

func semanticKindOf(legacyType reflect.Type) SemanticKind {
	switch legacyType.Kind() {
	case reflect.Bool:
		return SemanticBool
	case reflect.Int8:
		return SemanticInt8
	case reflect.Int16:
		return SemanticInt16
	case reflect.Int32:
		return SemanticInt32
	case reflect.Int, reflect.Int64:
		return SemanticInt64
	case reflect.Uint8:
		return SemanticUint8
	case reflect.Uint16:
		return SemanticUint16
	case reflect.Uint32:
		return SemanticUint32
	case reflect.Uint, reflect.Uint64:
		return SemanticUint64
	case reflect.Float32:
		return SemanticFloat32
	case reflect.Float64:
		return SemanticFloat64
	case reflect.String:
		return SemanticString
	case reflect.Slice:
		if legacyType.Elem().Kind() == reflect.Uint8 {
			return SemanticBytes
		}
	case reflect.Struct:
		return SemanticMessage
	}
	return ""
}

func legacyJSONName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}
	end := 0
	for end < len(tag) && tag[end] != ',' {
		end++
	}
	if end == 0 {
		return field.Name
	}
	return tag[:end]
}

func legacySemanticPath(owner reflect.Type, fieldName string) string {
	return legacySemanticTypePath(owner) + "." + fieldName
}

func legacySemanticTypePath(legacyType reflect.Type) string {
	legacyType = dereferenceType(legacyType)
	if legacyType.PkgPath() == "" {
		return legacyType.String()
	}
	return legacyType.PkgPath() + "." + legacyType.Name()
}

func descriptorFieldPath(messagePath string, fieldName string) string {
	if messagePath == "" {
		return "<missing descriptor>." + fieldName
	}
	return messagePath + "." + fieldName
}

func descriptorPathOrMissing(path string) string {
	if path == "" {
		return "<missing descriptor>"
	}
	return path
}

func dereferenceType(value reflect.Type) reflect.Type {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func semanticIssues(issues []SemanticIssue) error {
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		if issues[left].LegacyPath != issues[right].LegacyPath {
			return issues[left].LegacyPath < issues[right].LegacyPath
		}
		return issues[left].DescriptorPath < issues[right].DescriptorPath
	})
	return &SemanticError{Issues: issues}
}
