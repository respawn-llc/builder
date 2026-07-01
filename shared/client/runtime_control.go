package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type RuntimeControlClient = servicecontract.RuntimeControlService
type loopbackRuntimeControlClient struct {
	loopbackClient[servicecontract.RuntimeControlService]
}

func NewLoopbackRuntimeControlClient(service servicecontract.RuntimeControlService) RuntimeControlClient {
	return &loopbackRuntimeControlClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackRuntimeControlClient) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetSessionName)
}

func (c *loopbackRuntimeControlClient) SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetThinkingLevel)
}

func (c *loopbackRuntimeControlClient) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetFastModeEnabled)
}

func (c *loopbackRuntimeControlClient) SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetReviewerEnabled)
}

func (c *loopbackRuntimeControlClient) SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetAutoCompactionEnabled)
}

func (c *loopbackRuntimeControlClient) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetQuestionsEnabled)
}

func (c *loopbackRuntimeControlClient) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.AppendCommittedEntry)
}

func (c *loopbackRuntimeControlClient) ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.ShouldCompactBeforeUserMessage)
}

func (c *loopbackRuntimeControlClient) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SubmitUserTurn)
}

func (c *loopbackRuntimeControlClient) SubmitUserShellCommand(ctx context.Context, req serverapi.RuntimeSubmitUserShellCommandRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SubmitUserShellCommand)
}

func (c *loopbackRuntimeControlClient) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.CompactContext)
}

func (c *loopbackRuntimeControlClient) CompactContextForPreSubmit(ctx context.Context, req serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.CompactContextForPreSubmit)
}

func (c *loopbackRuntimeControlClient) HasQueuedUserWork(ctx context.Context, req serverapi.RuntimeHasQueuedUserWorkRequest) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.HasQueuedUserWork)
}

func (c *loopbackRuntimeControlClient) SubmitQueuedUserMessages(ctx context.Context, req serverapi.RuntimeSubmitQueuedUserMessagesRequest) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SubmitQueuedUserMessages)
}

func (c *loopbackRuntimeControlClient) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.Interrupt)
}

func (c *loopbackRuntimeControlClient) QueueUserMessage(ctx context.Context, req serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.QueueUserMessage)
}

func (c *loopbackRuntimeControlClient) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.DiscardQueuedUserMessage)
}

func (c *loopbackRuntimeControlClient) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	return callLoopbackClientNoResponse(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.RecordPromptHistory)
}

func (c *loopbackRuntimeControlClient) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.ShowGoal)
}

func (c *loopbackRuntimeControlClient) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.SetGoal)
}

func (c *loopbackRuntimeControlClient) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.PauseGoal)
}

func (c *loopbackRuntimeControlClient) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.ResumeGoal)
}

func (c *loopbackRuntimeControlClient) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.CompleteGoal)
}

func (c *loopbackRuntimeControlClient) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callLoopbackClient(c, "runtime control service is required", ctx, req, servicecontract.RuntimeControlService.ClearGoal)
}
