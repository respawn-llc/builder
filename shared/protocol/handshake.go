package protocol

import (
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

const (
	MethodChatContextGet                                = "chat.context.get"
	MethodPromptCommandCatalogGet                       = "promptCommands.catalog.get"
	MethodWorkflowCreate                                = "workflow.create"
	MethodWorkflowCreateAndLinkProject                  = "workflow.createAndLinkProject"
	MethodWorkflowUpdate                                = "workflow.update"
	MethodWorkflowList                                  = "workflow.list"
	MethodWorkflowGet                                   = "workflow.get"
	MethodWorkflowLinkProject                           = "workflow.linkProject"
	MethodWorkflowListProjectLinks                      = "workflow.listProjectLinks"
	MethodWorkflowSetDefaultProjectLink                 = "workflow.setDefaultProjectLink"
	MethodWorkflowUnlinkProject                         = "workflow.unlinkProject"
	MethodWorkflowDeletePreview                         = "workflow.deletePreview"
	MethodWorkflowDelete                                = "workflow.delete"
	MethodWorkflowValidate                              = "workflow.validate"
	MethodWorkflowScriptPathValidate                    = "workflow.scriptPath.validate"
	MethodWorkflowGraphValidateDraft                    = "workflow.graph.validateDraft"
	MethodWorkflowGraphDeriveWiring                     = "workflow.graph.deriveWiring"
	MethodWorkflowGraphSavePreview                      = "workflow.graph.savePreview"
	MethodWorkflowGraphSave                             = "workflow.graph.save"
	MethodWorkflowProjectLabelCreate                    = "workflow.project.label.create"
	MethodWorkflowProjectLabelList                      = "workflow.project.label.list"
	MethodWorkflowProjectLabelRename                    = "workflow.project.label.rename"
	MethodWorkflowProjectLabelDelete                    = "workflow.project.label.delete"
	MethodWorkflowProjectLabelReorder                   = "workflow.project.label.reorder"
	MethodWorkflowTaskLabelsGet                         = "workflow.task.labels.get"
	MethodWorkflowTaskLabelsUpdate                      = "workflow.task.labels.update"
	MethodWorkflowTaskCreate                            = "workflow.task.create"
	MethodWorkflowTaskUpdate                            = "workflow.task.update"
	MethodWorkflowTaskStart                             = "workflow.task.start"
	MethodWorkflowTaskInterrupt                         = "workflow.task.interrupt"
	MethodWorkflowTaskResume                            = "workflow.task.resume"
	MethodWorkflowTaskApprove                           = "workflow.task.approve"
	MethodWorkflowTaskMovePreview                       = "workflow.task.move.preview"
	MethodWorkflowTaskMove                              = "workflow.task.move"
	MethodWorkflowTaskComplete                          = "workflow.task.complete"
	MethodWorkflowTaskDelete                            = "workflow.task.delete"
	MethodWorkflowTaskDependencyAdd                     = "workflow.task.dependency.add"
	MethodWorkflowTaskDependencyRemove                  = "workflow.task.dependency.remove"
	MethodWorkflowTaskDependencyList                    = "workflow.task.dependency.list"
	MethodWorkflowAttentionList                         = "workflow.attention.list"
	MethodWorkflowTaskAttentionList                     = "workflow.task.attention.list"
	MethodWorkflowTaskCommentAdd                        = "workflow.task.comment.add"
	MethodWorkflowTaskCommentList                       = "workflow.task.comment.list"
	MethodWorkflowTaskCommentReplace                    = "workflow.task.comment.replace"
	MethodWorkflowTaskCommentDelete                     = "workflow.task.comment.delete"
	MethodWorkflowTaskActivityList                      = "workflow.task.activity.list"
	MethodWorkflowTaskSessionList                       = "workflow.task.session.list"
	MethodWorkflowTaskList                              = "workflow.task.list"
	MethodWorkflowProjectTaskGroupCounts                = "workflow.task.groupCounts"
	MethodWorkflowTaskSearch                            = "workflow.task.search"
	MethodWorkflowBoardGet                              = "workflow.board.get"
	MethodWorkflowBoardNodeCardsList                    = "workflow.board.nodeCards.list"
	MethodWorkflowSubscribe                             = "workflow.subscribe"
	MethodWorkflowSubscribeProject                      = "workflow.subscribeProject"
	MethodWorkflowEvent                                 = "workflow.event"
	MethodWorkflowComplete                              = "workflow.complete"
	MethodWorkflowProjectEvent                          = "workflow.project"
	MethodWorkflowProjectComplete                       = "workflow.project.complete"
	MethodWorkflowTaskGet                               = "workflow.task.get"
	MethodWorkflowTaskObserve                           = "workflow.task.observe"
	MethodSessionGetMainView                            = "session.getMainView"
	MethodSessionGetExecutionEnvironment                = "session.getExecutionEnvironment"
	MethodSessionGetTranscriptPage                      = "session.getTranscriptPage"
	MethodSessionGetLatestCommittedAssistantFinalAnswer = "session.getLatestCommittedAssistantFinalAnswer"
	MethodSessionGetInitialInput                        = "session.getInitialInput"
	MethodSessionPersistInputDraft                      = "session.persistInputDraft"
	MethodSessionRetargetWorkspace                      = "session.retargetWorkspace"
	MethodSessionResolveTransition                      = "session.resolveTransition"
	MethodSessionRuntimeActivate                        = "session.runtime.activate"
	MethodSessionRuntimeRelease                         = "session.runtime.release"
	MethodRuntimeSetSessionName                         = "runtime.setSessionName"
	MethodRuntimeAppendCommittedEntry                   = "runtime.appendCommittedEntry"
	MethodRuntimeShouldCompactBeforeUserMessage         = "runtime.shouldCompactBeforeUserMessage"
	MethodRuntimeSubmitUserTurn                         = "runtime.submitUserTurn"
	MethodRuntimeSubmitUserShellCommand                 = "runtime.submitUserShellCommand"
	MethodRuntimeCompactContext                         = "runtime.compactContext"
	MethodRuntimeInterrupt                              = "runtime.interrupt"
	MethodRuntimeLiveSteer                              = "runtime.liveSteer"
	MethodRuntimeLiveStop                               = "runtime.liveStop"
	MethodRuntimeLiveWait                               = "runtime.liveWait"
	MethodRuntimeLiveWatch                              = "runtime.liveWatch"
	MethodRuntimeListPendingWork                        = "runtime.pendingWork.list"
	MethodRuntimeRemovePendingWork                      = "runtime.pendingWork.remove"
	MethodRuntimeRecordPromptHistory                    = "runtime.recordPromptHistory"
	MethodRuntimeGoalShow                               = "runtime.goal.show"
	MethodRuntimeGoalSet                                = "runtime.goal.set"
	MethodRuntimeGoalPause                              = "runtime.goal.pause"
	MethodRuntimeGoalResume                             = "runtime.goal.resume"
	MethodRuntimeGoalComplete                           = "runtime.goal.complete"
	MethodRuntimeGoalClear                              = "runtime.goal.clear"
	MethodProcessList                                   = "process.list"
	MethodProcessGet                                    = "process.get"
	MethodProcessKill                                   = "process.kill"
	MethodProcessInlineOutput                           = "process.inlineOutput"
	MethodAskListPending                                = "ask.listPendingBySession"
	MethodPromptAnswerBatch                             = "prompt.answerBatch"
	MethodPromptFollowUpWatch                           = "prompt.followUp.watch"
	MethodPromptFollowUpEvent                           = "prompt.followUp.event"
	MethodPromptFollowUpComplete                        = "prompt.followUp.complete"
	MethodApprovalListPending                           = "approval.listPendingBySession"
	MethodAttentionNotificationSubscribe                = "attention.notification.subscribe"
	MethodAttentionNotificationEvent                    = "attention.notification"
	MethodAttentionNotificationComplete                 = "attention.notification.complete"
	MethodAttentionSessionNotificationSubscribe         = "attention.sessionNotification.subscribe"
	MethodAttentionSessionNotificationEvent             = "attention.sessionNotification"
	MethodAttentionSessionNotificationComplete          = "attention.sessionNotification.complete"
	MethodRunPrompt                                     = "run.prompt"
	MethodRunPromptProgress                             = "run.prompt.progress"
	MethodSessionSubscribeTranscript                    = "session.subscribeTranscript"
	MethodSessionTranscriptEvent                        = "session.transcript"
	MethodSessionTranscriptComplete                     = "session.transcript.complete"
	MethodSessionQuestionHistorySubscribe               = "session.questionHistory.subscribe"
	MethodSessionQuestionHistoryEvent                   = "session.questionHistory.event"
	MethodSessionQuestionHistoryComplete                = "session.questionHistory.complete"
)

type SubscribeResponse struct {
	Stream string `json:"stream"`
}

type SessionTranscriptEventParams struct {
	Message clientui.TranscriptMessage `json:"message"`
}

type SessionQuestionHistoryEventParams struct {
	Event SessionQuestionHistoryEvent `json:"event"`
}

type SessionQuestionHistoryEvent struct {
	Kind           string                          `json:"kind"`
	LargeHistory   *bool                           `json:"large_history,omitempty"`
	Question       *SessionQuestionHistoryQuestion `json:"question,omitempty"`
	HistoryOmitted *bool                           `json:"history_omitted,omitempty"`
}

type SessionQuestionHistoryQuestion struct {
	Question             string                        `json:"question"`
	Answer               string                        `json:"answer"`
	SelectedOptionNumber *int                          `json:"selected_option_number"`
	Commentary           *string                       `json:"commentary"`
	At                   *transcript.CommittedAtUnixMs `json:"at"`
}

type AttentionNotificationEventParams struct {
	Event clientui.AttentionNotificationEvent `json:"event"`
}

type PromptFollowUpEventParams struct {
	Event PromptFollowUpEvent `json:"event"`
}

type PromptFollowUpEvent struct {
	Kind string `json:"kind"`
}

type WorkflowProjectEventParams struct {
	Event WorkflowProjectEvent `json:"event"`
}

type WorkflowProjectEventResource string

const (
	WorkflowProjectEventResourceWorkflow     WorkflowProjectEventResource = "workflow"
	WorkflowProjectEventResourceWorkflowLink WorkflowProjectEventResource = "workflow_link"
	WorkflowProjectEventResourceTask         WorkflowProjectEventResource = "task"
	WorkflowProjectEventResourceLabel        WorkflowProjectEventResource = "label"
)

type WorkflowProjectEventAction string

const (
	WorkflowProjectEventActionCreated             WorkflowProjectEventAction = "created"
	WorkflowProjectEventActionUpdated             WorkflowProjectEventAction = "updated"
	WorkflowProjectEventActionRenamed             WorkflowProjectEventAction = "renamed"
	WorkflowProjectEventActionReordered           WorkflowProjectEventAction = "reordered"
	WorkflowProjectEventActionDeleted             WorkflowProjectEventAction = "deleted"
	WorkflowProjectEventActionGraphSaved          WorkflowProjectEventAction = "graph_saved"
	WorkflowProjectEventActionLinked              WorkflowProjectEventAction = "linked"
	WorkflowProjectEventActionDefaultChanged      WorkflowProjectEventAction = "default_changed"
	WorkflowProjectEventActionUnlinked            WorkflowProjectEventAction = "unlinked"
	WorkflowProjectEventActionStarted             WorkflowProjectEventAction = "started"
	WorkflowProjectEventActionInterrupted         WorkflowProjectEventAction = "interrupted"
	WorkflowProjectEventActionResumed             WorkflowProjectEventAction = "resumed"
	WorkflowProjectEventActionApproved            WorkflowProjectEventAction = "approved"
	WorkflowProjectEventActionMoved               WorkflowProjectEventAction = "moved"
	WorkflowProjectEventActionCanceled            WorkflowProjectEventAction = "canceled"
	WorkflowProjectEventActionCompleted           WorkflowProjectEventAction = "completed"
	WorkflowProjectEventActionCommentAdded        WorkflowProjectEventAction = "comment_added"
	WorkflowProjectEventActionCommentUpdated      WorkflowProjectEventAction = "comment_updated"
	WorkflowProjectEventActionCommentDeleted      WorkflowProjectEventAction = "comment_deleted"
	WorkflowProjectEventActionQuestionWaiting     WorkflowProjectEventAction = "question_waiting"
	WorkflowProjectEventActionQuestionCleared     WorkflowProjectEventAction = "question_cleared"
	WorkflowProjectEventActionLabelsChanged       WorkflowProjectEventAction = "labels_changed"
	WorkflowProjectEventActionDependenciesChanged WorkflowProjectEventAction = "dependencies_changed"
)

type WorkflowProjectEvent struct {
	ProjectID        *string                      `json:"project_id,omitempty"`
	WorkflowID       *runtimeids.WorkflowID       `json:"workflow_id,omitempty"`
	Resource         WorkflowProjectEventResource `json:"resource"`
	Action           WorkflowProjectEventAction   `json:"action"`
	PrimaryEntityID  string                       `json:"primary_entity_id"`
	RelatedIDs       []string                     `json:"related_ids,omitempty"`
	OccurredAtUnixMs int64                        `json:"occurred_at_unix_ms"`
}

type StreamCompleteParams struct {
	Code                  int    `json:"code,omitempty"`
	Message               string `json:"message,omitempty"`
	TranscriptCloseReason string `json:"transcript_close_reason,omitempty"`
}
