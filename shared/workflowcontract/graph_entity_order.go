package workflowcontract

import "strings"

// CompareGraphEntityIdentity defines canonical ordering by entity type, then persistent ID.
func CompareGraphEntityIdentity(leftType string, leftID string, rightType string, rightID string) int {
	if leftType != rightType {
		return strings.Compare(leftType, rightType)
	}
	return strings.Compare(leftID, rightID)
}
