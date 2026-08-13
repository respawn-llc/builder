package migrationcheck

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var javaScriptSafeIntegerMaximum = new(big.Int).Sub(
	new(big.Int).Lsh(big.NewInt(1), 53),
	big.NewInt(1),
)

type descriptorPolicyMessageRole string

const (
	descriptorPolicyOperation descriptorPolicyMessageRole = "operation"
	descriptorPolicyEnvelope  descriptorPolicyMessageRole = "envelope"
)

type descriptorPolicyFieldKind string

const (
	descriptorPolicyString  descriptorPolicyFieldKind = "string"
	descriptorPolicyInt64   descriptorPolicyFieldKind = "int64"
	descriptorPolicyUint64  descriptorPolicyFieldKind = "uint64"
	descriptorPolicyEnum    descriptorPolicyFieldKind = "enum"
	descriptorPolicyBytes   descriptorPolicyFieldKind = "bytes"
	descriptorPolicyAny     descriptorPolicyFieldKind = "any"
	descriptorPolicyRawJSON descriptorPolicyFieldKind = "raw_json"
	descriptorPolicyMap     descriptorPolicyFieldKind = "map"
)

type descriptorPolicyIntegerBounds struct {
	Minimum *big.Int
	Maximum *big.Int
}

type descriptorPolicyField struct {
	Name                   string
	Kind                   descriptorPolicyFieldKind
	MapValue               descriptorPolicyFieldKind
	EnumDefinedOnly        bool
	IntegerBounds          descriptorPolicyIntegerBounds
	DescriptorTypedPayload bool
}

type descriptorPolicyMessage struct {
	Path   string
	Role   descriptorPolicyMessageRole
	Fields []descriptorPolicyField
}

type descriptorPolicyPackage struct {
	Name     string
	Messages []descriptorPolicyMessage
}

type descriptorPolicySet struct {
	Packages   []descriptorPolicyPackage
	Operations []descriptorPolicyOperationIdentity
}

type descriptorPolicyOperationIdentity struct {
	ActiveName     string
	LegacyWireName *string
	DescriptorPath string
	Kind           sharedpb.OperationKind
	Output         protoreflect.MessageDescriptor
}

type descriptorPolicyIssueCode string

const (
	issuePackageVersionSegment            descriptorPolicyIssueCode = "package_version_segment"
	issueEnumAllowsUnknownValues          descriptorPolicyIssueCode = "enum_allows_unknown_values"
	issueUnsafeJavaScriptIntegerBounds    descriptorPolicyIssueCode = "unsafe_javascript_integer_bounds"
	issueForbiddenOperationAny            descriptorPolicyIssueCode = "forbidden_operation_any"
	issueForbiddenOperationRawJSON        descriptorPolicyIssueCode = "forbidden_operation_raw_json"
	issueForbiddenOperationBytes          descriptorPolicyIssueCode = "forbidden_operation_bytes"
	issueForbiddenOperationMap            descriptorPolicyIssueCode = "forbidden_operation_map"
	issueGenericApplicationRequestID      descriptorPolicyIssueCode = "generic_application_request_id"
	issueForbiddenEnvelopeBytes           descriptorPolicyIssueCode = "forbidden_envelope_bytes"
	issueDuplicateActiveOperationName     descriptorPolicyIssueCode = "duplicate_active_operation_name"
	issueDuplicateLegacyWireName          descriptorPolicyIssueCode = "duplicate_legacy_wire_name"
	issueInvalidOperationResultConvention descriptorPolicyIssueCode = "invalid_operation_result_convention"
)

type descriptorPolicyIssue struct {
	Code        descriptorPolicyIssueCode
	PackageName string
	MessagePath string
	FieldName   string
}

type descriptorPolicyError struct {
	Issues []descriptorPolicyIssue
}

func (e *descriptorPolicyError) Error() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "descriptor policy failed with %d issue(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(
			&diagnostic,
			"\n- %s: package %s; message %s; field %s",
			issue.Code,
			issue.PackageName,
			issue.MessagePath,
			issue.FieldName,
		)
	}
	return diagnostic.String()
}

