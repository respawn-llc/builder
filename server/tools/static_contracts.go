package tools

import (
	"errors"
	"fmt"

	"core/shared/jsoncontract"
	"core/shared/toolspec"

	invjsonschema "github.com/invopop/jsonschema"
)

var errStaticToolContractPreparation = errors.New("static tool contract preparation failed")

type InputAliases struct {
	Canonical string
	Aliases   []string
}

type StaticContractSource struct {
	ID      toolspec.ID
	Input   any
	Aliases []InputAliases
}

type staticToolContract struct {
	schema     jsoncontract.Function
	projection []jsoncontract.ObjectField
}

type StaticToolContracts struct {
	ingress jsoncontract.Function
	byID    map[toolspec.ID]staticToolContract
}

var ordinaryStaticToolIDs = []toolspec.ID{
	toolspec.ToolExecCommand,
	toolspec.ToolWriteStdin,
	toolspec.ToolViewImage,
	toolspec.ToolPatch,
	toolspec.ToolEdit,
	toolspec.ToolAskQuestion,
	toolspec.ToolTriggerHandoff,
	toolspec.ToolWebSearch,
}

func NewStaticToolContracts(
	preparer jsoncontract.Preparer,
	sources ...StaticContractSource,
) (StaticToolContracts, error) {
	ingress, err := preparer.Function(
		"ordinary tool input ingress",
		struct{}{},
		jsoncontract.Customize(func(schema *invjsonschema.Schema) error {
			schema.AdditionalProperties = invjsonschema.TrueSchema
			return nil
		}),
	)
	if err != nil {
		return StaticToolContracts{}, fmt.Errorf("%w: prepare ingress: %w", errStaticToolContractPreparation, err)
	}

	expected := make(map[toolspec.ID]struct{}, len(ordinaryStaticToolIDs))
	for _, id := range ordinaryStaticToolIDs {
		expected[id] = struct{}{}
	}
	contracts := make(map[toolspec.ID]staticToolContract, len(sources))
	for _, source := range sources {
		if _, ok := expected[source.ID]; !ok {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: tool %q is not an ordinary static tool",
				errStaticToolContractPreparation,
				source.ID,
			)
		}
		if source.Input == nil {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: tool %q input source is required",
				errStaticToolContractPreparation,
				source.ID,
			)
		}
		if _, exists := contracts[source.ID]; exists {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: duplicate tool %q",
				errStaticToolContractPreparation,
				source.ID,
			)
		}
		schema, err := preparer.Function("ordinary tool "+string(source.ID), source.Input)
		if err != nil {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: tool %q: %w",
				errStaticToolContractPreparation,
				source.ID,
				err,
			)
		}
		aliases := make(map[string][]string, len(source.Aliases))
		for _, alias := range source.Aliases {
			aliases[alias.Canonical] = append([]string(nil), alias.Aliases...)
		}
		fields := schema.Fields()
		projection := make([]jsoncontract.ObjectField, 0, len(fields))
		for _, field := range fields {
			projection = append(projection, jsoncontract.ObjectField{
				Name:    field,
				Aliases: aliases[field],
			})
			delete(aliases, field)
		}
		if len(aliases) != 0 {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: tool %q aliases target an unadvertised field",
				errStaticToolContractPreparation,
				source.ID,
			)
		}
		contracts[source.ID] = staticToolContract{schema: schema, projection: projection}
	}
	for _, id := range ordinaryStaticToolIDs {
		if _, ok := contracts[id]; !ok {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: tool %q contract is missing",
				errStaticToolContractPreparation,
				id,
			)
		}
	}
	return StaticToolContracts{ingress: ingress, byID: contracts}, nil
}

func (c StaticToolContracts) contract(id toolspec.ID) (staticToolContract, bool) {
	contract, ok := c.byID[id]
	return contract, ok
}

func (c StaticToolContracts) definition(id toolspec.ID) (Definition, bool) {
	contract, ok := c.contract(id)
	if !ok {
		return Definition{}, false
	}
	metadata, ok := definitions[id]
	if !ok {
		return Definition{}, false
	}
	metadata.Schema = contract.schema
	return metadata, true
}

func (c StaticToolContracts) prepareInput(id toolspec.ID, raw []byte) ([]byte, error) {
	contract, ok := c.contract(id)
	if !ok {
		return nil, fmt.Errorf("tool %q has no prepared static contract", id)
	}
	value, err := c.ingress.ValidateValue(raw)
	if err != nil {
		return nil, err
	}
	projected, err := value.ProjectObject(contract.projection)
	if err != nil {
		return nil, err
	}
	canonical, err := projected.CompactJSON()
	if err != nil {
		return nil, err
	}
	validated, err := contract.schema.ValidateValue(canonical)
	if err != nil {
		return nil, err
	}
	return validated.CompactJSON()
}
