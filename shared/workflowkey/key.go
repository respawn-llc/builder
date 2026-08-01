package workflowkey

import "strings"

const (
	MaxChars    = 64
	Description = "start with a lowercase letter and contain only lowercase letters, digits, or underscores, up to 64 characters"
)

// reservedParameters are transition-parameter keys the runtime owns, so authors
// cannot declare them as edge parameters.
var reservedParameters = map[string]bool{
	"commentary": true,
	"transition": true,
}

// ReservedParameter reports whether a key is reserved and cannot be declared as a
// transition parameter.
func ReservedParameter(value string) bool {
	return reservedParameters[strings.TrimSpace(value)]
}

func Valid(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) == 0 || len(trimmed) > MaxChars {
		return false
	}
	for index, char := range trimmed {
		if index == 0 {
			if char < 'a' || char > 'z' {
				return false
			}
			continue
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}
