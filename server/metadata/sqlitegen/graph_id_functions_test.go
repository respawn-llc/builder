package sqlitegen

import (
	"database/sql/driver"
	"sync"

	"core/shared/runtimeids"

	sqlitedriver "modernc.org/sqlite"
)

var registerGraphIDFunctionsForQueryTestsOnce sync.Once

func init() {
	registerGraphIDFunctionsForQueryTestsOnce.Do(func() {
		if err := sqlitedriver.RegisterDeterministicScalarFunction(
			"kent_graph_entity_id_blob_v1",
			1,
			func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
				return runtimeids.GraphEntityIDBlob(args[0].(string))
			},
		); err != nil {
			panic(err)
		}
		if err := sqlitedriver.RegisterDeterministicScalarFunction(
			"kent_graph_entity_id_text_v1",
			1,
			func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
				return runtimeids.GraphEntityIDText(args[0].([]byte))
			},
		); err != nil {
			panic(err)
		}
	})
}
