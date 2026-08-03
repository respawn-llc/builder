package workflowview

import "database/sql"

func workflowTaskDependencyFilterQueryArg(filter *bool) sql.NullInt64 {
	if filter == nil {
		return sql.NullInt64{}
	}
	if *filter {
		return sql.NullInt64{Int64: 1, Valid: true}
	}
	return sql.NullInt64{Int64: 0, Valid: true}
}