func checkDescriptorPolicy(descriptors descriptorPolicySet) error {
	issues := make([]descriptorPolicyIssue, 0)
	for _, packageDescriptor := range descriptors.Packages {
		checkPackageVersionSegments(packageDescriptor.Name, &issues)
		for _, message := range packageDescriptor.Messages {
			for _, field := range message.Fields {
				checkDescriptorPolicyField(packageDescriptor.Name, message, field, &issues)
			}
		}
	}
	checkDuplicateOperationNames(descriptors.Operations, &issues)
	checkOperationResultConventions(descriptors.Operations, &issues)
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].PackageName != issues[right].PackageName {
			return issues[left].PackageName < issues[right].PackageName
		}
		if issues[left].MessagePath != issues[right].MessagePath {
			return issues[left].MessagePath < issues[right].MessagePath
		}
		if issues[left].FieldName != issues[right].FieldName {
			return issues[left].FieldName < issues[right].FieldName
		}
		return issues[left].Code < issues[right].Code
	})
	return &descriptorPolicyError{Issues: issues}
}

func checkOperationResultConventions(
	operations []descriptorPolicyOperationIdentity,
	issues *[]descriptorPolicyIssue,
) {
	for _, operation := range operations {
		switch operation.Kind {
		case sharedpb.OperationKind_OPERATION_KIND_UNARY,
			sharedpb.OperationKind_OPERATION_KIND_PROGRESS,
			sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION:
			if protoapi.IsOperationResultDescriptor(operation.Output) {
				continue
			}
			*issues = append(*issues, descriptorPolicyIssue{
				Code:        issueInvalidOperationResultConvention,
				MessagePath: operation.DescriptorPath,
			})
		case sharedpb.OperationKind_OPERATION_KIND_NOTIFICATION:
		default:
			*issues = append(*issues, descriptorPolicyIssue{
				Code:        issueInvalidOperationResultConvention,
				MessagePath: operation.DescriptorPath,
			})
		}
	}
}

func checkPackageVersionSegments(packageName string, issues *[]descriptorPolicyIssue) {
	segmentStart := 0
	for index := 0; index <= len(packageName); index++ {
		if index != len(packageName) && packageName[index] != '.' {
			continue
		}
		segment := packageName[segmentStart:index]
		if isPackageVersionSegment(segment) {
			*issues = append(*issues, descriptorPolicyIssue{
				Code:        issuePackageVersionSegment,
				PackageName: packageName,
			})
		}
		segmentStart = index + 1
	}
}

func isPackageVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for index := 1; index < len(segment); index++ {
		if segment[index] < '0' || segment[index] > '9' {
			return false
		}
	}
	return true
}

func checkDescriptorPolicyField(
	packageName string,
	message descriptorPolicyMessage,
	field descriptorPolicyField,
	issues *[]descriptorPolicyIssue,
) {
	addIssue := func(code descriptorPolicyIssueCode) {
		*issues = append(*issues, descriptorPolicyIssue{
			Code:        code,
			PackageName: packageName,
			MessagePath: message.Path,
			FieldName:   field.Name,
		})
	}

	if field.Kind == descriptorPolicyEnum && !field.EnumDefinedOnly {
		addIssue(issueEnumAllowsUnknownValues)
	}
	if field.Kind == descriptorPolicyInt64 || field.Kind == descriptorPolicyUint64 {
		if !hasJavaScriptSafeIntegerBounds(field.Kind, field.IntegerBounds) {
			addIssue(issueUnsafeJavaScriptIntegerBounds)
		}
	}

	switch message.Role {
	case descriptorPolicyOperation:
		switch field.Kind {
		case descriptorPolicyAny:
			addIssue(issueForbiddenOperationAny)
		case descriptorPolicyRawJSON:
			addIssue(issueForbiddenOperationRawJSON)
		case descriptorPolicyBytes:
			addIssue(issueForbiddenOperationBytes)
		case descriptorPolicyMap:
			addIssue(issueForbiddenOperationMap)
		}
		if isGenericApplicationRequestID(field.Name) {
			addIssue(issueGenericApplicationRequestID)
		}
	case descriptorPolicyEnvelope:
		if field.Kind == descriptorPolicyBytes && !field.DescriptorTypedPayload {
			addIssue(issueForbiddenEnvelopeBytes)
		}
	}
}

