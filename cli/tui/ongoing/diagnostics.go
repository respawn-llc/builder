package ongoing

import (
	"fmt"
	"log"
	"runtime/debug"
)

type DeveloperError struct {
	Operation string
	Reason    string
	Facts     map[string]any
	Stack     string
}

func NewDeveloperError(operation, reason string, facts map[string]any) DeveloperError {
	return DeveloperError{
		Operation: operation,
		Reason:    reason,
		Facts:     facts,
		Stack:     string(debug.Stack()),
	}
}

func (e DeveloperError) Error() string {
	return fmt.Sprintf("ongoing developer error: operation=%s reason=%s facts=%v\n%s", e.Operation, e.Reason, e.Facts, e.Stack)
}

func PanicDeveloperError(err DeveloperError) {
	log.Printf("%s", err.Error())
	panic(err)
}

func panicOngoingDeveloperError(operation, reason string, facts map[string]any) {
	PanicDeveloperError(NewDeveloperError(operation, reason, facts))
}
