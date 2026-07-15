package runtimefeed

import (
	"reflect"
	"testing"

	"core/shared/clientui"
)

func TestRuntimeFeedContractDoesNotReferenceProtocol59AggregateShapes(t *testing.T) {
	forbidden := map[reflect.Type]struct{}{
		reflect.TypeOf(clientui.TranscriptMessage{}):                  {},
		reflect.TypeOf(clientui.TranscriptHydration{}):                {},
		reflect.TypeOf(clientui.RuntimeMainView{}):                    {},
		reflect.TypeOf(clientui.RuntimeStatus{}):                      {},
		reflect.TypeOf(clientui.RuntimeActivity{}):                    {},
		reflect.TypeOf(clientui.RuntimeOperationRef{}):                {},
		reflect.TypeOf(clientui.RuntimeInputReconciliation{}):         {},
		reflect.TypeOf(clientui.RuntimeInputReconciliationSnapshot{}): {},
		reflect.TypeOf(clientui.PendingPromptEvent{}):                 {},
		reflect.TypeOf(clientui.RunState{}):                           {},
		reflect.TypeOf(clientui.ReasoningDelta{}):                     {},
		reflect.TypeOf(clientui.BackgroundShellEvent{}):               {},
		reflect.TypeOf(clientui.WorktreeTransitionOutcome{}):          {},
	}
	walkRuntimeFeedType(t, reflect.TypeOf(TranscriptMessage{}), map[reflect.Type]struct{}{}, func(current reflect.Type, path string) {
		if _, found := forbidden[current]; found {
			t.Fatalf("runtime feed contract %s references protocol-59 aggregate %s", path, current)
		}
		if current.Kind() != reflect.Struct {
			return
		}
		for index := 0; index < current.NumField(); index++ {
			field := current.Field(index)
			if field.Name == "Removed" {
				t.Fatalf("runtime feed contract %s.%s reintroduces boolean lifecycle removal", path, field.Name)
			}
		}
	})
}

func walkRuntimeFeedType(
	t *testing.T,
	typ reflect.Type,
	seen map[reflect.Type]struct{},
	visit func(reflect.Type, string),
) {
	t.Helper()
	typ = dereferenceRuntimeFeedType(typ)
	if typ == nil {
		return
	}
	if _, found := seen[typ]; found {
		return
	}
	seen[typ] = struct{}{}
	visit(typ, typ.String())
	if typ.Kind() != reflect.Struct {
		return
	}
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}
		fieldType := dereferenceRuntimeFeedType(field.Type)
		if fieldType == nil {
			continue
		}
		fieldPath := typ.String() + "." + field.Name
		visit(fieldType, fieldPath)
		if fieldType.Kind() == reflect.Map || fieldType.Kind() == reflect.Interface {
			t.Fatalf("runtime feed contract %s exposes generic map/interface payload", fieldPath)
		}
		if fieldType.PkgPath() == "core/server/runtimefeed" {
			walkRuntimeFeedType(t, fieldType, seen, visit)
		}
	}
}

func dereferenceRuntimeFeedType(typ reflect.Type) reflect.Type {
	for typ != nil {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			typ = typ.Elem()
		default:
			return typ
		}
	}
	return nil
}
