package core_test

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

type currentNodeStructureFindingKind string

const (
	findingDuplicateControlIdentity       currentNodeStructureFindingKind = "duplicate_control_identity"
	findingSecondControllerImplementation currentNodeStructureFindingKind = "second_controller_implementation"
	findingControllerComposition          currentNodeStructureFindingKind = "controller_composition"
	findingForeignControllerMapIdentity   currentNodeStructureFindingKind = "foreign_controller_map_identity"
	findingSerializedExecutionAuthority   currentNodeStructureFindingKind = "serialized_execution_authority"
	findingWorkflowSessionMetadata        currentNodeStructureFindingKind = "workflow_session_metadata"
)

type currentNodeStructureFinding struct {
	kind     currentNodeStructureFindingKind
	position string
}

type currentNodeTypeIndex struct {
	packages map[string]*packages.Package
	named    map[string]*types.Named
}

type currentNodeCallSite struct {
	pkg  *packages.Package
	file *ast.File
	call *ast.CallExpr
}

func TestCurrentNodeControlGraphHasOneAuthority(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgs := testharness.LoadTypedPackages(t, repoRoot, false, "./...")
	findings := analyzeCurrentNodeGoStructure(pkgs)
	if len(findings) > 0 {
		t.Fatalf("Current Node authority structure violations:\n%s", formatCurrentNodeStructureFindings(findings))
	}
}

func TestCurrentNodeGoStructureGuardRejectsReintroducedAuthorities(t *testing.T) {
	t.Run("second controller implementation", func(t *testing.T) {
		pkgs := currentNodeStructureFixture(t, map[string]string{
			"server/rogue/controller.go": `package rogue

import "context"

type Controller struct{}

func (*Controller) CompleteCurrentNode(context.Context) error { return nil }
`,
		})
		assertCurrentNodeFinding(t, analyzeCurrentNodeGoStructure(pkgs), findingSecondControllerImplementation)
	})

	t.Run("separate durable task node branch identity", func(t *testing.T) {
		pkgs := currentNodeStructureFixture(t, map[string]string{
			"server/rogue/identity.go": `package rogue

import "core/server/workflow"

type ExecutionReference struct {
	TaskID workflow.TaskID
	NodeID workflow.NodeID
	Branch *workflow.TransitionBranchKey
}
`,
		})
		assertCurrentNodeFinding(t, analyzeCurrentNodeGoStructure(pkgs), findingDuplicateControlIdentity)
	})

	t.Run("opaque controller map identity", func(t *testing.T) {
		pkgs := currentNodeStructureFixture(t, map[string]string{
			"server/workflowexecution/attempt.go": `package workflowexecution

type AttemptID string
`,
			"server/workflowexecution/controller_extra.go": `package workflowexecution

func (*CurrentNodeController) rememberAttempt(AttemptID) {}
`,
		})
		controller := testharness.PackageByPath(t, pkgs, "core/server/workflowexecution")
		current := controller.Types.Scope().Lookup("CurrentNodeController").Type().Underlying().(*types.Struct)
		_ = current
		testharness.WriteFile(t, filepath.Join(currentNodeFixtureRoot(t, pkgs), "server/workflowexecution/controller.go"), currentNodeFixtureControllerSource("attempts map[AttemptID]struct{}"))
		pkgs = testharness.LoadTypedPackages(t, currentNodeFixtureRoot(t, pkgs), false, "./...")
		assertCurrentNodeFinding(t, analyzeCurrentNodeGoStructure(pkgs), findingForeignControllerMapIdentity)
	})

	t.Run("wire projection adds execution identity", func(t *testing.T) {
		pkgs := currentNodeStructureFixture(t, map[string]string{
			"shared/serverapi/workflow.go": `package serverapi

import "core/shared/runtimeids"

type WorkflowTaskCurrentNode struct {
	NodeID string ` + "`json:\"node_id\"`" + `
	TransitionBranchKey *string ` + "`json:\"transition_branch_key,omitempty\"`" + `
	SessionID *string ` + "`json:\"session_id,omitempty\"`" + `
	ExecutionID runtimeids.ExecutionScopeID ` + "`json:\"execution_id\"`" + `
}
`,
		})
		assertCurrentNodeFinding(t, analyzeCurrentNodeGoStructure(pkgs), findingSerializedExecutionAuthority)
	})

	t.Run("event payload carries workflow execution state", func(t *testing.T) {
		pkgs := currentNodeStructureFixture(t, map[string]string{
			"server/session/workflow_event.go": `package session

import "core/server/workflow"

type workflowEvent struct {
	CurrentNode workflow.CurrentNodeReference
}

func (workflowEvent) eventKind() string { return "workflow" }
`,
		})
		assertCurrentNodeFinding(t, analyzeCurrentNodeGoStructure(pkgs), findingSerializedExecutionAuthority)
	})

	t.Run("session metadata carries workflow control state", func(t *testing.T) {
		pkgs := currentNodeStructureFixture(t, map[string]string{
			"server/metadata/store.go": `package metadata

import "core/server/workflow"

type sessionMetadataDocument struct {
	CurrentNode workflow.CurrentNodeReference ` + "`json:\"current_node\"`" + `
}
`,
		})
		assertCurrentNodeFinding(t, analyzeCurrentNodeGoStructure(pkgs), findingWorkflowSessionMetadata)
	})
}

