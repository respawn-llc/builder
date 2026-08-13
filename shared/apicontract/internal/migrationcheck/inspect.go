// Package migrationcheck inspects the live legacy API contract while KENT-192
// migrates that contract to Protobuf. It is migration-only tooling.
package migrationcheck

import (
	"errors"
	"fmt"
	"go/types"
	"reflect"
	"sort"

	"core/shared/apicontract"

	"golang.org/x/tools/go/packages"
)

type IdentityKind string

const (
	IdentityType     IdentityKind = "type"
	IdentityField    IdentityKind = "field"
	IdentityVariable IdentityKind = "variable"
	IdentityFunction IdentityKind = "function"
)

type Identity struct {
	PackagePath string
	TypeName    string
	MemberName  string
	Kind        IdentityKind
}

func (i Identity) String() string {
	if i.TypeName == "" {
		return i.PackagePath + "." + i.MemberName
	}
	if i.MemberName == "" {
		return i.PackagePath + "." + i.TypeName
	}
	return i.PackagePath + "." + i.TypeName + "." + i.MemberName
}

type ResolvedIdentity struct {
	Identity Identity
	Object   types.Object
}

type NamedScalar struct {
	Identity  Identity
	Type      *types.TypeName
	Constants []*types.Const
}

type Validator struct {
	Identity    Identity
	Function    *types.Func
	Closure     []Identity
	Fingerprint string
}

type Report struct {
	Routes       []apicontract.Route
	Predecessors []ResolvedIdentity
	NamedScalars []NamedScalar
	Validators   []Validator
	WireFields   map[WireFieldObjectKey]*types.Var
}

type WireFieldObjectKey struct {
	PackagePath string
	TypeName    string
	FieldName   string
}

// InspectExecutionTarget derives all evidence from the packages and route
// values compiled in the current worktree. It deliberately does not consult
// Git history or retain a copied route/type inventory.
func InspectExecutionTarget() (Report, error) {
	routes := apicontract.Routes()
	reachable := collectReachableTypes(routes)
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
	for _, identity := range lockedPredecessorIdentities {
		if _, exists := seenPackagePaths[identity.PackagePath]; exists {
			continue
		}
		seenPackagePaths[identity.PackagePath] = struct{}{}
		packagePaths = append(packagePaths, identity.PackagePath)
	}
	sort.Strings(packagePaths)

	loaded, err := loadPackages(packagePaths)
	if err != nil {
		return Report{}, err
	}
	predecessors, err := resolveLockedPredecessors(loaded)
	if err != nil {
		return Report{}, err
	}
	scalars, validators, err := discoverDeclarations(reachable, loaded)
	if err != nil {
		return Report{}, err
	}
	if err := populateValidatorClosures(validators, loaded); err != nil {
		return Report{}, err
	}
	wireFields, err := resolveReachableWireFields(reachable, loaded)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Routes:       routes,
		Predecessors: predecessors,
		NamedScalars: scalars,
		Validators:   validators,
		WireFields:   wireFields,
	}, nil
}

func resolveReachableWireFields(
	reachable map[reflect.Type]struct{},
	loaded map[string]*packages.Package,
) (map[WireFieldObjectKey]*types.Var, error) {
	result := make(map[WireFieldObjectKey]*types.Var)
	for reflectedType := range reachable {
		reflectedType = dereferenceType(reflectedType)
		if reflectedType == nil || reflectedType.Kind() != reflect.Struct ||
			reflectedType.PkgPath() == "" || reflectedType.Name() == "" {
			continue
		}
		typeName := reflectedDeclarationName(reflectedType.Name())
		loadedPackage := loaded[reflectedType.PkgPath()]
		if loadedPackage == nil {
			return nil, fmt.Errorf("wire-field package is not loaded: %s", reflectedType.PkgPath())
		}
		for index := 0; index < reflectedType.NumField(); index++ {
			field := reflectedType.Field(index)
			if field.PkgPath != "" {
				continue
			}
			object, err := lookupField(loadedPackage, typeName, field.Name)
			if err != nil {
				return nil, err
			}
			key := WireFieldObjectKey{
				PackagePath: reflectedType.PkgPath(),
				TypeName:    typeName,
				FieldName:   field.Name,
			}
			if previous, duplicate := result[key]; duplicate {
				if previous != object {
					return nil, fmt.Errorf("wire field resolves ambiguously: %s.%s.%s", key.PackagePath, key.TypeName, key.FieldName)
				}
				continue
			}
			result[key] = object
		}
	}
	return result, nil
}

func collectReachableTypes(routes []apicontract.Route) map[reflect.Type]struct{} {
	roots := make([]reflect.Type, 0, len(routes)*4)
	for _, route := range routes {
		roots = append(roots, route.RequestType, route.ResponseType, route.EventType, route.CompleteType)
	}
	return collectReachableTypesFromRoots(roots...)
}

