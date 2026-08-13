package migrationcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strings"

	"core/shared/protoapi"
	"golang.org/x/tools/go/packages"
)

type DeclarationReport struct {
	NamedScalars []NamedScalar
	Validators   []Validator
}

type ScalarClassificationKind string

const (
	ScalarOpenValidatedString ScalarClassificationKind = "open_validated_string"
	ScalarIdentifier          ScalarClassificationKind = "identifier"
	ScalarClosedStringEnum    ScalarClassificationKind = "closed_string_enum"
	ScalarBoundedInteger      ScalarClassificationKind = "bounded_integer"
	ScalarProtobufDuration    ScalarClassificationKind = "protobuf_duration"
)

type EnumMemberClassification struct {
	GoConstant        string
	DescriptorName    string
	IntentionalRename bool
}

type ScalarClassification struct {
	Identity    Identity
	Kind        ScalarClassificationKind
	EnumMembers []EnumMemberClassification
}

type ValidatorClassificationKind string

const (
	ValidatorMessageLocal          ValidatorClassificationKind = "message_local"
	ValidatorStatefulOrSharedOwner ValidatorClassificationKind = "stateful_or_shared_owner"
)

type ServerAPISpec string

type ServerOwner string

type ValidatorOwner struct {
	Spec   ServerAPISpec
	Server ServerOwner
}

type ValidatorClassification struct {
	Identity    Identity
	Fingerprint string
	Kind        ValidatorClassificationKind
	Owner       *ValidatorOwner
}

type DeclarationClassification struct {
	Scalars    []ScalarClassification
	Validators []ValidatorClassification
}

type ClassificationIssueCode string

const (
	IssueUnclassifiedScalar             ClassificationIssueCode = "unclassified_scalar"
	IssueUnexpectedScalarClassification ClassificationIssueCode = "unexpected_scalar_classification"
	IssueInvalidScalarClassification    ClassificationIssueCode = "invalid_scalar_classification"
	IssueClosedEnumSetMismatch          ClassificationIssueCode = "closed_enum_set_mismatch"
	IssueInvalidIntentionalEnumRename   ClassificationIssueCode = "invalid_intentional_enum_rename"
	IssueUnclassifiedValidator          ClassificationIssueCode = "unclassified_validator"
	IssueUnexpectedValidatorSignoff     ClassificationIssueCode = "unexpected_validator_signoff"
	IssueValidatorFingerprintMismatch   ClassificationIssueCode = "validator_fingerprint_mismatch"
	IssueInvalidValidatorClassification ClassificationIssueCode = "invalid_validator_classification"
	IssueMissingValidatorOwner          ClassificationIssueCode = "missing_validator_owner"
)

type ClassificationIssue struct {
	Code     ClassificationIssueCode
	Identity Identity
	Detail   string
}

type ClassificationError struct {
	Issues []ClassificationIssue
}

func (e *ClassificationError) Error() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "declaration classification failed with %d issue(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(&diagnostic, "\n- %s: %s", issue.Code, issue.Identity)
		if issue.Detail != "" {
			fmt.Fprintf(&diagnostic, ": %s", issue.Detail)
		}
	}
	return diagnostic.String()
}

type ValidationBehaviorCase[T any] struct {
	Name  string
	Value T
}

type ValidationBehaviorIssue struct {
	CaseName           string
	LegacyAccepted     bool
	DescriptorAccepted bool
}

type ValidationBehaviorError struct {
	Issues []ValidationBehaviorIssue
}

func (e *ValidationBehaviorError) Error() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "validation behavior parity failed with %d issue(s)", len(e.Issues))
	for _, issue := range e.Issues {
		fmt.Fprintf(
			&diagnostic,
			"\n- %s: legacy accepted=%t; descriptor accepted=%t",
			issue.CaseName,
			issue.LegacyAccepted,
			issue.DescriptorAccepted,
		)
	}
	return diagnostic.String()
}

