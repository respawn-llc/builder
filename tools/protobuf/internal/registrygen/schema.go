package registrygen

import (
	"fmt"
	"strings"
)

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

// ActiveOperationName derives <package>.<service>.<method> using the locked
// initialism-aware Protobuf identifier conversion.
func ActiveOperationName(packageName string, service string, method string) (string, error) {
	if err := validatePackageName(packageName); err != nil {
		return "", err
	}
	serviceName, err := pascalCaseToLowerSnake(service)
	if err != nil {
		return "", fmt.Errorf("service: %w", err)
	}
	methodName, err := pascalCaseToLowerSnake(method)
	if err != nil {
		return "", fmt.Errorf("method: %w", err)
	}
	return strings.Join([]string{packageName, serviceName, methodName}, "."), nil
}

func validatePackageName(packageName string) error {
	if packageName == "" {
		return fmt.Errorf("package is empty")
	}
	atSegmentStart := true
	for index := 0; index < len(packageName); index++ {
		character := packageName[index]
		if character == '.' {
			if atSegmentStart {
				return fmt.Errorf("package segment at byte %d is empty", index)
			}
			atSegmentStart = true
			continue
		}
		if atSegmentStart {
			if !isASCIILower(character) {
				return fmt.Errorf("package segment at byte %d must start with an ASCII lowercase letter", index)
			}
			atSegmentStart = false
			continue
		}
		if !isASCIILower(character) && !isASCIIDigit(character) && character != '_' {
			return fmt.Errorf("invalid package character at byte %d", index)
		}
	}
	if atSegmentStart {
		return fmt.Errorf("package has an empty trailing segment")
	}
	return nil
}

func pascalCaseToLowerSnake(identifier string) (string, error) {
	if identifier == "" {
		return "", fmt.Errorf("identifier is empty")
	}
	if !isASCIIUpper(identifier[0]) {
		return "", fmt.Errorf("identifier must start with an ASCII uppercase letter")
	}
	result := make([]byte, 0, len(identifier)+4)
	for index := 0; index < len(identifier); index++ {
		character := identifier[index]
		if !isASCIIUpper(character) && !isASCIILower(character) && !isASCIIDigit(character) {
			return "", fmt.Errorf("invalid identifier character at byte %d", index)
		}
		if isASCIIUpper(character) && index > 0 {
			previous := identifier[index-1]
			hasFollowingLower := index+1 < len(identifier) && isASCIILower(identifier[index+1])
			if isASCIILower(previous) || isASCIIDigit(previous) || (isASCIIUpper(previous) && hasFollowingLower) {
				result = append(result, '_')
			}
		}
		if isASCIIUpper(character) {
			character += 'a' - 'A'
		}
		result = append(result, character)
	}
	return string(result), nil
}

func isASCIIUpper(character byte) bool { return character >= 'A' && character <= 'Z' }
func isASCIILower(character byte) bool { return character >= 'a' && character <= 'z' }
func isASCIIDigit(character byte) bool { return character >= '0' && character <= '9' }
