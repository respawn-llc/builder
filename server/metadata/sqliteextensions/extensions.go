package sqliteextensions

import (
	"database/sql/driver"
	"fmt"
	"sync"

	"core/server/tasksearchtext"
	"core/server/workflow/label"

	sqlitedriver "modernc.org/sqlite"
)

const LabelCollationName = "kent_label_casefold_v1"
const LiteralOccurrenceCountFunctionName = "kent_task_search_occurrence_count_v1"

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

func Register() error {
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
			LiteralOccurrenceCountFunctionName,
			3,
			literalOccurrenceCount,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: LiteralOccurrenceCountFunctionName,
				Cause:         err,
			}
		}
	})
	return registrationErr
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
