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
	req.Prompt = strings.TrimSpace(req.Prompt)
	if err := req.Validate(); err != nil {
		return serverapi.RunPromptResponse{}, err
	}

	runtimeHandle, err := s.launcher.prepareHeadlessPrompt(ctx, req, progress)
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	defer func() {
		err = errors.Join(err, runtimeHandle.closeWithFailure(err != nil))
	}()

	var historyErr error
	if history := s.launcher.boot.PromptHistory; history != nil {
		if runtimeHandle.retainedContinuation == nil {
			_, historyErr = history.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
				SessionID: runtimeHandle.promptHistorySessionID(),
				Text:      runtimeHandle.promptHistoryText(req.Prompt),
			})
			if historyErr != nil {
				return serverapi.RunPromptResponse{}, historyErr
			}
		} else {
			_, historyErr = history.RecordPromptHistoryEntry(runtimeHandle.promptHistoryContext(ctx), metadata.PromptHistoryEntry{
				SessionID: runtimeHandle.promptHistorySessionID(),
				Text:      runtimeHandle.promptHistoryText(req.Prompt),
			})
		}
	}
	runtimeHandle.releaseTechnicalContext()
	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	startedAt := time.Now()
	response, runErr := runtimeHandle.submitUserMessage(runCtx, req.Prompt)
	response.Duration = time.Since(startedAt)
	if runErr == nil && historyErr != nil {
		historyErr = errors.Join(historyErr, runtimeHandle.retainedWorkflowDiagnosticsError())
	}
	runErr = errors.Join(runErr, historyErr)
	if runErr != nil {
		return response, runErr
	}
	return response, nil
}

var _ servicecontract.RunPromptService = (*inProcessRunPromptService)(nil)
