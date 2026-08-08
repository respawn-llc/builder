package tools

import (
	"reflect"
	"testing"
)

func TestAskQuestionBatchMetadataUsesStepAsItsSoleBatchIdentity(t *testing.T) {
	metadataType := reflect.TypeOf(AskQuestionBatchMetadata{})
	if _, exists := metadataType.FieldByName("BatchID"); exists {
		t.Fatal("AskQuestionBatchMetadata retains secondary BatchID")
	}
	stepField, exists := metadataType.FieldByName("StepID")
	if !exists || stepField.Type.Kind() != reflect.String {
		t.Fatalf("AskQuestionBatchMetadata StepID field = %+v", stepField)
	}
}
