package registry

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"testing"

	"core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

type transcriptOwnershipFinding string

const (
	transcriptBrokerBypass   transcriptOwnershipFinding = "broker_bypass"
	transcriptSequenceBypass transcriptOwnershipFinding = "sequence_bypass"
)

func TestTranscriptPublicationHasOneSequencingOwner(t *testing.T) {
	pkgs := testsetup.LoadTypedPackages(t, testsetup.RepositoryRoot(t), false, "./server/registry", "./server/runtimeview")
	if findings := transcriptOwnershipFindings(pkgs); len(findings) != 0 {
		t.Fatalf("transcript ownership violations: %v", findings)
	}
}

func TestTranscriptOwnershipAnalyzerRejectsBypasses(t *testing.T) {
	root := t.TempDir()
	testsetup.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testsetup.WriteFile(t, filepath.Join(root, "shared/clientui/types.go"), `package clientui
type TranscriptEvent struct{}
type TranscriptMessage struct{}
func NewTranscriptMessage(int, TranscriptEvent) TranscriptMessage { return TranscriptMessage{} }
`)
	testsetup.WriteFile(t, filepath.Join(root, "server/registry/broker.go"), `package registry
import "core/shared/clientui"
type transcriptSubscriptionBroker struct{}
func (*transcriptSubscriptionBroker) Publish([]clientui.TranscriptEvent) {}
`)
	testsetup.WriteFile(t, filepath.Join(root, "server/registry/bad.go"), `package registry
import "core/shared/clientui"
var publish = (*transcriptSubscriptionBroker).Publish
var message = clientui.NewTranscriptMessage
func bad(b *transcriptSubscriptionBroker) {
	publish(b, nil)
	_ = clientui.TranscriptMessage{}
}
`)
	pkgs := testsetup.LoadTypedPackages(t, root, false, "./server/registry")
	got := map[transcriptOwnershipFinding]bool{}
	for _, finding := range transcriptOwnershipFindings(pkgs) {
		got[finding] = true
	}
	for _, want := range []transcriptOwnershipFinding{transcriptBrokerBypass, transcriptSequenceBypass} {
		if !got[want] {
			t.Errorf("findings = %v, want %s", got, want)
		}
	}
}

func transcriptOwnershipFindings(pkgs []*packages.Package) []transcriptOwnershipFinding {
	var findings []transcriptOwnershipFinding
	for _, pkg := range pkgs {
		clientUI := pkg.Imports["core/shared/clientui"]
		var messageType types.Type
		if clientUI != nil {
			messageType = clientUI.Types.Scope().Lookup("TranscriptMessage").Type()
		}
		for _, file := range pkg.Syntax {
			for _, declaration := range file.Decls {
				owner := ""
				if function, ok := declaration.(*ast.FuncDecl); ok {
					object, _ := pkg.TypesInfo.Defs[function.Name].(*types.Func)
					if object != nil && object.Pkg().Path() == pkg.PkgPath {
						owner = receiverTypeName(object)
					}
				}
				ast.Inspect(declaration, func(node ast.Node) bool {
					if literal, ok := node.(*ast.CompositeLit); ok &&
						types.Identical(pkg.TypesInfo.TypeOf(literal), messageType) &&
						owner != "transcriptSubscription" {
						findings = append(findings, transcriptSequenceBypass)
					}
					identifier, ok := node.(*ast.Ident)
					if !ok {
						return true
					}
					reference, ok := pkg.TypesInfo.Uses[identifier].(*types.Func)
					if !ok || reference.Pkg() == nil {
						return true
					}
					if reference.Pkg().Path() == "core/shared/clientui" && reference.Name() == "NewTranscriptMessage" &&
						owner != "transcriptSubscription" {
						findings = append(findings, transcriptSequenceBypass)
					}
					if reference.Pkg().Path() == pkg.PkgPath &&
						(reference.Name() == "Publish" || reference.Name() == "Subscribe") &&
						receiverTypeName(reference) == "transcriptSubscriptionBroker" &&
						owner != "sessionFeedSequencer" {
						findings = append(findings, transcriptBrokerBypass)
					}
					return true
				})
			}
		}
	}
	return findings
}

func receiverTypeName(function *types.Func) string {
	signature, _ := function.Type().(*types.Signature)
	if signature == nil || signature.Recv() == nil {
		return ""
	}
	typ := types.Unalias(signature.Recv().Type())
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	return typ.(*types.Named).Obj().Name()
}