func analyzeCurrentNodeGoStructure(pkgs []*packages.Package) []currentNodeStructureFinding {
	index := indexCurrentNodeTypes(pkgs)
	var findings []currentNodeStructureFinding
	findings = append(findings, currentNodeIdentityFindings(index)...)
	findings = append(findings, currentNodeControllerFindings(index)...)
	findings = append(findings, currentNodeProductionCompositionFindings(index)...)
	findings = append(findings, currentNodeWireFindings(index)...)
	findings = append(findings, currentNodeEventFindings(index)...)
	findings = append(findings, currentNodeSessionMetadataFindings(index)...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].kind != findings[j].kind {
			return findings[i].kind < findings[j].kind
		}
		return findings[i].position < findings[j].position
	})
	return findings
}

func currentNodeProductionCompositionFindings(index currentNodeTypeIndex) []currentNodeStructureFinding {
	permit := index.named["core/server/workflowexecution.MutationPermit"]
	controller := index.named["core/server/workflowexecution.CurrentNodeController"]
	starter := index.named["core/server/workflowrunner.Starter"]
	authority := index.named["core/server/sessionruntime.Authority"]
	statusProjection := index.named["core/server/workflowview.TaskStatusProjection"]
	projectService := index.named["core/server/projectview.Service"]
	workflowService := index.named["core/server/workflowsvc.Service"]
	if permit == nil || controller == nil || starter == nil || authority == nil || statusProjection == nil || projectService == nil || workflowService == nil {
		return nil
	}

	calls := map[string][]currentNodeCallSite{}
	for _, pkg := range index.packages {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				function := calledFunction(pkg, call)
				if function == nil || function.Pkg() == nil {
					return true
				}
				key := function.Pkg().Path() + "." + function.Name()
				calls[key] = append(calls[key], currentNodeCallSite{pkg: pkg, file: file, call: call})
				return true
			})
		}
	}

	expectedCalls := []string{
		"core/server/workflowexecution.NewMutationPermit",
		"core/server/sessionruntime.NewAuthority",
		"core/server/workflowrunner.NewStarter",
		"core/server/workflowview.NewTaskStatusProjection",
		"core/server/workflowview.NewTaskDependencies",
		"core/server/core.bindTaskDependencies",
		"core/server/workflowsvc.New",
		"core/server/workflowview.NewTaskSearch",
		"core/server/projectview.WithWorkflowExecution",
		"core/server/workflowsvc.WithCurrentNodeExecution",
		"core/server/workflowexecution.Recover",
		"core/server/core.composeBundles",
	}
	var findings []currentNodeStructureFinding
	for _, key := range expectedCalls {
		if len(calls[key]) != 1 {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: fmt.Sprintf("%s production composition calls = %d, want 1", key, len(calls[key])),
			})
		}
	}
	controllerCalls := calls["core/server/workflowexecution.NewCurrentNodeController"]
	if len(controllerCalls) != 1 || len(findings) > 0 {
		return findings
	}

	permitType := types.NewPointer(permit)
	authorityType := types.NewPointer(authority)
	starterType := types.NewPointer(starter)
	controllerType := types.NewPointer(controller)
	statusProjectionType := types.NewPointer(statusProjection)
	permitObject := assignedVariableOfType(calls["core/server/workflowexecution.NewMutationPermit"][0], permitType)
	authorityObject := assignedVariableOfType(calls["core/server/sessionruntime.NewAuthority"][0], authorityType)
	starterObject := assignedVariableOfType(calls["core/server/workflowrunner.NewStarter"][0], starterType)
	controllerObject := assignedVariableOfType(controllerCalls[0], controllerType)
	statusProjectionObject := assignedVariableOfType(calls["core/server/workflowview.NewTaskStatusProjection"][0], statusProjectionType)
	for name, object := range map[string]*types.Var{
		"workflow mutation permit":        permitObject,
		"session runtime authority":       authorityObject,
		"workflow runtime starter":        starterObject,
		"workflow execution controller":   controllerObject,
		"workflow task status projection": statusProjectionObject,
	} {
		if object == nil {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: name + " constructor result must have one typed composition owner",
			})
		}
	}
	if len(findings) > 0 {
		return findings
	}

	for _, key := range []string{
		"core/server/workflowrunner.NewStarter",
		"core/server/workflowexecution.NewCurrentNodeController",
		"core/server/workflowsvc.New",
		"core/server/projectview.WithWorkflowExecution",
	} {
		if !callReferencesExactly(calls[key][0], permitType, permitObject) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: key + " must receive the one production workflow mutation permit",
			})
		}
	}
	for _, key := range []string{
		"core/server/workflowrunner.NewStarter",
		"core/server/workflowexecution.NewCurrentNodeController",
	} {
		if !callReferencesExactly(calls[key][0], authorityType, authorityObject) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: key + " must receive the one production Session runtime Authority",
			})
		}
	}
	if !callReferencesExactly(calls["core/server/workflowview.NewTaskStatusProjection"][0], controllerType, controllerObject) {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingControllerComposition,
			position: "NewTaskStatusProjection must receive the one production Workflow Execution controller",
		})
	}
	for _, key := range []string{
		"core/server/workflowview.NewTaskSearch",
		"core/server/workflowview.NewTaskList",
		"core/server/workflowview.NewBoard",
		"core/server/workflowview.NewTaskDetail",
	} {
		if len(calls[key]) != 1 || !callReferencesExactly(calls[key][0], statusProjectionType, statusProjectionObject) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: key + " must receive the one production TaskStatusProjection",
			})
		}
	}
	projectionStruct, projectionIsStruct := statusProjection.Underlying().(*types.Struct)
	if projectionIsStruct {
		if structDirectlyContains(projectionStruct, authorityType) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: "TaskStatusProjection must not directly own Session runtime Authority",
			})
		}
		if structHasCurrentTaskQuiescenceSource(projectionStruct) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: "TaskStatusProjection must not directly own TaskQuiescenceSource",
			})
		}
	}
	for _, surface := range []string{
		"core/server/workflowview.TaskSearch",
		"core/server/workflowview.TaskList",
		"core/server/workflowview.Board",
		"core/server/workflowview.TaskDetail",
	} {
		surfaceType := index.named[surface]
		if surfaceType == nil {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: surface + " production surface type is missing",
			})
			continue
		}
		structure, ok := surfaceType.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		if structDirectlyContains(structure, authorityType) || structHasCurrentTaskQuiescenceSource(structure) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: surface + " must not directly own live Authority or Quiescence",
			})
		}
	}
	if !callReferencesExactly(controllerCalls[0], starterType, starterObject) {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingControllerComposition,
			position: "NewCurrentNodeController must receive the one production Workflow Runner",
		})
	}
	for _, key := range []string{
		"core/server/sessionruntime.NewAuthority",
		"core/server/projectview.WithWorkflowExecution",
		"core/server/workflowsvc.WithCurrentNodeExecution",
		"core/server/workflowexecution.Recover",
	} {
		if !callReferencesExactly(calls[key][0], controllerType, controllerObject) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingControllerComposition,
				position: key + " must reference the one production Workflow Execution controller",
			})
		}
	}

	controllerPosition := controllerCalls[0].call.Pos()
	projectionPosition := calls["core/server/workflowview.NewTaskStatusProjection"][0].call.Pos()
	dependenciesPosition := calls["core/server/workflowview.NewTaskDependencies"][0].call.Pos()
	dependencyBindPosition := calls["core/server/core.bindTaskDependencies"][0].call.Pos()
	recoveryPosition := calls["core/server/workflowexecution.Recover"][0].call.Pos()
	projectWiringPosition := calls["core/server/projectview.WithWorkflowExecution"][0].call.Pos()
	servicePosition := calls["core/server/workflowsvc.New"][0].call.Pos()
	corePosition := calls["core/server/core.composeBundles"][0].call.Pos()
	if !(controllerPosition < projectionPosition &&
		projectionPosition < dependenciesPosition &&
		dependenciesPosition < dependencyBindPosition &&
		dependencyBindPosition < recoveryPosition &&
		recoveryPosition < projectWiringPosition &&
		projectWiringPosition < servicePosition &&
		servicePosition < corePosition) {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingControllerComposition,
			position: "shared lifecycle-aware Task dependencies must bind before Current Node recovery, workflow service wiring, and Core composition",
		})
	}
	return findings
}