// InspectDeclarations performs bounded structured analysis for the supplied
// reachable roots. It discovers scalar declarations, typed constants, and
// Validate/ValidateRPC implementation closures without scanning unrelated
// packages or matching source text.
func InspectDeclarations(rootTypes ...reflect.Type) (DeclarationReport, error) {
	reachable := collectReachableTypesFromRoots(rootTypes...)

	packagePaths := make([]string, 0)
	seenPackagePaths := make(map[string]struct{})
	for reflectedType := range reachable {
		if reflectedType.PkgPath() == "" {
			continue
		}
		if _, exists := seenPackagePaths[reflectedType.PkgPath()]; exists {
			continue
		}
		seenPackagePaths[reflectedType.PkgPath()] = struct{}{}
		packagePaths = append(packagePaths, reflectedType.PkgPath())
	}
	sort.Strings(packagePaths)
	loaded, err := loadPackages(packagePaths)
	if err != nil {
		return DeclarationReport{}, err
	}
	scalars, validators, err := discoverDeclarations(reachable, loaded)
	if err != nil {
		return DeclarationReport{}, err
	}
	if err := populateValidatorClosures(validators, loaded); err != nil {
		return DeclarationReport{}, err
	}
	return DeclarationReport{NamedScalars: scalars, Validators: validators}, nil
}

// CheckDeclarationClassifications requires exact reviewed coverage for every
// discovered scalar and validator implementation.
func CheckDeclarationClassifications(report DeclarationReport, classification DeclarationClassification) error {
	issues := make([]ClassificationIssue, 0)
	scalarsByIdentity := make(map[Identity]NamedScalar, len(report.NamedScalars))
	for _, scalar := range report.NamedScalars {
		scalarsByIdentity[scalar.Identity] = scalar
	}
	classifiedScalars := make(map[Identity]ScalarClassification, len(classification.Scalars))
	for _, scalar := range classification.Scalars {
		if _, exists := classifiedScalars[scalar.Identity]; exists {
			issues = append(issues, ClassificationIssue{
				Code:     IssueUnexpectedScalarClassification,
				Identity: scalar.Identity,
				Detail:   "duplicate classification",
			})
			continue
		}
		classifiedScalars[scalar.Identity] = scalar
		discovered, exists := scalarsByIdentity[scalar.Identity]
		if !exists {
			issues = append(issues, ClassificationIssue{
				Code:     IssueUnexpectedScalarClassification,
				Identity: scalar.Identity,
			})
			continue
		}
		checkScalarClassification(discovered, scalar, &issues)
	}
	for identity := range scalarsByIdentity {
		if _, exists := classifiedScalars[identity]; !exists {
			issues = append(issues, ClassificationIssue{
				Code:     IssueUnclassifiedScalar,
				Identity: identity,
			})
		}
	}

	validatorsByIdentity := make(map[Identity]Validator, len(report.Validators))
	for _, validator := range report.Validators {
		validatorsByIdentity[validator.Identity] = validator
	}
	classifiedValidators := make(map[Identity]ValidatorClassification, len(classification.Validators))
	for _, validator := range classification.Validators {
		if _, exists := classifiedValidators[validator.Identity]; exists {
			issues = append(issues, ClassificationIssue{
				Code:     IssueUnexpectedValidatorSignoff,
				Identity: validator.Identity,
				Detail:   "duplicate sign-off",
			})
			continue
		}
		classifiedValidators[validator.Identity] = validator
		discovered, exists := validatorsByIdentity[validator.Identity]
		if !exists {
			issues = append(issues, ClassificationIssue{
				Code:     IssueUnexpectedValidatorSignoff,
				Identity: validator.Identity,
			})
			continue
		}
		checkValidatorClassification(discovered, validator, &issues)
	}
	for identity := range validatorsByIdentity {
		if _, exists := classifiedValidators[identity]; !exists {
			discovered := validatorsByIdentity[identity]
			issues = append(issues, ClassificationIssue{
				Code:     IssueUnclassifiedValidator,
				Identity: identity,
				Detail:   "discovered fingerprint " + discovered.Fingerprint,
			})
		}
	}

	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Identity.String() != issues[right].Identity.String() {
			return issues[left].Identity.String() < issues[right].Identity.String()
		}
		return issues[left].Code < issues[right].Code
	})
	return &ClassificationError{Issues: issues}
}

