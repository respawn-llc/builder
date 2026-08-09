package runprompt

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/server/metadata"
	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type inProcessRunPromptService struct {
	launcher *headlessPromptLauncher
}

func (s *inProcessRunPromptService) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	return s.runPrompt(ctx, req, progress)
}

func (s *inProcessRunPromptService) runPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (response serverapi.RunPromptResponse, err error) {
	if s == nil || s.launcher == nil {
		return serverapi.RunPromptResponse{}, errors.New("run prompt service is not configured")
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if err := req.Validate(); err != nil {
		return serverapi.RunPromptResponse{}, err
	}

	runtimeHandle, err := s.launcher.prepareHeadlessPrompt(ctx, req, progress)
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	defer func() {
		err = errors.Join(err, runtimeHandle.plan.CloseWithFailure(err != nil))
	}()

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	startedAt := time.Now()
	if history := s.launcher.boot.PromptHistory; history != nil {
		_, _, err := history.RecordPromptHistoryEntry(runCtx, metadata.PromptHistoryEntry{
			SessionID: runtimeHandle.plan.sessionID,
			SourceID:  req.ClientRequestID,
			Text:      runtimeHandle.plan.PromptHistoryText(req.Prompt),
		})
		if err != nil {
			return serverapi.RunPromptResponse{}, err
		}
	}
	response, runErr := runtimeHandle.submitUserMessage(runCtx, req.Prompt)
	response.Duration = time.Since(startedAt)
	if runErr != nil {
		return response, runErr
	}
	return response, nil
}

var _ servicecontract.RunPromptService = (*inProcessRunPromptService)(nil)