func structHasCurrentTaskQuiescenceSource(structure *types.Struct) bool {
	for fieldIndex := 0; fieldIndex < structure.NumFields(); fieldIndex++ {
		fieldType := types.Unalias(structure.Field(fieldIndex).Type())
		if pointer, ok := fieldType.(*types.Pointer); ok {
			fieldType = types.Unalias(pointer.Elem())
		}
		if named, ok := fieldType.(*types.Named); ok {
			fieldType = types.Unalias(named.Underlying())
		}
		interfaceType, ok := fieldType.(*types.Interface)
		if !ok {
			continue
		}
		interfaceType.Complete()
		for methodIndex := 0; methodIndex < interfaceType.NumMethods(); methodIndex++ {
			if interfaceType.Method(methodIndex).Name() == "CurrentTaskQuiescence" {
				return true
			}
		}
	}
	return false
}

func indexCurrentNodeTypes(pkgs []*packages.Package) currentNodeTypeIndex {
	index := currentNodeTypeIndex{
		packages: make(map[string]*packages.Package),
		named:    make(map[string]*types.Named),
	}
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		index.packages[pkg.PkgPath] = pkg
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			object, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := types.Unalias(object.Type()).(*types.Named)
			if !ok {
				continue
			}
			index.named[pkg.PkgPath+"."+name] = named
		}
	}
	return index
}

