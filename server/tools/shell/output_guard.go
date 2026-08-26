package shell

import (
	"fmt"

	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

const oversizedOutputMessageTemplate = "Command was executed but the output you requested exceeded 0.5 of your memory size. It was forcibly truncated still to prevent your memory overload, and the output written to %s . Next time be more careful with larger outputs."

type oversizedOutputGuard struct {
	contextWindowTokens int
}

func newOversizedOutputGuard(contextWindowTokens int) oversizedOutputGuard {
	return oversizedOutputGuard{contextWindowTokens: contextWindowTokens}
}

func (g oversizedOutputGuard) FailedResult(
	call tools.Call,
	requestedOutputTokens *int,
	modelVisibleOutput string,
	outputPath string,
	presentation *transcript.ToolResultPresentationDelta,
) (tools.Result, bool) {
	if requestedOutputTokens == nil ||
		*requestedOutputTokens <= g.contextWindowTokens/2 ||
		textutil.ApproxTextTokenCount(modelVisibleOutput) <= g.contextWindowTokens/2 {
		return tools.Result{}, false
	}
	result := tools.ErrorResultWith(
		call,
		fmt.Sprintf(oversizedOutputMessageTemplate, outputPath),
		marshalNoHTMLEscape,
	)
	result.PresentationDelta = presentation
	return result, true
}