func checkScalarClassification(
	discovered NamedScalar,
	classification ScalarClassification,
	issues *[]ClassificationIssue,
) {
	switch classification.Kind {
	case ScalarOpenValidatedString, ScalarIdentifier, ScalarBoundedInteger, ScalarProtobufDuration:
		if len(classification.EnumMembers) != 0 {
			*issues = append(*issues, ClassificationIssue{
				Code:     IssueInvalidScalarClassification,
				Identity: discovered.Identity,
				Detail:   "non-enum scalar declares enum members",
			})
		}
	case ScalarClosedStringEnum:
		checkClosedEnumClassification(discovered, classification, issues)
	default:
		*issues = append(*issues, ClassificationIssue{
			Code:     IssueInvalidScalarClassification,
			Identity: discovered.Identity,
			Detail:   "unknown scalar classification kind",
		})
	}
}

func checkClosedEnumClassification(
	discovered NamedScalar,
	classification ScalarClassification,
	issues *[]ClassificationIssue,
) {
	discoveredConstants := make(map[string]struct{}, len(discovered.Constants))
	for _, constant := range discovered.Constants {
		discoveredConstants[constant.Name()] = struct{}{}
	}
	classifiedConstants := make(map[string]struct{}, len(classification.EnumMembers))
	mismatch := len(discoveredConstants) != len(classification.EnumMembers)
	for _, member := range classification.EnumMembers {
		if _, exists := classifiedConstants[member.GoConstant]; exists {
			mismatch = true
		}
		classifiedConstants[member.GoConstant] = struct{}{}
		if _, exists := discoveredConstants[member.GoConstant]; !exists {
			mismatch = true
		}
		if member.IntentionalRename {
			if member.DescriptorName == "" ||
				member.DescriptorName == defaultDescriptorEnumName(member.GoConstant) {
				*issues = append(*issues, ClassificationIssue{
					Code:     IssueInvalidIntentionalEnumRename,
					Identity: discovered.Identity,
					Detail:   member.GoConstant,
				})
			}
		} else if member.DescriptorName != defaultDescriptorEnumName(member.GoConstant) {
			*issues = append(*issues, ClassificationIssue{
				Code:     IssueInvalidIntentionalEnumRename,
				Identity: discovered.Identity,
				Detail:   member.GoConstant,
			})
		}
	}
	if mismatch {
		*issues = append(*issues, ClassificationIssue{
			Code:     IssueClosedEnumSetMismatch,
			Identity: discovered.Identity,
		})
	}
}

func defaultDescriptorEnumName(goConstant string) string {
	name, err := protoapi.PascalCaseToLowerSnake(goConstant)
	if err != nil {
		return ""
	}
	var result strings.Builder
	result.Grow(len(name))
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		}
		result.WriteByte(character)
	}
	return result.String()
}

