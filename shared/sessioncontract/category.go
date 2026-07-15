package sessioncontract

import "fmt"

type SessionCategory string

const (
	SessionCategoryMain     SessionCategory = "main"
	SessionCategorySubagent SessionCategory = "subagent"
)

func ParseSessionCategory(raw string) (SessionCategory, error) {
	category := SessionCategory(raw)
	switch category {
	case SessionCategoryMain, SessionCategorySubagent:
		return category, nil
	default:
		return "", fmt.Errorf("invalid session category %q", raw)
	}
}
