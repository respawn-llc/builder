package client

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	rpccontract "core/shared/apicontract"
)

type remoteRouteCall struct {
	connection rpccontract.ConnectionStrategy
	requestID  string
	methodName string
}

func TestRemoteClientRoutesMatchRPCContractConnectionStrategy(t *testing.T) {
	calls := remoteRouteCalls(t)
	for _, route := range rpccontract.Routes() {
		if route.Kind == rpccontract.KindNotification || route.Dependency == rpccontract.DependencyProtocol {
			continue
		}
		call, ok := calls[route.Method]
		if !ok {
			t.Fatalf("remote client missing binding for route %q", route.Method)
		}
		if call.connection != route.Connection {
			t.Fatalf("remote route %q connection = %q, want %q", route.Method, call.connection, route.Connection)
		}
		if route.Connection == rpccontract.ConnectionDedicated && call.requestID != route.DedicatedRequestID {
			t.Fatalf("remote route %q dedicated request id = %q, want %q", route.Method, call.requestID, route.DedicatedRequestID)
		}
	}
}

func TestRemoteClientRouteTypesMatchRPCContract(t *testing.T) {
	calls := remoteRouteCalls(t)
	remoteType := reflect.TypeOf((*Remote)(nil))
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	for _, route := range rpccontract.Routes() {
		if route.Kind == rpccontract.KindNotification || route.Dependency == rpccontract.DependencyProtocol {
			continue
		}
		call, ok := calls[route.Method]
		if !ok {
			t.Fatalf("remote client missing binding for route %q", route.Method)
		}
		method, ok := remoteType.MethodByName(call.methodName)
		if !ok {
			t.Fatalf("remote route %q references missing method %q", route.Method, call.methodName)
		}
		if method.Type.NumIn() < 3 {
			t.Fatalf("remote method %s has %d inputs, want receiver, context, request", method.Name, method.Type.NumIn())
		}
		if method.Type.In(1) != contextType {
			t.Fatalf("remote method %s input 1 = %v, want context.Context", method.Name, method.Type.In(1))
		}
		if method.Type.In(2) != route.RequestType {
			t.Fatalf("remote method %s request type = %v, want %v for route %q", method.Name, method.Type.In(2), route.RequestType, route.Method)
		}
		if method.Type.NumOut() == 0 || method.Type.Out(method.Type.NumOut()-1) != errorType {
			t.Fatalf("remote method %s must return error last, got %v", method.Name, method.Type)
		}
		if route.Kind == rpccontract.KindSubscription {
			continue
		}
		if route.ResponseType == reflect.TypeOf(struct{}{}) {
			if method.Type.NumOut() != 1 {
				t.Fatalf("remote method %s returns %d values for empty response route %q, want error only", method.Name, method.Type.NumOut(), route.Method)
			}
			continue
		}
		if method.Type.NumOut() != 2 || method.Type.Out(0) != route.ResponseType {
			t.Fatalf("remote method %s response type = %v, want %v for route %q", method.Name, method.Type, route.ResponseType, route.Method)
		}
	}
}

func TestLoopbackClientsExposeEveryRemoteRouteBinding(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	dir := filepath.Dir(filename)
	interfaceMethods := clientInterfaceMethods(t, dir)
	loopbackMethods := loopbackMethodNames(t, dir)
	loopbackServiceMethods := loopbackMethodServiceContractTypes(t, dir)
	for _, route := range rpccontract.Routes() {
		if route.Kind == rpccontract.KindNotification || route.Dependency == rpccontract.DependencyProtocol {
			continue
		}
		call := remoteRouteCalls(t)[route.Method]
		if _, ok := interfaceMethods[call.methodName]; !ok {
			t.Fatalf("route %q remote method %q missing from client interfaces", route.Method, call.methodName)
		}
		if _, ok := loopbackMethods[call.methodName]; !ok {
			t.Fatalf("route %q client method %q missing loopback binding", route.Method, call.methodName)
		}
		serviceType, ok := loopbackServiceMethods[call.methodName]
		if !ok {
			t.Fatalf("route %q client method %q loopback binding is not backed by shared/apicontract service contracts", route.Method, call.methodName)
		}
		if want := expectedLoopbackServiceContractType(route.Dependency); serviceType != want {
			t.Fatalf("route %q client method %q loopback service = %q, want %q", route.Method, call.methodName, serviceType, want)
		}
	}
}

