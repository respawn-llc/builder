package toolfixture

import (
	"core/server/tools"
	"core/shared/jsoncontract"
)

func NewRegistry(t TestingT, registrations ...tools.HandlerRegistration) *tools.Registry {
	t.Helper()
	if len(registrations) == 0 {
		return tools.NewRegistry()
	}
	contracts, err := tools.NewStaticToolContracts(
		jsoncontract.NewPreparer(false),
		staticContractSources()...,
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
