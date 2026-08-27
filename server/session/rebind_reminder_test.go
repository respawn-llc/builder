package session

import (
	"reflect"
	"testing"

	"core/shared/serverapi"
)

func TestSessionRebindReminderNormalizesAndClonesOnlyReminderFacts(t *testing.T) {
	workingDirectory := " /target/pkg "
	normalized, err := NormalizeSessionRebindReminder(SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: " source-id ", Name: " Source "},
		TargetProject:    serverapi.ProjectReference{ID: " target-id ", Name: " Target "},
		WorkingDirectory: &workingDirectory,
	})
	if err != nil {
		t.Fatalf("NormalizeSessionRebindReminder: %v", err)
	}
	want := SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: "source-id", Name: "Source"},
		TargetProject:    serverapi.ProjectReference{ID: "target-id", Name: "Target"},
		WorkingDirectory: stringPointer("/target/pkg"),
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized reminder = %#v, want %#v", normalized, want)
	}
}
