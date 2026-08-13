package registrygen

import "fmt"

// SchemaClass identifies which generated registry owns a Protobuf schema.
type SchemaClass uint8

const (
	KentDomain SchemaClass = iota + 1
	Fixture
)

// ClassifySchemaPath returns the sole generated-registry owner for a schema.
func ClassifySchemaPath(schemaPath string) (SchemaClass, error) {
	first, remainder, ok := nextPathSegment(schemaPath)
	if !ok {
		return 0, unclassifiedSchemaPath(schemaPath)
	}
	switch first {
	case "fixture":
		return Fixture, nil
	case "kent":
		second, remainder, ok := nextPathSegment(remainder)
		if !ok || second != "api" || remainder == "" {
			return 0, unclassifiedSchemaPath(schemaPath)
		}
		return KentDomain, nil
	default:
		return 0, unclassifiedSchemaPath(schemaPath)
	}
}

func nextPathSegment(value string) (segment string, remainder string, ok bool) {
	for index := 0; index < len(value); index++ {
		if value[index] != '/' {
			continue
		}
		if index == 0 {
			return "", "", false
		}
		return value[:index], value[index+1:], true
	}
	if value == "" {
		return "", "", false
	}
	return value, "", true
}

func unclassifiedSchemaPath(schemaPath string) error {
	return fmt.Errorf(
		"generated schema %q is outside the Kent domain and fixture roots",
		schemaPath,
	)
}