func currentNodeIdentityFindings(index currentNodeTypeIndex) []currentNodeStructureFinding {
	taskID := index.named["core/server/workflow.TaskID"]
	nodeID := index.named["core/server/workflow.NodeID"]
	branchKey := index.named["core/server/workflow.TransitionBranchKey"]
	canonical := index.named["core/server/workflow.CurrentNodeReference"]
	referenceKey := index.named["core/server/workflow.CurrentNodeReferenceKey"]
	scriptIdentity := index.named["core/server/workflowscript.CurrentNodeIdentity"]
	if taskID == nil || nodeID == nil || branchKey == nil || canonical == nil || referenceKey == nil {
		return []currentNodeStructureFinding{{kind: findingDuplicateControlIdentity, position: "canonical Current Node identity types are missing"}}
	}
	keyInterface, _ := referenceKey.Underlying().(*types.Interface)
	if keyInterface != nil {
		keyInterface.Complete()
	}
	var findings []currentNodeStructureFinding
	for _, named := range index.named {
		if types.Identical(named, canonical) {
			continue
		}
		if scriptIdentity != nil && types.Identical(named, scriptIdentity) {
			continue
		}
		if keyInterface != nil &&
			(types.Implements(named, keyInterface) || types.Implements(types.NewPointer(named), keyInterface)) {
			continue
		}
		structure, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		if structDirectlyContains(structure, taskID) &&
			structDirectlyContains(structure, nodeID) &&
			structDirectlyContains(structure, branchKey) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingDuplicateControlIdentity,
				position: namedTypePosition(index, named),
			})
		}
	}
	return findings
}

func currentNodeControllerFindings(index currentNodeTypeIndex) []currentNodeStructureFinding {
	controllerInterface := index.named["core/server/workflowruntime.Controller"]
	canonicalController := index.named["core/server/workflowexecution.CurrentNodeController"]
	referenceKey := index.named["core/server/workflow.CurrentNodeReferenceKey"]
	scopeID := index.named["core/shared/runtimeids.ExecutionScopeID"]
	mutationPermit := index.named["core/server/workflowexecution.MutationPermit"]
	if controllerInterface == nil || canonicalController == nil || referenceKey == nil || scopeID == nil || mutationPermit == nil {
		return []currentNodeStructureFinding{{kind: findingControllerComposition, position: "canonical controller structure is missing"}}
	}
	interfaceType, ok := controllerInterface.Underlying().(*types.Interface)
	if !ok {
		return []currentNodeStructureFinding{{kind: findingControllerComposition, position: namedTypePosition(index, controllerInterface)}}
	}
	interfaceType.Complete()
	implementations := make([]*types.Named, 0)
	for _, named := range index.named {
		if _, isInterface := named.Underlying().(*types.Interface); isInterface {
			continue
		}
		if types.Implements(named, interfaceType) || types.Implements(types.NewPointer(named), interfaceType) {
			implementations = append(implementations, named)
		}
	}
	var findings []currentNodeStructureFinding
	for _, implementation := range implementations {
		if !types.Identical(implementation, canonicalController) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingSecondControllerImplementation,
				position: namedTypePosition(index, implementation),
			})
		}
	}
	if len(implementations) != 1 || !types.Identical(implementations[0], canonicalController) {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingControllerComposition,
			position: "workflowruntime.Controller must have exactly one concrete implementation",
		})
	}
	controllerStruct, ok := canonicalController.Underlying().(*types.Struct)
	if !ok || !structDirectlyContains(controllerStruct, types.NewPointer(mutationPermit)) {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingControllerComposition,
			position: namedTypePosition(index, canonicalController) + ": shared mutation permit field is missing",
		})
	}
	if ok {
		for fieldIndex := 0; fieldIndex < controllerStruct.NumFields(); fieldIndex++ {
			mapType, isMap := types.Unalias(controllerStruct.Field(fieldIndex).Type()).(*types.Map)
			if !isMap {
				continue
			}
			key := types.Unalias(mapType.Key())
			if types.Identical(key, referenceKey) || types.Identical(key, scopeID) {
				continue
			}
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingForeignControllerMapIdentity,
				position: namedTypePosition(index, canonicalController) + "." + controllerStruct.Field(fieldIndex).Name(),
			})
		}
	}
	constructorCalls := 0
	for _, pkg := range index.packages {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				function := calledFunction(pkg, call)
				if function != nil &&
					function.Pkg() != nil &&
					function.Pkg().Path() == "core/server/workflowexecution" &&
					function.Name() == "NewCurrentNodeController" {
					constructorCalls++
				}
				return true
			})
		}
	}
	if constructorCalls != 1 {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingControllerComposition,
			position: fmt.Sprintf("NewCurrentNodeController production composition calls = %d, want 1", constructorCalls),
		})
	}
	return findings
}

