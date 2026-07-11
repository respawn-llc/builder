package metadata

import "database/sql"

// OptionalInt64 converts a nullable SQLite value to an optional domain value.
func OptionalInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	valueCopy := value.Int64
	return &valueCopy
}

// OptionalString converts a nullable SQLite value to an optional domain value.
func OptionalString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	valueCopy := value.String
	return &valueCopy
}