func collectReachableTypesFromRoots(rootTypes ...reflect.Type) map[reflect.Type]struct{} {
	reachable := make(map[reflect.Type]struct{})
	var visit func(reflect.Type)
	visit = func(reflectedType reflect.Type) {
		if reflectedType == nil {
			return
		}
		if _, exists := reachable[reflectedType]; exists {
			return
		}
		reachable[reflectedType] = struct{}{}
		switch reflectedType.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(reflectedType.Elem())
		case reflect.Map:
			visit(reflectedType.Key())
			visit(reflectedType.Elem())
		case reflect.Struct:
			for index := range reflectedType.NumField() {
				field := reflectedType.Field(index)
				if field.PkgPath == "" {
					visit(field.Type)
				}
			}
		}
	}
	for _, rootType := range rootTypes {
		visit(rootType)
	}
	return reachable
}

func loadPackages(packagePaths []string) (map[string]*packages.Package, error) {
	loadedPackages, err := packages.Load(&packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedDeps,
	}, packagePaths...)
	if err != nil {
		return nil, fmt.Errorf("load execution-target contract packages: %w", err)
	}
	result := make(map[string]*packages.Package)
	var packageErrors []error
	packages.Visit(loadedPackages, nil, func(loadedPackage *packages.Package) {
		if len(loadedPackage.Errors) > 0 {
			for _, packageError := range loadedPackage.Errors {
				packageErrors = append(packageErrors, errors.New(packageError.Error()))
			}
			return
		}
		if loadedPackage.Types != nil {
			result[loadedPackage.PkgPath] = loadedPackage
		}
	})
	if err := errors.Join(packageErrors...); err != nil {
		return nil, fmt.Errorf("load execution-target contract packages: %w", err)
	}
	return result, nil
}

func resolveLockedPredecessors(loaded map[string]*packages.Package) ([]ResolvedIdentity, error) {
	resolved := make([]ResolvedIdentity, 0, len(lockedPredecessorIdentities)+32)
	seen := make(map[Identity]struct{})
	add := func(identity Identity, object types.Object) error {
		if object == nil {
			return fmt.Errorf("locked predecessor identity does not resolve: %s", identity)
		}
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("locked predecessor identity resolves more than once: %s", identity)
		}
		seen[identity] = struct{}{}
		resolved = append(resolved, ResolvedIdentity{Identity: identity, Object: object})
		return nil
	}

	for _, identity := range lockedPredecessorIdentities {
		loadedPackage := loaded[identity.PackagePath]
		if loadedPackage == nil {
			return nil, fmt.Errorf("locked predecessor package is not loaded: %s", identity.PackagePath)
		}
		switch identity.Kind {
		case IdentityType:
			if err := add(identity, lookupTypeName(loadedPackage, identity.TypeName)); err != nil {
				return nil, err
			}
		case IdentityField:
			field, err := lookupField(loadedPackage, identity.TypeName, identity.MemberName)
			if err != nil {
				return nil, err
			}
			if err := add(identity, field); err != nil {
				return nil, err
			}
		case IdentityVariable:
			variable, _ := loadedPackage.Types.Scope().Lookup(identity.MemberName).(*types.Var)
			if err := add(identity, variable); err != nil {
				return nil, err
			}
		case IdentityFunction:
			function, _ := loadedPackage.Types.Scope().Lookup(identity.MemberName).(*types.Func)
			if err := add(identity, function); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported locked predecessor identity kind %q", identity.Kind)
		}
	}

	sort.Slice(resolved, func(left, right int) bool {
		return resolved[left].Identity.String() < resolved[right].Identity.String()
	})
	return resolved, nil
}

func lookupTypeName(loadedPackage *packages.Package, name string) *types.TypeName {
	if loadedPackage == nil {
		return nil
	}
	typeName, _ := loadedPackage.Types.Scope().Lookup(name).(*types.TypeName)
	return typeName
}

func lookupField(loadedPackage *packages.Package, typeName string, fieldName string) (*types.Var, error) {
	namedObject := lookupTypeName(loadedPackage, typeName)
	if namedObject == nil {
		return nil, fmt.Errorf("locked predecessor type does not resolve: %s.%s", loadedPackage.PkgPath, typeName)
	}
	structType, ok := namedObject.Type().Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("locked predecessor field owner is not a struct: %s.%s", loadedPackage.PkgPath, typeName)
	}
	var match *types.Var
	for index := range structType.NumFields() {
		field := structType.Field(index)
		if field.Name() != fieldName {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("locked predecessor field resolves more than once: %s.%s.%s", loadedPackage.PkgPath, typeName, fieldName)
		}
		match = field
	}
	if match == nil {
		return nil, fmt.Errorf("locked predecessor field does not resolve: %s.%s.%s", loadedPackage.PkgPath, typeName, fieldName)
	}
	return match, nil
}

