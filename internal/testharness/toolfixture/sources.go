package toolfixture

import (
	"core/server/tools"
	edittool "core/server/tools/edit"
	patchtool "core/server/tools/patch"
	readimagetool "core/server/tools/readimage"
	shelltool "core/server/tools/shell"
)

func staticContractSources() []tools.StaticContractSource {
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
