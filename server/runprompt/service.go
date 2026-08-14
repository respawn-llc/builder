package runprompt

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/requestmemo"
	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type runPromptMemoRequest struct {
	Intent          serverapi.SessionLaunchIntent
	Prompt          string
	Timeout         string
	CallerSessionID serverapi.OptionalStringKey
	Overrides       serverapi.RunPromptOverridesKey
}

type inProcessRunPromptService struct {
	launcher *headlessPromptLauncher
	runs     *requestmemo.Memo[runPromptMemoRequest, serverapi.RunPromptResponse]
}

func (s *inProcessRunPromptService) RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RunPromptRequest]) (serverapi.RunPromptResponse, error) {
		return s.RunPromptValidated(ctx, validated, progress)
	})
}

func (s *inProcessRunPromptService) RunPromptValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RunPromptRequest], progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	req := prepareRunPromptRequest(validated.Value())
	overrides, err := req.Overrides.CanonicalKey()
	if err != nil {
		return serverapi.RunPromptResponse{}, err
	}
	memoReq := runPromptMemoRequest{
		Intent:          req.Intent,
		Prompt:          req.Prompt,
		Timeout:         req.Timeout.String(),
		CallerSessionID: serverapi.CanonicalOptionalString(req.CallerSessionID),
		Overrides:       overrides,
	}
	return s.runs.Do(ctx, req.ClientRequestID, memoReq, sameRunPromptMemoRequest, func(ctx context.Context) (serverapi.RunPromptResponse, error) {
		return s.runPrompt(ctx, req, progress)
	})
}

func prepareRunPromptRequest(source serverapi.RunPromptRequest) serverapi.RunPromptRequest {
	prepared := source
	prepared.ClientRequestID = strings.TrimSpace(source.ClientRequestID)
	prepared.Prompt = strings.TrimSpace(source.Prompt)
	if source.CallerSessionID != nil {
		callerSessionID := *source.CallerSessionID
		prepared.CallerSessionID = &callerSessionID
	}
	if source.Overrides.AgentRole != nil {
		agentRole := *source.Overrides.AgentRole
		prepared.Overrides.AgentRole = &agentRole
	}
	return prepared
}

func (s *inProcessRunPromptService) runPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (response serverapi.RunPromptResponse, err error) {
	if s == nil || s.launcher == nil {
		return serverapi.RunPromptResponse{}, errors.New("run prompt service is not configured")
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

func sameRunPromptMemoRequest(a runPromptMemoRequest, b runPromptMemoRequest) bool {
	return a.Intent.Equal(b.Intent) &&
		a.Prompt == b.Prompt &&
		a.Timeout == b.Timeout &&
		a.CallerSessionID == b.CallerSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.RunPromptService = (*inProcessRunPromptService)(nil)