func discoverDeclarations(reachable map[reflect.Type]struct{}, loaded map[string]*packages.Package) ([]NamedScalar, []Validator, error) {
	var scalars []NamedScalar
	var validators []Validator
	seenScalars := make(map[Identity]struct{})
	seenValidators := make(map[Identity]struct{})

	reflectedTypes := make([]reflect.Type, 0, len(reachable))
	for reflectedType := range reachable {
		reflectedTypes = append(reflectedTypes, reflectedType)
	}
	sort.Slice(reflectedTypes, func(left, right int) bool {
		leftKey := reflectedTypes[left].PkgPath() + "." + reflectedTypes[left].Name()
		rightKey := reflectedTypes[right].PkgPath() + "." + reflectedTypes[right].Name()
		return leftKey < rightKey
	})
	for _, reflectedType := range reflectedTypes {
		if reflectedType.Name() == "" || reflectedType.PkgPath() == "" {
			continue
		}
		declarationName := reflectedDeclarationName(reflectedType.Name())
		loadedPackage := loaded[reflectedType.PkgPath()]
		if loadedPackage == nil {
			return nil, nil, fmt.Errorf("route-reachable package is not loaded: %s", reflectedType.PkgPath())
		}
		typeName := lookupTypeName(loadedPackage, declarationName)
		if typeName == nil {
			return nil, nil, fmt.Errorf("route-reachable type declaration does not resolve: %s.%s", reflectedType.PkgPath(), reflectedType.Name())
		}
		if isScalarKind(reflectedType.Kind()) {
			identity := typeIdentity(reflectedType.PkgPath(), declarationName)
			if _, exists := seenScalars[identity]; !exists {
				seenScalars[identity] = struct{}{}
				scalars = append(scalars, NamedScalar{
					Identity:  identity,
					Type:      typeName,
					Constants: assignableConstants(loadedPackage, typeName.Type()),
				})
			}
		}
		for _, receiverType := range []types.Type{typeName.Type(), types.NewPointer(typeName.Type())} {
			methodSet := types.NewMethodSet(receiverType)
			for _, methodName := range []string{"Validate", "ValidateRPC"} {
				selection := methodSet.Lookup(loadedPackage.Types, methodName)
				if selection == nil {
					continue
				}
				function, ok := selection.Obj().(*types.Func)
				if !ok {
					return nil, nil, fmt.Errorf("validator declaration is not a function: %s.%s.%s", reflectedType.PkgPath(), reflectedType.Name(), methodName)
				}
				identity := Identity{
					PackagePath: reflectedType.PkgPath(),
					TypeName:    declarationName,
					MemberName:  methodName,
					Kind:        IdentityFunction,
				}
				if _, exists := seenValidators[identity]; exists {
					continue
				}
				seenValidators[identity] = struct{}{}
				validators = append(validators, Validator{Identity: identity, Function: function})
			}
		}
	}
	sort.Slice(scalars, func(left, right int) bool {
		return scalars[left].Identity.String() < scalars[right].Identity.String()
	})
	sort.Slice(validators, func(left, right int) bool {
		return validators[left].Identity.String() < validators[right].Identity.String()
	})
	return scalars, validators, nil
}

func reflectedDeclarationName(name string) string {
	end := 0
	for end < len(name) {
		character := name[end]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' {
			end++
			continue
		}
		break
	}
	return name[:end]
}

func isScalarKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Bool,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Float32,
		reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}

func assignableConstants(loadedPackage *packages.Package, scalarType types.Type) []*types.Const {
	var constants []*types.Const
	for _, name := range loadedPackage.Types.Scope().Names() {
		constant, ok := loadedPackage.Types.Scope().Lookup(name).(*types.Const)
		if !ok || isUntyped(constant.Type()) || !types.AssignableTo(constant.Type(), scalarType) {
			continue
		}
		constants = append(constants, constant)
	}
	sort.Slice(constants, func(left, right int) bool {
		leftPosition := loadedPackage.Fset.Position(constants[left].Pos())
		rightPosition := loadedPackage.Fset.Position(constants[right].Pos())
		if leftPosition.Filename != rightPosition.Filename {
			return leftPosition.Filename < rightPosition.Filename
		}
		if leftPosition.Line != rightPosition.Line {
			return leftPosition.Line < rightPosition.Line
		}
		return constants[left].Name() < constants[right].Name()
	})
	return constants
}

func isUntyped(valueType types.Type) bool {
	basic, ok := valueType.Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsUntyped != 0
}

func typeIdentity(packagePath string, typeName string) Identity {
	return Identity{PackagePath: packagePath, TypeName: typeName, Kind: IdentityType}
}

func fieldIdentity(packagePath string, typeName string, fieldName string) Identity {
	return Identity{PackagePath: packagePath, TypeName: typeName, MemberName: fieldName, Kind: IdentityField}
}

func variableIdentity(packagePath string, name string) Identity {
	return Identity{PackagePath: packagePath, MemberName: name, Kind: IdentityVariable}
}

func functionIdentity(packagePath string, name string) Identity {
	return Identity{PackagePath: packagePath, MemberName: name, Kind: IdentityFunction}
}

var lockedPredecessorIdentities = append(
	append(
		KENT554ProjectionIdentities(),
		KENT345ProjectionIdentities()...,
	),
	ProjectSchemaProjectionIdentities()...,
)
