package metadata

import (
	"database/sql/driver"
	"strings"
	"testing"
)

func TestMigrationCurrentInputValuesRejectsInvalidBindingValues(t *testing.T) {
	_, err := migrationCurrentInputValues(nil, []driver.Value{
		"task-1",
		"node-1",
		"",
		`[{"name":"summary","source":"unsupported","field":"summary"}]`,
		`{}`,
		"",
		"TASK-1",
		"Task",
		"Body",
		"",
	})
	if err == nil {
		t.Fatal("migrationCurrentInputValues accepted an unsupported binding source")
	}
	if !strings.Contains(err.Error(), "unsupported binding source") {
		t.Fatalf("migrationCurrentInputValues error = %v", err)
	}
}
