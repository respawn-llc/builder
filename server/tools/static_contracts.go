package tools

import (
	"errors"
	"fmt"

	"core/shared/jsoncontract"
	"core/shared/toolspec"

	invjsonschema "github.com/invopop/jsonschema"
)

var errStaticToolContractPreparation = errors.New("static tool contract preparation failed")

type StaticContractSource struct {
	ID    toolspec.ID
	Input any
}

type staticToolContract struct {
	schema jsoncontract.Function
	fields []string
}

type StaticToolContracts struct {
	ingress jsoncontract.Function
	byID    map[toolspec.ID]staticToolContract
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

	contracts := make(map[toolspec.ID]staticToolContract, len(sources))
	for _, source := range sources {
		if _, ok := definitions[source.ID]; !ok {
			return StaticToolContracts{}, fmt.Errorf(
				"%w: tool %q is missing centralized metadata",
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
		fields := schema.Fields()
		contracts[source.ID] = staticToolContract{schema: schema, fields: fields}
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

type PreparedInput struct {
	Canonical       []byte
	ValidationError error
}

func (c StaticToolContracts) prepareInput(id toolspec.ID, raw []byte) ([]byte, error) {
	prepared, err := c.prepareInputOutcome(id, raw)
	if err != nil {
		return nil, err
	}
	if prepared.ValidationError != nil {
		return nil, prepared.ValidationError
	}
	return prepared.Canonical, nil
}

func (c StaticToolContracts) prepareInputOutcome(id toolspec.ID, raw []byte) (PreparedInput, error) {
	contract, ok := c.contract(id)
	if !ok {
		return PreparedInput{}, fmt.Errorf("tool %q has no prepared static contract", id)
	}
	value, err := c.ingress.ValidateValue(raw)
	if err != nil {
		return PreparedInput{}, err
	}
	projected, err := value.ProjectObjectWithMatcher(contract.fields, func(name string) (string, int, bool) {
		if canonical, priority, ok := toolspec.MatchModelParameterName(id, name); ok {
			return canonical, priority, true
		}
		for _, field := range contract.fields {
			if name == field {
				return field, 0, true
			}
		}
		return "", 0, false
	})
	if err != nil {
		return PreparedInput{}, err
	}
	canonical, err := projected.CompactJSON()
	if err != nil {
		return PreparedInput{}, err
	}
	validated, err := contract.schema.ValidateValue(canonical)
	if err != nil {
		return PreparedInput{Canonical: canonical, ValidationError: err}, nil
	}
	validatedJSON, err := validated.CompactJSON()
	if err != nil {
		return PreparedInput{}, err
	}
	return PreparedInput{Canonical: validatedJSON}, nil
}
