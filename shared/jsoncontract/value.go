package jsoncontract

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

type Value struct {
	value any
}

type NamedValue struct {
	Name  string
	Value Value
}

func (v Value) Field(name string) (Value, bool) {
	object, ok := v.value.(map[string]any)
	if !ok {
		return Value{}, false
	}
	value, ok := object[name]
	if !ok {
		return Value{}, false
	}
	return Value{value: value}, true
}

func (v Value) ObjectFields() ([]NamedValue, error) {
	object, ok := v.value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON value is not an object")
	}
	names := slices.Sorted(maps.Keys(object))
	fields := make([]NamedValue, 0, len(names))
	for _, name := range names {
		fields = append(fields, NamedValue{
			Name:  name,
			Value: Value{value: object[name]},
		})
	}
	return fields, nil
}

type ObjectField struct {
	Name    string
	Aliases []string
}

func (v Value) ProjectObject(fields []ObjectField) (Value, error) {
	source, ok := v.value.(map[string]any)
	if !ok {
		return Value{}, fmt.Errorf("JSON value is not an object")
	}
	projected := make(map[string]any, len(fields))
	for _, field := range fields {
		value, present := source[field.Name]
		if !present {
			for _, alias := range field.Aliases {
				value, present = source[alias]
				if present {
					break
				}
			}
		}
		if present {
			projected[field.Name] = value
		}
	}
	return Value{value: projected}, nil
}

func (v Value) String() (string, bool) {
	value, ok := v.value.(string)
	return value, ok
}

func (v Value) IsNull() bool {
	return v.value == nil
}

func (v Value) CompactJSON() ([]byte, error) {
	return json.Marshal(v.value)
}