func checkValidatorClassification(
	discovered Validator,
	classification ValidatorClassification,
	issues *[]ClassificationIssue,
) {
	if classification.Fingerprint != discovered.Fingerprint {
		*issues = append(*issues, ClassificationIssue{
			Code:     IssueValidatorFingerprintMismatch,
			Identity: discovered.Identity,
			Detail: fmt.Sprintf(
				"discovered %s, classified %s",
				discovered.Fingerprint,
				classification.Fingerprint,
			),
		})
	}
	switch classification.Kind {
	case ValidatorMessageLocal:
		if classification.Owner != nil {
			*issues = append(*issues, ClassificationIssue{
				Code:     IssueInvalidValidatorClassification,
				Identity: discovered.Identity,
				Detail:   "message-local validator declares a server owner",
			})
		}
	case ValidatorStatefulOrSharedOwner:
		if classification.Owner == nil ||
			classification.Owner.Spec == "" ||
			classification.Owner.Server == "" {
			*issues = append(*issues, ClassificationIssue{
				Code:     IssueMissingValidatorOwner,
				Identity: discovered.Identity,
			})
		}
	default:
		*issues = append(*issues, ClassificationIssue{
			Code:     IssueInvalidValidatorClassification,
			Identity: discovered.Identity,
			Detail:   "unknown validator classification kind",
		})
	}
}

// CheckValidationBehaviorParity compares acceptance at the legacy validation
// boundary and its descriptor-neutral generated-validation equivalent.
func CheckValidationBehaviorParity[T any](
	cases []ValidationBehaviorCase[T],
	legacyValidate func(T) error,
	descriptorValidate func(T) error,
) error {
	issues := make([]ValidationBehaviorIssue, 0)
	for _, testCase := range cases {
		legacyAccepted := legacyValidate(testCase.Value) == nil
		descriptorAccepted := descriptorValidate(testCase.Value) == nil
		if legacyAccepted == descriptorAccepted {
			continue
		}
		issues = append(issues, ValidationBehaviorIssue{
			CaseName:           testCase.Name,
			LegacyAccepted:     legacyAccepted,
			DescriptorAccepted: descriptorAccepted,
		})
	}
	if len(issues) == 0 {
		return nil
	}
	return &ValidationBehaviorError{Issues: issues}
}

func populateValidatorClosures(validators []Validator, loaded map[string]*packages.Package) error {
	for index := range validators {
		loadedPackage := loaded[validators[index].Identity.PackagePath]
		if loadedPackage == nil {
			return fmt.Errorf("validator package is not loaded: %s", validators[index].Identity.PackagePath)
		}
		closure, canonical, err := validatorClosure(loadedPackage, validators[index].Function)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canonical)
		validators[index].Closure = closure
		validators[index].Fingerprint = hex.EncodeToString(sum[:])
	}
	return nil
}

func validatorClosure(
	loadedPackage *packages.Package,
	root *types.Func,
) ([]Identity, []byte, error) {
	declarations := functionDeclarations(loadedPackage)
	queued := []*types.Func{root}
	seen := make(map[*types.Func]struct{})
	functions := make([]*types.Func, 0)
	for len(queued) > 0 {
		function := queued[0]
		queued = queued[1:]
		if _, exists := seen[function]; exists {
			continue
		}
		seen[function] = struct{}{}
		declaration := declarations[function]
		if declaration == nil {
			return nil, nil, fmt.Errorf("validator declaration syntax does not resolve: %s", function.FullName())
		}
		functions = append(functions, function)
		for _, called := range directlyCalledPackageFunctions(declaration, loadedPackage, declarations) {
			if called.Pkg() == loadedPackage.Types {
				queued = append(queued, called)
			}
		}
	}
	sort.Slice(functions, func(left, right int) bool {
		return functionIdentityForObject(functions[left]).String() <
			functionIdentityForObject(functions[right]).String()
	})

	identities := make([]Identity, 0, len(functions))
	var canonical bytes.Buffer
	for _, function := range functions {
		identity := functionIdentityForObject(function)
		identities = append(identities, identity)
		canonical.WriteString(identity.String())
		canonical.WriteByte('\n')
		declaration := declarations[function]
		normalized := *declaration
		normalized.Doc = nil
		if err := format.Node(&canonical, token.NewFileSet(), &normalized); err != nil {
			return nil, nil, fmt.Errorf("canonicalize validator declaration %s: %w", identity, err)
		}
		canonical.WriteByte('\n')
		appendTypedASTFacts(&canonical, declaration, loadedPackage.TypesInfo)
	}
	return identities, canonical.Bytes(), nil
}