func currentNodeWireFindings(index currentNodeTypeIndex) []currentNodeStructureFinding {
	dto := index.named["core/shared/serverapi.WorkflowTaskCurrentNode"]
	if dto == nil {
		return []currentNodeStructureFinding{{kind: findingSerializedExecutionAuthority, position: "WorkflowTaskCurrentNode is missing"}}
	}
	structure, ok := dto.Underlying().(*types.Struct)
	if !ok {
		return []currentNodeStructureFinding{{kind: findingSerializedExecutionAuthority, position: namedTypePosition(index, dto)}}
	}
	const expectedCurrentNodeWireFingerprint = "effective_assignee,omitempty:*string;effective_thinking,omitempty:*string;node_id:string;session_id,omitempty:*string;transition_branch_key,omitempty:*string"
	if got := jsonStructFingerprint(structure); got != expectedCurrentNodeWireFingerprint {
		return []currentNodeStructureFinding{{
			kind:     findingSerializedExecutionAuthority,
			position: namedTypePosition(index, dto) + ": " + got,
		}}
	}
	workflowService := index.named["core/shared/apicontract.WorkflowService"]
	if workflowService == nil {
		return []currentNodeStructureFinding{{kind: findingSerializedExecutionAuthority, position: "WorkflowService is missing"}}
	}
	service, ok := workflowService.Underlying().(*types.Interface)
	if !ok {
		return []currentNodeStructureFinding{{kind: findingSerializedExecutionAuthority, position: namedTypePosition(index, workflowService)}}
	}
	service.Complete()
	reachable := make(map[*types.Named]struct{})
	for methodIndex := 0; methodIndex < service.NumMethods(); methodIndex++ {
		collectNamedTypes(service.Method(methodIndex).Type(), reachable, make(map[types.Type]bool))
	}
	var serializedShapes []string
	for named := range reachable {
		structure, ok := named.Underlying().(*types.Struct)
		if !ok || !typeGraphContains(named, dto, make(map[types.Type]bool)) {
			continue
		}
		serializedShapes = append(serializedShapes, named.Obj().Pkg().Path()+"."+named.Obj().Name()+"{"+jsonStructFingerprint(structure)+"}")
	}
	sort.Strings(serializedShapes)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(serializedShapes, "\n"))))
	const expectedWorkflowCurrentNodeWireDigest = "adf4a27f3ae974b79d5f784c5ad4b24605e057173e33f4616482fc4d8a8efa3e"
	if digest != expectedWorkflowCurrentNodeWireDigest {
		return []currentNodeStructureFinding{{
			kind:     findingSerializedExecutionAuthority,
			position: "Workflow Current Node wire digest " + digest + "\n" + strings.Join(serializedShapes, "\n"),
		}}
	}
	return nil
}

