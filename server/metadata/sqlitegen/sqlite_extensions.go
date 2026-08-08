package sqlitegen

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/workflow/label"
	"core/shared/labelcontract"
	"core/shared/tasksearchtext"

	"github.com/google/uuid"
	sqlitedriver "modernc.org/sqlite"
)

const LabelCollationName = "kent_label_casefold_v1"
const LabelFoldFunctionName = "kent_label_casefold_v1_fold"
const LiteralOccurrenceCountFunctionName = "kent_task_search_occurrence_count_v1"
const LifecycleTaskStateFunctionName = "kent_lifecycle_task_state_v1"
const LifecycleCurrentNodeIDsFunctionName = "kent_lifecycle_current_node_ids_v1"

const (
	LifecycleTaskStateOwned int64 = 1 << iota
	LifecycleTaskStateRunning
	LifecycleTaskStateQueued
	LifecycleTaskStateWaitingQuestion
	LifecycleTaskStateWaitingApproval
)

type LifecycleTaskQueryState struct {
	Flags          int64
	CurrentNodeIDs []string
}

type LifecycleTaskStateResolver func(taskID string) (LifecycleTaskQueryState, error)

var lifecycleTaskStateResolvers sync.Map

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
		if err := sqlitedriver.RegisterScalarFunction(
			LifecycleTaskStateFunctionName,
			2,
			lifecycleTaskState,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: LifecycleTaskStateFunctionName,
				Cause:         err,
			}
			return
		}
		if err := sqlitedriver.RegisterScalarFunction(
			LifecycleCurrentNodeIDsFunctionName,
			2,
			lifecycleCurrentNodeIDs,
		); err != nil {
			registrationErr = &RegistrationError{
				ExtensionName: LifecycleCurrentNodeIDsFunctionName,
				Cause:         err,
			}
		}
	})
	return registrationErr
}

func RegisterLifecycleTaskStateResolver(resolver LifecycleTaskStateResolver) (string, func(), error) {
	if resolver == nil {
		return "", nil, errors.New("lifecycle Task state resolver is required")
	}
	token := uuid.NewString()
	lifecycleTaskStateResolvers.Store(token, resolver)
	var releaseOnce sync.Once
	return token, func() {
		releaseOnce.Do(func() {
			lifecycleTaskStateResolvers.Delete(token)
		})
	}, nil
}

func lifecycleTaskState(_ *sqlitedriver.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	resolver, taskID, err := lifecycleTaskStateResolver(arguments)
	if err != nil {
		return nil, err
	}
	state, err := resolver(taskID)
	if err != nil {
		return nil, err
	}
	return state.Flags, nil
}

func lifecycleCurrentNodeIDs(_ *sqlitedriver.FunctionContext, arguments []driver.Value) (driver.Value, error) {
	resolver, taskID, err := lifecycleTaskStateResolver(arguments)
	if err != nil {
		return nil, err
	}
	state, err := resolver(taskID)
	if err != nil {
		return nil, err
	}
	if state.Flags&LifecycleTaskStateOwned == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(state.CurrentNodeIDs)
	if err != nil {
		return nil, fmt.Errorf("encode SQLite lifecycle Current Node ids: %w", err)
	}
	return string(encoded), nil
}

func lifecycleTaskStateResolver(arguments []driver.Value) (LifecycleTaskStateResolver, string, error) {
	token, err := textArgument(arguments, 0, "lifecycle state token")
	if err != nil {
		return nil, "", err
	}
	taskID, err := textArgument(arguments, 1, "lifecycle Task id")
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(token) != token || token == "" {
		return nil, "", errors.New("SQLite lifecycle state token is invalid")
	}
	if strings.TrimSpace(taskID) != taskID || taskID == "" {
		return nil, "", errors.New("SQLite lifecycle Task id is invalid")
	}
	rawResolver, exists := lifecycleTaskStateResolvers.Load(token)
	if !exists {
		return nil, "", errors.New("SQLite lifecycle state resolver is unavailable")
	}
	resolver, ok := rawResolver.(LifecycleTaskStateResolver)
	if !ok || resolver == nil {
		return nil, "", errors.New("SQLite lifecycle state resolver has an invalid type")
	}
	return resolver, taskID, nil
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