func expectedLoopbackServiceContractType(dependency rpccontract.Dependency) string {
	switch dependency {
	case rpccontract.DependencyApprovalView:
		return "ApprovalViewService"
	case rpccontract.DependencyAttentionNotification:
		return "AttentionNotificationService"
	case rpccontract.DependencyAskView:
		return "AskViewService"
	case rpccontract.DependencyAuthBootstrap:
		return "AuthBootstrapService"
	case rpccontract.DependencyAuthStatus:
		return "AuthStatusService"
	case rpccontract.DependencyProcessControl:
		return "ProcessControlService"
	case rpccontract.DependencyProcessOutput:
		return "ProcessOutputService"
	case rpccontract.DependencyProcessView:
		return "ProcessViewService"
	case rpccontract.DependencyProjectView:
		return "ProjectViewService"
	case rpccontract.DependencyPromptActivity:
		return "PromptActivityService"
	case rpccontract.DependencyPromptControl:
		return "PromptControlService"
	case rpccontract.DependencyRunPrompt:
		return "RunPromptService"
	case rpccontract.DependencyServerStatus:
		return "ServerStatusService"
	case rpccontract.DependencyRuntimeControl:
		return "RuntimeControlService"
	case rpccontract.DependencySessionActivity:
		return "SessionActivityService"
	case rpccontract.DependencySessionLaunch:
		return "SessionLaunchService"
	case rpccontract.DependencySessionLifecycle:
		return "SessionLifecycleService"
	case rpccontract.DependencySessionRuntime:
		return "SessionRuntimeService"
	case rpccontract.DependencySessionView:
		return "SessionViewService"
	case rpccontract.DependencyWorktree:
		return "WorktreeService"
	case rpccontract.DependencyWorkflow:
		return "WorkflowService"
	default:
		return ""
	}
}

func remoteRouteCalls(t *testing.T) map[string]remoteRouteCall {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	dir := filepath.Dir(filename)
	methodValues := protocolMethodValues(t, dir)
	calls := map[string]remoteRouteCall{}
	for _, name := range []string{"remote.go", "remote_stream.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil || !isRemoteMethod(funcDecl) {
				continue
			}
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					receiver, _ := selector.X.(*ast.Ident)
					if receiver == nil || receiver.Name != "c" {
						return true
					}
					switch selector.Sel.Name {
					case "call":
						recordRemoteCall(calls, methodValues, call.Args, 1, "", rpccontract.ConnectionControl, funcDecl.Name.Name)
					case "callUnscoped":
						recordRemoteCall(calls, methodValues, call.Args, 1, "", rpccontract.ConnectionUnscoped, funcDecl.Name.Name)
					case "callDedicated":
						recordRemoteCall(calls, methodValues, call.Args, 2, stringArg(call.Args, 1), rpccontract.ConnectionDedicated, funcDecl.Name.Name)
					case "subscribeRPC":
						recordRemoteCall(calls, methodValues, call.Args, 1, stringArg(call.Args, 2), rpccontract.ConnectionSubscription, funcDecl.Name.Name)
					}
					return true
				}
				if ident := callIdent(call.Fun); ident != "" {
					switch ident {
					case "callRPC":
						recordRemoteCall(calls, methodValues, call.Args, 3, stringArg(call.Args, 2), rpccontract.ConnectionSubscription, funcDecl.Name.Name)
					case "callControlRPC", "callControlRPCNoResponse":
						recordRemoteCall(calls, methodValues, call.Args, 2, "", rpccontract.ConnectionControl, funcDecl.Name.Name)
					case "callDedicatedRPC", "callDedicatedRPCNoResponse":
						recordRemoteCall(calls, methodValues, call.Args, 3, stringArg(call.Args, 2), rpccontract.ConnectionDedicated, funcDecl.Name.Name)
					case "callUnscopedRPC", "callUnscopedRPCNoResponse":
						recordRemoteCall(calls, methodValues, call.Args, 2, "", rpccontract.ConnectionUnscoped, funcDecl.Name.Name)
					}
				}
				return true
			})
		}
		if name != "remote_stream.go" {
			continue
		}
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil || !isRemoteMethod(funcDecl) {
				continue
			}
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				lit, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "Method" {
						continue
					}
					method, ok := methodValue(methodValues, kv.Value)
					if ok {
						calls[method] = remoteRouteCall{connection: rpccontract.ConnectionProgress, methodName: funcDecl.Name.Name}
					}
				}
				return true
			})
		}
	}
	return calls
}

func callIdent(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.IndexExpr:
		if ident, ok := typed.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.IndexListExpr:
		if ident, ok := typed.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

func clientInterfaceMethods(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	serviceMethods := serviceContractInterfaceMethods(t, dir)
	methods := map[string]struct{}{}
	for _, file := range clientSourceFiles(t, dir) {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(typeSpec.Name.Name, "Client") {
					continue
				}
				if serviceName, ok := serviceContractSelectorName(typeSpec.Type); ok {
					for method := range serviceMethods[serviceName] {
						methods[method] = struct{}{}
					}
					continue
				}
				iface, ok := typeSpec.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				for _, method := range iface.Methods.List {
					if serviceName, ok := serviceContractSelectorName(method.Type); ok {
						for method := range serviceMethods[serviceName] {
							methods[method] = struct{}{}
						}
						continue
					}
					if len(method.Names) != 1 {
						continue
					}
					methods[method.Names[0].Name] = struct{}{}
				}
			}
		}
	}
	return methods
}