func currentNodeEventFindings(index currentNodeTypeIndex) []currentNodeStructureFinding {
	payload := index.named["core/server/session.EventRecordPayload"]
	if payload == nil {
		return []currentNodeStructureFinding{{kind: findingSerializedExecutionAuthority, position: "EventRecordPayload is missing"}}
	}
	payloadInterface, ok := payload.Underlying().(*types.Interface)
	if !ok {
		return []currentNodeStructureFinding{{kind: findingSerializedExecutionAuthority, position: namedTypePosition(index, payload)}}
	}
	payloadInterface.Complete()
	var findings []currentNodeStructureFinding
	for _, named := range index.named {
		if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "core/server/session" {
			continue
		}
		if !types.Implements(named, payloadInterface) && !types.Implements(types.NewPointer(named), payloadInterface) {
			continue
		}
		if typeGraphContainsWorkflowAuthority(named, make(map[types.Type]bool)) {
			findings = append(findings, currentNodeStructureFinding{
				kind:     findingSerializedExecutionAuthority,
				position: namedTypePosition(index, named),
			})
		}
	}
	return findings
}

func currentNodeSessionMetadataFindings(index currentNodeTypeIndex) []currentNodeStructureFinding {
	document := index.named["core/server/metadata.sessionMetadataDocument"]
	if document == nil {
		return []currentNodeStructureFinding{{kind: findingWorkflowSessionMetadata, position: "typed session metadata document is missing"}}
	}
	var findings []currentNodeStructureFinding
	if typeGraphContainsWorkflowAuthority(document, make(map[types.Type]bool)) {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingWorkflowSessionMetadata,
			position: namedTypePosition(index, document),
		})
	}
	metadataPackage := index.packages["core/server/metadata"]
	typedWrite := false
	if metadataPackage != nil {
		for _, file := range metadataPackage.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				function := calledFunction(metadataPackage, call)
				if function == nil || function.Pkg() == nil ||
					function.Pkg().Path() != "core/server/metadata" ||
					function.Name() != "marshalJSON" ||
					len(call.Args) != 1 {
					return true
				}
				if types.Identical(types.Unalias(metadataPackage.TypesInfo.TypeOf(call.Args[0])), document) {
					typedWrite = true
				}
				return true
			})
		}
	}
	if !typedWrite {
		findings = append(findings, currentNodeStructureFinding{
			kind:     findingWorkflowSessionMetadata,
			position: "session metadata new writes must serialize sessionMetadataDocument",
		})
	}
	return findings
}

func structDirectlyContains(structure *types.Struct, want types.Type) bool {
	for index := 0; index < structure.NumFields(); index++ {
		if typeContainsDirect(structure.Field(index).Type(), want) {
			return true
		}
	}
	return false
}

func typeContainsDirect(candidate, want types.Type) bool {
	candidate = types.Unalias(candidate)
	want = types.Unalias(want)
	if types.Identical(candidate, want) {
		return true
	}
	switch typed := candidate.(type) {
	case *types.Pointer:
		return types.Identical(types.Unalias(typed.Elem()), want)
	case *types.Slice:
		return types.Identical(types.Unalias(typed.Elem()), want)
	case *types.Array:
		return types.Identical(types.Unalias(typed.Elem()), want)
	default:
		return false
	}
}

func collectNamedTypes(candidate types.Type, out map[*types.Named]struct{}, seen map[types.Type]bool) {
	candidate = types.Unalias(candidate)
	if candidate == nil || seen[candidate] {
		return
	}
	seen[candidate] = true
	switch typed := candidate.(type) {
	case *types.Named:
		out[typed] = struct{}{}
		collectNamedTypes(typed.Underlying(), out, seen)
	case *types.Pointer:
		collectNamedTypes(typed.Elem(), out, seen)
	case *types.Slice:
		collectNamedTypes(typed.Elem(), out, seen)
	case *types.Array:
		collectNamedTypes(typed.Elem(), out, seen)
	case *types.Map:
		collectNamedTypes(typed.Key(), out, seen)
		collectNamedTypes(typed.Elem(), out, seen)
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			collectNamedTypes(typed.Field(index).Type(), out, seen)
		}
	case *types.Signature:
		collectNamedTypes(typed.Params(), out, seen)
		collectNamedTypes(typed.Results(), out, seen)
	case *types.Tuple:
		for index := 0; index < typed.Len(); index++ {
			collectNamedTypes(typed.At(index).Type(), out, seen)
		}
	case *types.Interface:
		typed.Complete()
		for index := 0; index < typed.NumMethods(); index++ {
			collectNamedTypes(typed.Method(index).Type(), out, seen)
		}
	}
}

