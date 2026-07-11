package ongoing

import (
	"reflect"
	"testing"
)

func TestSurfaceDoesNotOwnPendingToolMutableState(t *testing.T) {
	surfaceType := reflect.TypeOf(Surface{})
	for index := 0; index < surfaceType.NumField(); index++ {
		field := surfaceType.Field(index)
		switch field.Name {
		case "pendingTools", "toolStarts", "toolAborts":
			t.Fatalf("Surface field %s reintroduces pending-tool state into cli/tui/ongoing", field.Name)
		}
	}
}