func hasJavaScriptSafeIntegerBounds(
	kind descriptorPolicyFieldKind,
	bounds descriptorPolicyIntegerBounds,
) bool {
	if bounds.Minimum == nil || bounds.Maximum == nil {
		return false
	}
	if bounds.Maximum.Cmp(javaScriptSafeIntegerMaximum) > 0 {
		return false
	}
	switch kind {
	case descriptorPolicyInt64:
		minimum := new(big.Int).Neg(new(big.Int).Set(javaScriptSafeIntegerMaximum))
		return bounds.Minimum.Cmp(minimum) >= 0
	case descriptorPolicyUint64:
		return bounds.Minimum.Sign() >= 0
	default:
		panic("JavaScript integer bounds checked for non-64-bit integer field")
	}
}

func checkDuplicateOperationNames(
	operations []descriptorPolicyOperationIdentity,
	issues *[]descriptorPolicyIssue,
) {
	activeNames := make(map[string]string, len(operations))
	legacyNames := make(map[string]string, len(operations))
	for _, operation := range operations {
		if priorPath, duplicate := activeNames[operation.ActiveName]; duplicate {
			*issues = append(*issues, descriptorPolicyIssue{
				Code:        issueDuplicateActiveOperationName,
				MessagePath: operation.DescriptorPath,
				FieldName:   priorPath,
			})
		} else {
			activeNames[operation.ActiveName] = operation.DescriptorPath
		}
		if operation.LegacyWireName == nil {
			continue
		}
		if priorPath, duplicate := legacyNames[*operation.LegacyWireName]; duplicate {
			*issues = append(*issues, descriptorPolicyIssue{
				Code:        issueDuplicateLegacyWireName,
				MessagePath: operation.DescriptorPath,
				FieldName:   priorPath,
			})
		} else {
			legacyNames[*operation.LegacyWireName] = operation.DescriptorPath
		}
	}
}

func isGenericApplicationRequestID(fieldName string) bool {
	return fieldName == "request_id" || fieldName == "client_request_id"
}

func parseDescriptorPolicyFixture() (descriptorPolicySet, error) {
	files := make([]protoreflect.FileDescriptor, 0)
	for file := range protoapi.Files() {
		files = append(files, file)
	}
	return parseDescriptorPolicyFiles(files)
}

func parseDescriptorPolicyFiles(files []protoreflect.FileDescriptor) (descriptorPolicySet, error) {
	descriptorSet := descriptorPolicySet{}
	operationMessages := make(map[protoreflect.FullName]struct{})
	for _, file := range files {
		collectFileOperationMessageNames(file, operationMessages)
	}
	for _, file := range files {
		packageDescriptor := descriptorPolicyPackage{Name: string(file.Package())}
		appendDescriptorPolicyMessages(file.Messages(), "", operationMessages, &packageDescriptor.Messages)
		descriptorSet.Packages = append(descriptorSet.Packages, packageDescriptor)
		services := file.Services()
		for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
			methods := services.Get(serviceIndex).Methods()
			for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
				method := methods.Get(methodIndex)
				options, err := descriptorPolicyMethodOptions(method)
				if err != nil {
					return descriptorPolicySet{}, err
				}
				activeName, err := protoapi.ActiveOperationName(
					string(file.Package()),
					string(method.Parent().Name()),
					string(method.Name()),
				)
				if err != nil {
					return descriptorPolicySet{}, fmt.Errorf("%s: %w", method.FullName(), err)
				}
				var legacyWireName *string
				if options.LegacyWireName != nil {
					value := options.GetLegacyWireName()
					legacyWireName = &value
				}
				descriptorSet.Operations = append(descriptorSet.Operations, descriptorPolicyOperationIdentity{
					ActiveName:     activeName,
					LegacyWireName: legacyWireName,
					DescriptorPath: string(method.FullName()),
					Kind:           options.Kind,
					Output:         method.Output(),
				})
			}
		}
	}
	return descriptorSet, nil
}

