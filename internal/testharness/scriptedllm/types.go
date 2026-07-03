package scriptedllm

import (
	"context"
	"errors"

	"core/server/llm"
)

var ErrScriptExhausted = errors.New("scripted llm: no steps remaining")
var ErrUnexpectedToolResult = errors.New("scripted llm: unexpected tool result")
var ErrConcurrentCall = errors.New("scripted llm: undeclared concurrent call")

type Script struct {
	Capabilities        *llm.ProviderCapabilities
	Steps               []Step
	Compactions         []llm.CompactionResponse
	InputTokenCount     *int
	ContextWindowTokens *int
	AllowConcurrent     bool
}

type Step struct {
	Response            llm.Response
	Err                 error
	Cancel              bool
	StreamDeltas        []llm.AssistantDelta
	ReasoningDeltas     []llm.ReasoningSummaryDelta
	ExpectedToolResults []ExpectedToolResult
	BeforeResponse      func(context.Context) error
	AfterResponse       func(context.Context) error
}

type ExpectedToolResult struct {
	CallID string
	Name   string
}
