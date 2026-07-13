package workflowstore

import (
	"database/sql"
	"strings"

	"core/server/workflow"
)

func workflowCustomRefFromRow(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	ref := strings.TrimSpace(value.String)
	return &ref
}

func workflowCustomRefForQuery(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: strings.TrimSpace(*value), Valid: true}
}

func normalizeWorkflowExecutionTargetPolicy(policy workflow.ExecutionTargetPolicy) workflow.ExecutionTargetPolicy {
	policy = policy.Canonical()
	if policy.CustomRef == nil {
		return policy
	}
	ref := strings.TrimSpace(*policy.CustomRef)
	policy.CustomRef = &ref
	return policy
}