func descriptorPolicyMethodOptions(
	method protoreflect.MethodDescriptor,
) (*sharedpb.KentMethodOptions, error) {
	options := method.Options()
	if !proto.HasExtension(options, sharedpb.E_KentMethod) {
		return nil, fmt.Errorf("%s method options are required", method.FullName())
	}
	value, ok := proto.GetExtension(options, sharedpb.E_KentMethod).(*sharedpb.KentMethodOptions)
	if !ok {
		return nil, fmt.Errorf("%s method options have unexpected Go type", method.FullName())
	}
	return value, nil
}

func appendDescriptorPolicyMessages(
	messages protoreflect.MessageDescriptors,
	parentPath string,
	operationMessages map[protoreflect.FullName]struct{},
	destination *[]descriptorPolicyMessage,
) {
	for index := 0; index < messages.Len(); index++ {
		message := messages.Get(index)
		messagePath := string(message.Name())
		if parentPath != "" {
			messagePath = parentPath + "." + messagePath
		}
		projected := descriptorPolicyMessage{
			Path: messagePath,
			Role: descriptorPolicyMessageRoleOf(message, operationMessages),
		}
		fields := message.Fields()
		for fieldIndex := 0; fieldIndex < fields.Len(); fieldIndex++ {
			projected.Fields = append(projected.Fields, projectDescriptorPolicyField(fields.Get(fieldIndex)))
		}
		*destination = append(*destination, projected)
		appendDescriptorPolicyMessages(message.Messages(), messagePath, operationMessages, destination)
	}
}

func collectFileOperationMessageNames(
	file protoreflect.FileDescriptor,
	names map[protoreflect.FullName]struct{},
) {
	services := file.Services()
	for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
		methods := services.Get(serviceIndex).Methods()
		for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
			method := methods.Get(methodIndex)
			collectOperationMessageNames(method.Input(), names)
			collectOperationMessageNames(method.Output(), names)
		}
	}
}

func collectOperationMessageNames(
	message protoreflect.MessageDescriptor,
	names map[protoreflect.FullName]struct{},
) {
	if message == nil {
		return
	}
	if _, exists := names[message.FullName()]; exists {
		return
	}
	names[message.FullName()] = struct{}{}
	fields := message.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if field.IsMap() || field.Kind() != protoreflect.MessageKind {
			continue
		}
		collectOperationMessageNames(field.Message(), names)
	}
}

func projectDescriptorPolicyField(field protoreflect.FieldDescriptor) descriptorPolicyField {
	projected := descriptorPolicyField{Name: string(field.Name())}
	if field.IsMap() {
		projected.Kind = descriptorPolicyMap
		projected.MapValue = descriptorPolicyKind(field.MapValue())
		return projected
	}
	projected.Kind = descriptorPolicyKind(field)
	projected.DescriptorTypedPayload = isDescriptorTypedPayload(field)
	switch projected.Kind {
	case descriptorPolicyEnum:
		projected.EnumDefinedOnly = enumDefinedOnly(field)
	case descriptorPolicyInt64, descriptorPolicyUint64:
		projected.IntegerBounds = integerBounds(field, projected.Kind)
	}
	return projected
}

func descriptorPolicyMessageRoleOf(
	message protoreflect.MessageDescriptor,
	operationMessages map[protoreflect.FullName]struct{},
) descriptorPolicyMessageRole {
	switch message.FullName() {
	case "kent.api.shared.Call",
		"kent.api.shared.Result",
		"kent.api.shared.NotificationEvent",
		"kent.api.shared.TransportFailure":
		return descriptorPolicyEnvelope
	default:
		if _, exists := operationMessages[message.FullName()]; exists {
			return descriptorPolicyOperation
		}
		return ""
	}
}

