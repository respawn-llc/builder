package runprompt

import (
	"context"
	"strings"

	"core/server/requestmemo"
	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type runPromptMemoRequest struct {
	Intent            serverapi.SessionLaunchIntent
	SelectedSessionID string
	Prompt            string
	Timeout           string
	CallerSessionID   serverapi.OptionalStringKey
	ParentSessionID   serverapi.OptionalStringKey
	Overrides         serverapi.RunPromptOverridesKey
}

type memoizingPromptService struct {
	inner servicecontract.RunPromptService
	runs  *requestmemo.Memo[runPromptMemoRequest, serverapi.RunPromptResponse]
}

func (s *memoizingPromptService) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	overrides, err := req.Overrides.CanonicalKey()
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	memoReq := runPromptMemoRequest{
		Intent:            req.Intent,
		SelectedSessionID: strings.TrimSpace(req.SelectedSessionID),
		Prompt:            strings.TrimSpace(req.Prompt),
		Timeout:           req.Timeout.String(),
		CallerSessionID:   serverapi.CanonicalOptionalString(req.CallerSessionID),
		ParentSessionID:   serverapi.CanonicalOptionalString(req.ParentSessionID),
		Overrides:         overrides,
	}
	return s.runs.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameRunPromptMemoRequest, func(ctx context.Context) (serverapi.RunPromptResponse, error) {
		return s.inner.RunPrompt(ctx, req, progress)
	})
}

func sameRunPromptMemoRequest(a runPromptMemoRequest, b runPromptMemoRequest) bool {
	return a.Intent.Equal(b.Intent) &&
		a.SelectedSessionID == b.SelectedSessionID &&
		a.Prompt == b.Prompt &&
		a.Timeout == b.Timeout &&
		a.CallerSessionID == b.CallerSessionID &&
		a.ParentSessionID == b.ParentSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.RunPromptService = (*memoizingPromptService)(nil)