func typeGraphContains(candidate, want types.Type, seen map[types.Type]bool) bool {
	candidate = types.Unalias(candidate)
	want = types.Unalias(want)
	if types.Identical(candidate, want) {
		return true
	}
	if candidate == nil || seen[candidate] {
		return false
	}
	seen[candidate] = true
	switch typed := candidate.(type) {
	case *types.Named:
		return typeGraphContains(typed.Underlying(), want, seen)
	case *types.Pointer:
		return typeGraphContains(typed.Elem(), want, seen)
	case *types.Slice:
		return typeGraphContains(typed.Elem(), want, seen)
	case *types.Array:
		return typeGraphContains(typed.Elem(), want, seen)
	case *types.Map:
		return typeGraphContains(typed.Key(), want, seen) || typeGraphContains(typed.Elem(), want, seen)
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			if typeGraphContains(typed.Field(index).Type(), want, seen) {
				return true
			}
		}
	case *types.Signature:
		return typeGraphContains(typed.Params(), want, seen) || typeGraphContains(typed.Results(), want, seen)
	case *types.Tuple:
		for index := 0; index < typed.Len(); index++ {
			if typeGraphContains(typed.At(index).Type(), want, seen) {
				return true
			}
		}
	}
	return false
}

func typeGraphContainsWorkflowAuthority(candidate types.Type, seen map[types.Type]bool) bool {
	candidate = types.Unalias(candidate)
	if candidate == nil || seen[candidate] {
		return false
	}
	seen[candidate] = true
	if named, ok := candidate.(*types.Named); ok {
		object := named.Obj()
		if object.Pkg() != nil {
			switch object.Pkg().Path() {
			case "core/server/workflow", "core/server/workflowexecution", "core/server/workflowruntime":
				return true
			case "core/server/sessionruntime":
				if object.Name() == "WorkflowExecutionRef" || object.Name() == "ExecutionScope" {
					return true
				}
			case "core/shared/runtimeids":
				if object.Name() == "ExecutionScopeID" {
					return true
				}
			}
		}
		return typeGraphContainsWorkflowAuthority(named.Underlying(), seen)
	}
	switch typed := candidate.(type) {
	case *types.Pointer:
		return typeGraphContainsWorkflowAuthority(typed.Elem(), seen)
	case *types.Slice:
		return typeGraphContainsWorkflowAuthority(typed.Elem(), seen)
	case *types.Array:
		return typeGraphContainsWorkflowAuthority(typed.Elem(), seen)
	case *types.Map:
		return typeGraphContainsWorkflowAuthority(typed.Key(), seen) || typeGraphContainsWorkflowAuthority(typed.Elem(), seen)
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			if typeGraphContainsWorkflowAuthority(typed.Field(index).Type(), seen) {
				return true
			}
		}
	}
	return false
}

func jsonStructFingerprint(structure *types.Struct) string {
	fields := make([]string, 0, structure.NumFields())
	for index := 0; index < structure.NumFields(); index++ {
		tag := reflectStructTagJSON(structure.Tag(index))
		if tag == "-" || tag == "" {
			continue
		}
		fields = append(fields, tag+":"+types.TypeString(structure.Field(index).Type(), func(*types.Package) string { return "" }))
	}
	sort.Strings(fields)
	return strings.Join(fields, ";")
}

func reflectStructTagJSON(tag string) string {
	for _, field := range strings.Fields(tag) {
		if !strings.HasPrefix(field, `json:"`) || !strings.HasSuffix(field, `"`) {
			continue
		}
		return strings.TrimSuffix(strings.TrimPrefix(field, `json:"`), `"`)
	}
	return ""
}

func calledFunction(pkg *packages.Package, call *ast.CallExpr) *types.Func {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		typed, _ := pkg.TypesInfo.Uses[function].(*types.Func)
		return typed
	case *ast.SelectorExpr:
		typed, _ := pkg.TypesInfo.Uses[function.Sel].(*types.Func)
		return typed
	default:
		return nil
	}
}

func assignedVariableOfType(site currentNodeCallSite, want types.Type) *types.Var {
	var matches []*types.Var
	record := func(expression ast.Expr) {
		identifier, ok := expression.(*ast.Ident)
		if !ok {
			return
		}
		variable, ok := site.pkg.TypesInfo.ObjectOf(identifier).(*types.Var)
		if !ok || !types.Identical(types.Unalias(variable.Type()), types.Unalias(want)) {
			return
		}
		matches = append(matches, variable)
	}
	ast.Inspect(site.file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			containsCall := false
			for _, expression := range typed.Rhs {
				if expression == site.call {
					containsCall = true
					break
				}
			}
			if containsCall {
				for _, expression := range typed.Lhs {
					record(expression)
				}
			}
		case *ast.ValueSpec:
			containsCall := false
			for _, expression := range typed.Values {
				if expression == site.call {
					containsCall = true
					break
				}
			}
			if containsCall {
				for _, identifier := range typed.Names {
					record(identifier)
				}
			}
		}
		return true
	})
	if len(matches) != 1 {
		return nil
	}
	return matches[0]
}

