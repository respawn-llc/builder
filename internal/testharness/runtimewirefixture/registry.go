package runtimewirefixture

import (
	"core/server/tools"
	edittool "core/server/tools/edit"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	shelltool "core/server/tools/shell"
	"core/shared/jsoncontract"
)

type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

func NewToolRegistry(t TestingT, registrations ...tools.HandlerRegistration) *tools.Registry {
	t.Helper()
	if len(registrations) == 0 {
		return tools.NewRegistry()
	}
	contracts, err := tools.NewStaticToolContracts(
		jsoncontract.NewPreparer(false),
		shelltool.ExecCommandStaticContractSource(),
		shelltool.WriteStdinStaticContractSource(),
		readimagetool.StaticContractSource(),
		patchtool.StaticContractSource(),
		edittool.StaticContractSource(),
		tools.AskQuestionStaticContractSource(),
		tools.TriggerHandoffStaticContractSource(),
		tools.WebSearchStaticContractSource(),
	)
	if err != nil {
		t.Fatalf("prepare test static tool contracts: %v", err)
	}
	registry, err := tools.NewStaticToolRegistry(contracts, registrations...)
	if err != nil {
		t.Fatalf("create test static tool registry: %v", err)
	}
	return registry
}
