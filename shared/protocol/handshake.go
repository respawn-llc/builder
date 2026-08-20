package protocol

import (
	"encoding/json"

	"core/shared/clientui"
	"core/shared/runtimeids"
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
	MethodChatSettingsRead                              = "chat.settings.read"
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
	MethodWorktreeList                                  = "worktree.list"
	MethodWorktreeWorkspaceList                         = "worktree.workspace.list"
	MethodWorktreeStatus                                = "worktree.status"
	MethodWorktreeSelectorResolve                       = "worktree.selector.resolve"
	MethodWorktreeDeletePreview                         = "worktree.deletePreview"
	MethodWorktreeCreateTargetResolve                   = "worktree.create_target.resolve"
	MethodWorktreeCreate                                = "worktree.create"
	MethodWorktreeEnter                                 = "worktree.enter"
	MethodWorktreeLeave                                 = "worktree.leave"
	MethodWorktreeDelete                                = "worktree.delete"
	MethodWorktreeSetupSubscribe                        = "worktree.setup.subscribe"
	MethodWorktreeSetupEvent                            = "worktree.setup"
	MethodWorktreeSetupComplete                         = "worktree.setup.complete"
	MethodRuntimeSetSessionName                         = "runtime.setSessionName"
	MethodRuntimeSetThinkingLevel                       = "runtime.setThinkingLevel"
	MethodRuntimeSetFastModeEnabled                     = "runtime.setFastModeEnabled"
	MethodRuntimeSetReviewerEnabled                     = "runtime.setReviewerEnabled"
	MethodRuntimeSetAutoCompactionEnabled               = "runtime.setAutoCompactionEnabled"
	MethodRuntimeSetQuestionsEnabled                    = "runtime.setQuestionsEnabled"
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
	MethodRuntimeDiscardQueuedUserMessage               = "runtime.discardQueuedUserMessage"
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
)

type SubscribeResponse struct {
	Stream string `json:"stream"`
}

type SessionTranscriptEventParams struct {
	Message clientui.TranscriptMessage `json:"message"`
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

type WorktreeSetupEventParams struct {
	Event WorktreeSetupEvent `json:"event"`
}

type WorktreeSetupEvent struct {
	SetupOperationID string          `json:"setup_operation_id"`
	Phase            string          `json:"phase"`
	Started          json.RawMessage `json:"started,omitempty"`
	Completed        json.RawMessage `json:"completed,omitempty"`
	NotRequired      json.RawMessage `json:"not_required,omitempty"`
	Failed           json.RawMessage `json:"failed,omitempty"`
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