func serviceContractInterfaceMethods(t *testing.T, clientDir string) map[string]map[string]struct{} {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(clientDir, "..", "apicontract", "service_contracts.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse apicontract/service_contracts.go: %v", err)
	}
	out := map[string]map[string]struct{}{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			methods := map[string]struct{}{}
			for _, method := range iface.Methods.List {
				if len(method.Names) == 1 {
					methods[method.Names[0].Name] = struct{}{}
				}
			}
			out[typeSpec.Name.Name] = methods
		}
	}
	return out
}

func serviceContractSelectorName(expr ast.Expr) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "servicecontract" {
		return "", false
	}
	return selector.Sel.Name, true
}

func loopbackMethodNames(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	methods := map[string]struct{}{}
	for _, file := range clientSourceFiles(t, dir) {
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || len(funcDecl.Recv.List) != 1 {
				continue
			}
			receiver := receiverTypeName(funcDecl.Recv.List[0].Type)
			if !strings.HasPrefix(receiver, "loopback") {
				continue
			}
			methods[funcDecl.Name.Name] = struct{}{}
		}
	}
	return methods
}

func loopbackMethodServiceContractTypes(t *testing.T, dir string) map[string]string {
	t.Helper()
	serviceTypesByReceiver := map[string]string{}
	for _, file := range clientSourceFiles(t, dir) {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasPrefix(typeSpec.Name.Name, "loopback") {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					serviceType, ok := loopbackFieldServiceContractName(field)
					if !ok {
						continue
					}
					serviceTypesByReceiver[typeSpec.Name.Name] = serviceType
				}
			}
		}
	}
	methods := map[string]string{}
	for _, file := range clientSourceFiles(t, dir) {
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || len(funcDecl.Recv.List) != 1 {
				continue
			}
			receiver := receiverTypeName(funcDecl.Recv.List[0].Type)
			serviceType, ok := serviceTypesByReceiver[receiver]
			if !ok {
				continue
			}
			methods[funcDecl.Name.Name] = serviceType
		}
	}
	return methods
}

func loopbackFieldServiceContractName(field *ast.Field) (string, bool) {
	if len(field.Names) == 1 && field.Names[0].Name == "service" {
		return serviceContractSelectorName(field.Type)
	}
	if len(field.Names) != 0 {
		return "", false
	}
	index, ok := field.Type.(*ast.IndexExpr)
	if !ok {
		return "", false
	}
	ident, ok := index.X.(*ast.Ident)
	if !ok || ident.Name != "loopbackClient" {
		return "", false
	}
	return serviceContractSelectorName(index.Index)
}

func clientSourceFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read client dir: %v", err)
	}
	files := make([]*ast.File, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, "_test.go") || !strings.HasSuffix(name, ".go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return files
}

func receiverTypeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return receiverTypeName(typed.X)
	default:
		return ""
	}
}

func isRemoteMethod(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Recv == nil || len(decl.Recv.List) != 1 {
		return false
	}
	star, ok := decl.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Remote"
}

func recordRemoteCall(calls map[string]remoteRouteCall, methodValues map[string]string, args []ast.Expr, methodIndex int, requestID string, connection rpccontract.ConnectionStrategy, methodName string) {
	method, ok := methodArg(methodValues, args, methodIndex)
	if !ok {
		return
	}
	calls[method] = remoteRouteCall{connection: connection, requestID: requestID, methodName: methodName}
}

func methodArg(methodValues map[string]string, args []ast.Expr, index int) (string, bool) {
	if index < 0 || index >= len(args) {
		return "", false
	}
	return methodValue(methodValues, args[index])
}

func methodValue(methodValues map[string]string, expr ast.Expr) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "protocol" {
		return "", false
	}
	value, ok := methodValues[selector.Sel.Name]
	return value, ok
}

func stringArg(args []ast.Expr, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	lit, ok := args[index].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
}

func protocolMethodValues(t *testing.T, clientDir string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(clientDir, "..", "protocol", "handshake.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse protocol methods: %v", err)
	}
	values := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range valueSpec.Names {
				if index >= len(valueSpec.Values) {
					continue
				}
				lit, ok := valueSpec.Values[index].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote method %s: %v", name.Name, err)
				}
				values[name.Name] = value
			}
		}
	}
	return values
}
