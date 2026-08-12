package sqlitegen

import (
	"database/sql/driver"
	"fmt"
	"sync"

	"core/server/workflow/label"
	"core/shared/labelcontract"
	"core/shared/runtimeids"
	"core/shared/tasksearchtext"

	sqlitedriver "modernc.org/sqlite"
)

const LabelCollationName = "kent_label_casefold_v1"
const LabelFoldFunctionName = "kent_label_casefold_v1_fold"
const LiteralOccurrenceCountFunctionName = "kent_task_search_occurrence_count_v1"
const graphEntityIDBlobFunctionName = "kent_graph_entity_id_blob_v1"
const graphEntityIDTextFunctionName = "kent_graph_entity_id_text_v1"

type RegistrationError struct {
	ExtensionName string
	Cause         error
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("register SQLite extension %q: %v", e.ExtensionName, e.Cause)
}

func (e *RegistrationError) Unwrap() error {
	return e.Cause
}

var registrationOnce sync.Once
var registrationErr error

func RegisterSQLiteExtensions() error {
	registrationOnce.Do(func() {
		if err := sqlitedriver.RegisterCollationUtf8(
			LabelCollationName,
			func(left string, right string) int {
				return label.Compare(label.Name(left), label.Name(right))
			},
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: LabelCollationName,
				Cause:         err,
			}
			return
		}
		if err := sqlitedriver.RegisterDeterministicScalarFunction(
			LabelFoldFunctionName,
			1,
			labelFold,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: LabelFoldFunctionName,
				Cause:         err,
			}
			return
		}
		if err := sqlitedriver.RegisterDeterministicScalarFunction(
			LiteralOccurrenceCountFunctionName,
			3,
			literalOccurrenceCount,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: LiteralOccurrenceCountFunctionName,
				Cause:         err,
			}
			return
		}
		if err := sqlitedriver.RegisterDeterministicScalarFunction(
			graphEntityIDBlobFunctionName,
			1,
			graphEntityIDBlob,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: graphEntityIDBlobFunctionName,
				Cause:         err,
			}
			return
		}
		if err := sqlitedriver.RegisterDeterministicScalarFunction(
			graphEntityIDTextFunctionName,
			1,
			graphEntityIDText,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: graphEntityIDTextFunctionName,
				Cause:         err,
			}
		}
	})
	return registrationErr
}

func graphEntityIDBlob(_ *sqlitedriver.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	raw, err := textArgument(arguments, 0, "graph entity ID")
	if err != nil {
		return nil, err
	}
	return runtimeids.GraphEntityIDBlob(raw)
}

func graphEntityIDText(_ *sqlitedriver.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	if len(arguments) != 1 {
		return nil, fmt.Errorf("SQLite graph entity ID BLOB argument count = %d, want 1", len(arguments))
	}
	raw, ok := arguments[0].([]byte)
	if !ok {
		return nil, fmt.Errorf("SQLite graph entity ID BLOB argument has type %T, want BLOB", arguments[0])
	}
	return runtimeids.GraphEntityIDText(raw)
}

func labelFold(_ *sqlitedriver.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	text, err := textArgument(arguments, 0, "label fold")
	if err != nil {
		return nil, err
	}
	return labelcontract.Fold(text), nil
}

func literalOccurrenceCount(_ *sqlitedriver.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	source, err := textArgument(arguments, 0, "source")
	if err != nil {
		return nil, err
	}
	query, err := textArgument(arguments, 1, "query")
	if err != nil {
		return nil, err
	}
	caseMode, err := literalCaseModeArgument(arguments, 2)
	if err != nil {
		return nil, err
	}
	matcher, err := tasksearchtext.NewLiteralMatcher(query, caseMode)
	if err != nil {
		return nil, fmt.Errorf("create literal matcher: %w", err)
	}
	return int64(matcher.OccurrenceCount(source)), nil
}

func textArgument(arguments []driver.Value, index int, name string) (string, error) {
	if len(arguments) <= index {
		return "", fmt.Errorf("SQLite occurrence-count %s argument is missing", name)
	}
	text, ok := arguments[index].(string)
	if !ok {
		return "", fmt.Errorf("SQLite occurrence-count %s argument has type %T, want text", name, arguments[index])
	}
	return text, nil
}

func literalCaseModeArgument(arguments []driver.Value, index int) (tasksearchtext.LiteralCaseMode, error) {
	if len(arguments) <= index {
		return 0, fmt.Errorf("SQLite occurrence-count case mode argument is missing")
	}
	rawMode, ok := arguments[index].(int64)
	if !ok {
		return 0, fmt.Errorf("SQLite occurrence-count case mode argument has type %T, want integer", arguments[index])
	}
	caseMode := tasksearchtext.LiteralCaseMode(rawMode)
	if caseMode != tasksearchtext.LiteralCaseInsensitive && caseMode != tasksearchtext.LiteralCaseSensitive {
		return 0, fmt.Errorf("SQLite occurrence-count case mode %d is invalid", rawMode)
	}
	return caseMode, nil
}