func isDescriptorTypedPayload(field protoreflect.FieldDescriptor) bool {
	if field.Name() != "payload" {
		return false
	}
	switch field.ContainingMessage().FullName() {
	case "kent.api.shared.Call",
		"kent.api.shared.Result",
		"kent.api.shared.NotificationEvent":
		return true
	default:
		return false
	}
}

func descriptorPolicyKind(field protoreflect.FieldDescriptor) descriptorPolicyFieldKind {
	switch field.Kind() {
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return descriptorPolicyInt64
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return descriptorPolicyUint64
	case protoreflect.EnumKind:
		return descriptorPolicyEnum
	case protoreflect.BytesKind:
		return descriptorPolicyBytes
	case protoreflect.MessageKind:
		switch field.Message().FullName() {
		case "google.protobuf.Any":
			return descriptorPolicyAny
		case "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.ListValue":
			return descriptorPolicyRawJSON
		default:
			return descriptorPolicyString
		}
	default:
		return descriptorPolicyString
	}
}

func fieldRules(field protoreflect.FieldDescriptor) *validate.FieldRules {
	options := field.Options()
	if !proto.HasExtension(options, validate.E_Field) {
		return nil
	}
	rules, ok := proto.GetExtension(options, validate.E_Field).(*validate.FieldRules)
	if !ok {
		panic(fmt.Sprintf("buf.validate.field on %s has unexpected Go type", field.FullName()))
	}
	return rules
}

func enumDefinedOnly(field protoreflect.FieldDescriptor) bool {
	rules := fieldRules(field)
	if rules == nil {
		return false
	}
	if field.Cardinality() == protoreflect.Repeated {
		return rules.GetRepeated().GetItems().GetEnum().GetDefinedOnly()
	}
	return rules.GetEnum().GetDefinedOnly()
}

func integerBounds(
	field protoreflect.FieldDescriptor,
	kind descriptorPolicyFieldKind,
) descriptorPolicyIntegerBounds {
	rules := fieldRules(field)
	if rules == nil {
		return descriptorPolicyIntegerBounds{}
	}
	switch kind {
	case descriptorPolicyInt64:
		return signedIntegerBounds(rules.GetInt64())
	case descriptorPolicyUint64:
		return unsignedIntegerBounds(rules.GetUint64())
	default:
		panic("integer bounds requested for non-64-bit field")
	}
}

func signedIntegerBounds(rules *validate.Int64Rules) descriptorPolicyIntegerBounds {
	if rules == nil {
		return descriptorPolicyIntegerBounds{}
	}
	bounds := descriptorPolicyIntegerBounds{}
	switch {
	case rules.HasGte():
		bounds.Minimum = big.NewInt(rules.GetGte())
	case rules.HasGt():
		bounds.Minimum = new(big.Int).Add(big.NewInt(rules.GetGt()), big.NewInt(1))
	}
	switch {
	case rules.HasLte():
		bounds.Maximum = big.NewInt(rules.GetLte())
	case rules.HasLt():
		bounds.Maximum = new(big.Int).Sub(big.NewInt(rules.GetLt()), big.NewInt(1))
	}
	return bounds
}

func unsignedIntegerBounds(rules *validate.UInt64Rules) descriptorPolicyIntegerBounds {
	if rules == nil {
		return descriptorPolicyIntegerBounds{}
	}
	bounds := descriptorPolicyIntegerBounds{}
	switch {
	case rules.HasGte():
		bounds.Minimum = new(big.Int).SetUint64(rules.GetGte())
	case rules.HasGt():
		bounds.Minimum = new(big.Int).Add(
			new(big.Int).SetUint64(rules.GetGt()),
			big.NewInt(1),
		)
	}
	switch {
	case rules.HasLte():
		bounds.Maximum = new(big.Int).SetUint64(rules.GetLte())
	case rules.HasLt():
		bounds.Maximum = new(big.Int).Sub(
			new(big.Int).SetUint64(rules.GetLt()),
			big.NewInt(1),
		)
	}
	return bounds
}
