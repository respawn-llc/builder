package metadata

import (
	"bytes"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/google/uuid"
	sqlitedriver "modernc.org/sqlite"
)

const migrationWorkflowIDTestValue = "7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"

func TestMigrationWorkflowIDFunctionsConvertCanonicalAndLegacyText(t *testing.T) {
	expectedUUID := uuid.MustParse(migrationWorkflowIDTestValue)
	for _, raw := range []string{
		migrationWorkflowIDTestValue,
		"workflow-" + migrationWorkflowIDTestValue,
	} {
		t.Run(raw, func(t *testing.T) {
			args := []driver.Value{raw, "workflows.id row=workflow-1"}

			blobValue, err := migrationWorkflowIDBlob(nil, args)
			if err != nil {
				t.Fatalf("migrationWorkflowIDBlob(%q): %v", raw, err)
			}
			blob, ok := blobValue.([]byte)
			if !ok {
				t.Fatalf("migrationWorkflowIDBlob(%q) type = %T, want []byte", raw, blobValue)
			}
			if !bytes.Equal(blob, expectedUUID[:]) {
				t.Fatalf("migrationWorkflowIDBlob(%q) = %x, want %x", raw, blob, expectedUUID[:])
			}

			textValue, err := migrationWorkflowIDText(nil, args)
			if err != nil {
				t.Fatalf("migrationWorkflowIDText(%q): %v", raw, err)
			}
			if got, ok := textValue.(string); !ok || got != migrationWorkflowIDTestValue {
				t.Fatalf("migrationWorkflowIDText(%q) = %#v, want %q", raw, textValue, migrationWorkflowIDTestValue)
			}
		})
	}
}

func TestMigrationWorkflowIDFunctionsRejectInvalidTextWithContext(t *testing.T) {
	const location = "workflow_nodes.workflow_id row=node-1"
	for _, raw := range []string{
		"workflow-11111111-1111-1111-8111-111111111111",
		"workflow-00000000-0000-4000-0000-000000000000",
		"workflow-" + migrationWorkflowIDTestValue + " ",
		"not-a-workflow-id",
	} {
		t.Run(raw, func(t *testing.T) {
			for name, function := range map[string]func(*sqlitedriver.FunctionContext, []driver.Value) (driver.Value, error){
				"blob": migrationWorkflowIDBlob,
				"text": migrationWorkflowIDText,
			} {
				t.Run(name, func(t *testing.T) {
					_, err := function(nil, []driver.Value{raw, location})
					if err == nil {
						t.Fatalf("%s(%q) succeeded", name, raw)
					}
					var diagnostic *WorkflowIdentityMigrationDiagnostic
					if !errors.As(err, &diagnostic) {
						t.Fatalf("%s(%q) error = %T, want WorkflowIdentityMigrationDiagnostic", name, raw, err)
					}
					if diagnostic.Location != location {
						t.Fatalf("%s(%q) diagnostic location = %q, want %q", name, raw, diagnostic.Location, location)
					}
				})
			}
		})
	}
}
