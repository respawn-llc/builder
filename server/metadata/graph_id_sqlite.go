package metadata

import (
	"database/sql/driver"
	"fmt"

	"core/shared/runtimeids"

	sqlitedriver "modernc.org/sqlite"
)

const graphEntityIDBlobFunction = "kent_graph_entity_id_blob_v1"
const graphEntityIDTextFunction = "kent_graph_entity_id_text_v1"

func graphEntityIDBlob(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s requires 1 argument", graphEntityIDBlobFunction)
	}
	raw, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("%s requires canonical UUIDv4 text", graphEntityIDBlobFunction)
	}
	return runtimeids.GraphEntityIDBlob(raw)
}

func graphEntityIDText(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s requires 1 argument", graphEntityIDTextFunction)
	}
	raw, ok := args[0].([]byte)
	if !ok {
		return nil, fmt.Errorf("%s requires a UUIDv4 BLOB", graphEntityIDTextFunction)
	}
	return runtimeids.GraphEntityIDText(raw)
}
