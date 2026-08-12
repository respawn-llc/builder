package toolcontracts

import (
	"core/server/tools"
	edittool "core/server/tools/edit"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	shelltool "core/server/tools/shell"
)

func sources() []tools.StaticContractSource {
	return []tools.StaticContractSource{
		shelltool.ExecCommandStaticContractSource(),
		shelltool.WriteStdinStaticContractSource(),
		readimagetool.StaticContractSource(),
		patchtool.StaticContractSource(),
		edittool.StaticContractSource(),
		tools.AskQuestionStaticContractSource(),
		tools.TriggerHandoffStaticContractSource(),
		tools.WebSearchStaticContractSource(),
	}
}