func appendTypedASTFacts(canonical *bytes.Buffer, declaration *ast.FuncDecl, info *types.Info) {
	ast.Inspect(declaration, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.Ident:
			object := info.Uses[node]
			if object == nil {
				object = info.Defs[node]
			}
			if object != nil {
				fmt.Fprintf(canonical, "object:%s\n", canonicalObjectIdentity(object))
			}
		case ast.Expr:
			if expressionType := info.TypeOf(node); expressionType != nil {
				fmt.Fprintf(canonical, "type:%s\n", canonicalTypeString(expressionType))
			}
		}
		return true
	})
}

func canonicalObjectIdentity(object types.Object) string {
	packagePath := ""
	if object.Pkg() != nil {
		packagePath = object.Pkg().Path()
	}
	return packagePath + "." + object.Name() + ":" + canonicalTypeString(object.Type())
}

func canonicalTypeString(valueType types.Type) string {
	return types.TypeString(valueType, func(typesPackage *types.Package) string {
		return typesPackage.Path()
	})
}

func functionDeclarations(loadedPackage *packages.Package) map[*types.Func]*ast.FuncDecl {
	result := make(map[*types.Func]*ast.FuncDecl)
	for _, file := range loadedPackage.Syntax {
		for _, declaration := range file.Decls {
			functionDeclaration, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			function, _ := loadedPackage.TypesInfo.Defs[functionDeclaration.Name].(*types.Func)
			if function != nil {
				result[function] = functionDeclaration
			}
		}
	}
	return result
}

func directlyCalledPackageFunctions(
	declaration *ast.FuncDecl,
	loadedPackage *packages.Package,
	declarations map[*types.Func]*ast.FuncDecl,
) []*types.Func {
	result := make([]*types.Func, 0)
	seen := make(map[*types.Func]struct{})
	ast.Inspect(declaration.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		function := calledFunction(call.Fun, loadedPackage.TypesInfo)
		if function == nil {
			return true
		}
		function = function.Origin()
		if function.Pkg() != loadedPackage.Types || declarations[function] == nil {
			return true
		}
		if _, exists := seen[function]; exists {
			return true
		}
		seen[function] = struct{}{}
		result = append(result, function)
		return true
	})
	sort.Slice(result, func(left, right int) bool {
		return functionIdentityForObject(result[left]).String() <
			functionIdentityForObject(result[right]).String()
	})
	return result
}

func calledFunction(expression ast.Expr, info *types.Info) *types.Func {
	switch expression := expression.(type) {
	case *ast.Ident:
		function, _ := info.Uses[expression].(*types.Func)
		return function
	case *ast.SelectorExpr:
		selection := info.Selections[expression]
		if selection == nil {
			function, _ := info.Uses[expression.Sel].(*types.Func)
			return function
		}
		function, _ := selection.Obj().(*types.Func)
		return function
	case *ast.IndexExpr:
		return calledFunction(expression.X, info)
	case *ast.IndexListExpr:
		return calledFunction(expression.X, info)
	case *ast.ParenExpr:
		return calledFunction(expression.X, info)
	default:
		return nil
	}
}

func functionIdentityForObject(function *types.Func) Identity {
	typeName := ""
	signature, _ := function.Type().(*types.Signature)
	if signature != nil && signature.Recv() != nil {
		receiverType := signature.Recv().Type()
		if pointer, ok := receiverType.(*types.Pointer); ok {
			receiverType = pointer.Elem()
		}
		if named, ok := receiverType.(*types.Named); ok {
			typeName = named.Obj().Name()
		}
	}
	packagePath := ""
	if function.Pkg() != nil {
		packagePath = function.Pkg().Path()
	}
	return Identity{
		PackagePath: packagePath,
		TypeName:    typeName,
		MemberName:  function.Name(),
		Kind:        IdentityFunction,
	}
}
