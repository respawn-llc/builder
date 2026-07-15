package runprompt

import (
	"context"
	"strings"

	"core/server/requestmemo"
	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type runPromptMemoRequest struct {
	Intent    serverapi.SessionLaunchIntent
	Prompt    string
	Timeout   string
	Overrides serverapi.RunPromptOverrides
}

type memoizingPromptService struct {
	inner servicecontract.RunPromptService
	runs  *requestmemo.Memo[runPromptMemoRequest, serverapi.RunPromptResponse]
}

func (s *memoizingPromptService) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	memoReq := runPromptMemoRequest{
		Intent:    req.Intent,
		Prompt:    strings.TrimSpace(req.Prompt),
		Timeout:   req.Timeout.String(),
		Overrides: req.Overrides,
	}
	return s.runs.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameRunPromptMemoRequest, func(ctx context.Context) (serverapi.RunPromptResponse, error) {
		return s.inner.RunPrompt(ctx, req, progress)
	})
}

func sameRunPromptMemoRequest(a runPromptMemoRequest, b runPromptMemoRequest) bool {
	return a.Intent.Equal(b.Intent) &&
		a.Prompt == b.Prompt &&
		a.Timeout == b.Timeout &&
		a.Overrides == b.Overrides
}

var _ servicecontract.RunPromptService = (*memoizingPromptService)(nil)