func callReferencesExactly(site currentNodeCallSite, want types.Type, expected *types.Var) bool {
	matches := make(map[*types.Var]struct{})
	ast.Inspect(site.call, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		variable, ok := site.pkg.TypesInfo.ObjectOf(identifier).(*types.Var)
		if ok && !variable.IsField() && types.Identical(types.Unalias(variable.Type()), types.Unalias(want)) {
			matches[variable] = struct{}{}
		}
		return true
	})
	if len(matches) != 1 {
		return false
	}
	_, exists := matches[expected]
	return exists
}

func namedTypePosition(index currentNodeTypeIndex, named *types.Named) string {
	if named == nil || named.Obj() == nil {
		return "<unknown>"
	}
	pkg := index.packages[named.Obj().Pkg().Path()]
	if pkg == nil {
		return named.Obj().Pkg().Path() + "." + named.Obj().Name()
	}
	return testharness.SourcePosition(pkg, named.Obj().Pos()).String()
}

func formatCurrentNodeStructureFindings(findings []currentNodeStructureFinding) string {
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		lines = append(lines, string(finding.kind)+": "+finding.position)
	}
	return strings.Join(lines, "\n")
}

func assertCurrentNodeFinding(t *testing.T, findings []currentNodeStructureFinding, want currentNodeStructureFindingKind) {
	t.Helper()
	for _, finding := range findings {
		if finding.kind == want {
			return
		}
	}
	t.Fatalf("findings =\n%s\nwant category %s", formatCurrentNodeStructureFindings(findings), want)
}

func currentNodeStructureFixture(t *testing.T, replacements map[string]string) []*packages.Package {
	t.Helper()
	root := t.TempDir()
	testharness.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	files := map[string]string{
		"server/workflow/types.go": `package workflow

type TaskID string
type NodeID string
type TransitionBranchKey string

type CurrentNodeReference struct {
	TaskID TaskID
	NodeID NodeID
	Branch *TransitionBranchKey
}

type CurrentNodeReferenceKey interface{ currentNodeReferenceKey() }
`,
		"shared/runtimeids/types.go": `package runtimeids

type ExecutionScopeID string
`,
		"server/workflowruntime/controller.go": `package workflowruntime

import "context"

type Controller interface {
	CompleteCurrentNode(context.Context) error
}
`,
		"server/workflowexecution/controller.go": currentNodeFixtureControllerSource(""),
		"server/core/composition.go": `package core

import "core/server/workflowexecution"

func compose() {
	workflowexecution.NewCurrentNodeController(&workflowexecution.MutationPermit{})
}
`,
		"shared/serverapi/workflow.go": `package serverapi

type WorkflowTaskCurrentNode struct {
	NodeID string ` + "`json:\"node_id\"`" + `
	TransitionBranchKey *string ` + "`json:\"transition_branch_key,omitempty\"`" + `
	SessionID *string ` + "`json:\"session_id,omitempty\"`" + `
}
`,
		"shared/apicontract/workflow.go": `package apicontract

import "core/shared/serverapi"

type WorkflowService interface {
	Current() serverapi.WorkflowTaskCurrentNode
}
`,
		"server/session/event.go": `package session

type EventRecordPayload interface {
	eventKind() string
}

type messageEvent struct{}
func (messageEvent) eventKind() string { return "message" }
`,
		"server/metadata/store.go": `package metadata

import "encoding/json"

type sessionMetadataDocument struct {
	WorkspaceRoot string ` + "`json:\"workspace_root\"`" + `
}

func marshalJSON(any) {}
func write() { marshalJSON(sessionMetadataDocument{}) }

var _ = json.Valid
`,
	}
	for path, source := range replacements {
		files[path] = source
	}
	for path, source := range files {
		testharness.WriteFile(t, filepath.Join(root, path), source)
	}
	return testharness.LoadTypedPackages(t, root, false, "./...")
}

func currentNodeFixtureControllerSource(extraField string) string {
	return `package workflowexecution

import (
	"context"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type MutationPermit struct{}

type CurrentNodeController struct {
	permit *MutationPermit
	gates map[workflow.CurrentNodeReferenceKey]struct{}
	live map[runtimeids.ExecutionScopeID]struct{}
	` + extraField + `
}

func NewCurrentNodeController(permit *MutationPermit) *CurrentNodeController {
	return &CurrentNodeController{permit: permit}
}

func (*CurrentNodeController) CompleteCurrentNode(context.Context) error { return nil }
`
}

func currentNodeFixtureRoot(t *testing.T, pkgs []*packages.Package) string {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.Module != nil && pkg.Module.Dir != "" {
			return pkg.Module.Dir
		}
	}
	t.Fatal("fixture module root is missing")
	return ""
}
