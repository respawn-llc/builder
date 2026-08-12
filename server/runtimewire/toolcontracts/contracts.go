package toolcontracts

import (
	"core/server/tools"
	"core/shared/jsoncontract"
)

func Prepare(preparer jsoncontract.Preparer) (tools.StaticToolContracts, error) {
	return tools.NewStaticToolContracts(preparer, sources()...)
}
