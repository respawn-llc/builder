package jsoncontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	invjsonschema "github.com/invopop/jsonschema"
	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaResource = "schema.json"

func DecodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON value")
	}
	return nil
}

// Customize applies narrow, owner-specific changes through invopop's schema
// document before the contract is compiled.
type Customize func(*invjsonschema.Schema) error

type Preparer struct {
	debug bool
}

func NewPreparer(debug bool) Preparer {
	return Preparer{debug: debug}
}

type prepared struct {
	document []byte
	schema   *validator.Schema
	strict   bool
	fields   []string
}

func (p Preparer) prepare(
	owner string,
	source any,
	selected profile,
	customizers []Customize,
) (prepared, error) {
	reflector := invjsonschema.Reflector{Anonymous: true}
	strict := false
	switch selected {
	case profileFunction:
		reflector.DoNotReference = true
	case profileStructured:
		reflector.DoNotReference = true
		strict = true
	}

	document := reflector.Reflect(source)
	for _, customize := range customizers {
		if customize == nil {
			continue
		}
		if err := customize(document); err != nil {
			return prepared{}, p.failure(owner, "customize", err)
		}
	}
	serialized, err := json.Marshal(document)
	if err != nil {
		return prepared{}, p.failure(owner, "serialize", err)
	}
	parsed, err := validator.UnmarshalJSON(bytes.NewReader(serialized))
	if err != nil {
		return prepared{}, p.failure(owner, "parse reflected schema", err)
	}
	compiler := validator.NewCompiler()
	compiler.DefaultDraft(validator.Draft2020)
	if err := compiler.AddResource(schemaResource, parsed); err != nil {
		return prepared{}, p.failure(owner, "add schema resource", err)
	}
	compiled, err := compiler.Compile(schemaResource)
	if err != nil {
		return prepared{}, p.failure(owner, "compile", err)
	}
	fields := make([]string, 0)
	if document.Properties != nil {
		for name := range document.Properties.KeysFromOldest() {
			fields = append(fields, name)
		}
	}
	return prepared{
		document: bytes.Clone(serialized),
		schema:   compiled,
		strict:   strict,
		fields:   fields,
	}, nil
}

func (p Preparer) failure(owner, operation string, cause error) error {
	err := fmt.Errorf("prepare JSON contract for %s: %s: %w", owner, operation, cause)
	if p.debug {
		panic(err)
	}
	return err
}

func (p prepared) JSON() []byte {
	return bytes.Clone(p.document)
}

func (p prepared) Strict() bool {
	return p.strict
}

func (p prepared) Prepared() bool {
	return p.schema != nil
}

func (p prepared) Fields() []string {
	return append([]string(nil), p.fields...)
}

func (p prepared) Validate(raw []byte) error {
	_, err := p.ValidateValue(raw)
	return err
}

func (p prepared) ValidateValue(raw []byte) (Value, error) {
	value, err := validator.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return Value{}, err
	}
	if err := p.schema.Validate(value); err != nil {
		return Value{}, err
	}
	return Value{value: value}, nil
}
