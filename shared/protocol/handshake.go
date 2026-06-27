package protocol

import (
	"errors"
	"strings"

	"core/shared/clientui"
)

const (
	MethodHandshake                             = "protocol.handshake"
	MethodServerReadinessGet                    = "server.readiness.get"
	MethodAuthGetBootstrapStatus                = "auth.getBootstrapStatus"
	MethodAuthCompleteBootstrap                 = "auth.completeBootstrap"
	MethodAuthGetStatus                         = "auth.getStatus"
	MethodAttachProject                         = "project.attach"
	MethodAttachSession                         = "session.attach"
	MethodProjectList                           = "project.list"
	MethodProjectHomeList                       = "project.home.list"
	MethodProjectResolvePath                    = "project.resolvePath"
	MethodProjectPlanWorkspaceBinding           = "project.planWorkspaceBinding"
	MethodProjectCreate                         = "project.create"
	MethodProjectEditGet                        = "project.edit.get"
	MethodProjectUpdate                         = "project.update"
	MethodProjectSetDefaultWorkspace            = "project.defaultWorkspace.set"
	MethodProjectWorkspaceList                  = "project.workspace.list"
	MethodProjectUnlinkWorkspace                = "project.unlinkWorkspace"
	MethodProjectDelete                         = "project.delete"
	MethodProjectAttachWorkspace                = "project.attachWorkspace"
	MethodProjectRebindWorkspace                = "project.rebindWorkspace"
	MethodProjectGetOverview                    = "project.getOverview"
	MethodSessionListByProject                  = "session.listByProject"
	MethodWorkflowCreate                        = "workflow.create"
	MethodWorkflowCreateAndLinkProject          = "workflow.createAndLinkProject"
	MethodWorkflowUpdate                        = "workflow.update"
	MethodWorkflowList                          = "workflow.list"
	MethodWorkflowGet                           = "workflow.get"
	MethodWorkflowNodeGroupAdd                  = "workflow.nodeGroup.add"
	MethodWorkflowNodeGroupUpdate               = "workflow.nodeGroup.update"
	MethodWorkflowNodeGroupDelete               = "workflow.nodeGroup.delete"
	MethodWorkflowAddNode                       = "workflow.addNode"
	MethodWorkflowUpdateNode                    = "workflow.updateNode"
	MethodWorkflowAddTransitionGroup            = "workflow.addTransitionGroup"
	MethodWorkflowUpdateTransitionGroup         = "workflow.updateTransitionGroup"
	MethodWorkflowAddEdge                       = "workflow.addEdge"
	MethodWorkflowUpdateEdge                    = "workflow.updateEdge"
	MethodWorkflowLinkProject                   = "workflow.linkProject"
	MethodWorkflowListProjectLinks              = "workflow.listProjectLinks"
	MethodWorkflowSetDefaultProjectLink         = "workflow.setDefaultProjectLink"
	MethodWorkflowUnlinkProject                 = "workflow.unlinkProject"
	MethodWorkflowDeletePreview                 = "workflow.deletePreview"
	MethodWorkflowDelete                        = "workflow.delete"
	MethodWorkflowValidate                      = "workflow.validate"
	MethodWorkflowGraphValidateDraft            = "workflow.graph.validateDraft"
	MethodWorkflowGraphDeriveWiring             = "workflow.graph.deriveWiring"
	MethodWorkflowGraphSavePreview              = "workflow.graph.savePreview"
	MethodWorkflowGraphSave                     = "workflow.graph.save"
	MethodWorkflowTaskCreate                    = "workflow.task.create"
	MethodWorkflowTaskUpdate                    = "workflow.task.update"
	MethodWorkflowTaskStart                     = "workflow.task.start"
	MethodWorkflowTaskInterrupt                 = "workflow.task.interrupt"
	MethodWorkflowTaskResume                    = "workflow.task.resume"
	MethodWorkflowTaskApprove                   = "workflow.task.approve"
	MethodWorkflowTaskMove                      = "workflow.task.move"
	MethodWorkflowTaskComplete                  = "workflow.task.complete"
	MethodWorkflowTaskCancel                    = "workflow.task.cancel"
	MethodWorkflowTaskDelete                    = "workflow.task.delete"
	MethodWorkflowAttentionList                 = "workflow.attention.list"
	MethodWorkflowTaskAttentionList             = "workflow.task.attention.list"
	MethodWorkflowTaskQuestionAnswer            = "workflow.task.question.answer"
	MethodWorkflowTaskCommentAdd                = "workflow.task.comment.add"
	MethodWorkflowTaskCommentList               = "workflow.task.comment.list"
	MethodWorkflowTaskCommentReplace            = "workflow.task.comment.replace"
	MethodWorkflowTaskCommentDelete             = "workflow.task.comment.delete"
	MethodWorkflowTaskActivityList              = "workflow.task.activity.list"
	MethodWorkflowTaskList                      = "workflow.task.list"
	MethodWorkflowBoardGet                      = "workflow.board.get"
	MethodWorkflowBoardNodeCardsList            = "workflow.board.nodeCards.list"
	MethodWorkflowSubscribe                     = "workflow.subscribe"
	MethodWorkflowSubscribeProject              = "workflow.subscribeProject"
	MethodWorkflowEvent                         = "workflow.event"
	MethodWorkflowComplete                      = "workflow.complete"
	MethodWorkflowProjectEvent                  = "workflow.project"
	MethodWorkflowProjectComplete               = "workflow.project.complete"
	MethodWorkflowTaskGet                       = "workflow.task.get"
	MethodSessionPlan                           = "session.plan"
	MethodSessionGetMainView                    = "session.getMainView"
	MethodSessionGetTranscriptPage              = "session.getTranscriptPage"
	MethodSessionGetCommittedTranscriptSuffix   = "session.getCommittedTranscriptSuffix"
	MethodSessionGetInitialInput                = "session.getInitialInput"
	MethodSessionPersistInputDraft              = "session.persistInputDraft"
	MethodSessionRetargetWorkspace              = "session.retargetWorkspace"
	MethodSessionResolveTransition              = "session.resolveTransition"
	MethodSessionRuntimeActivate                = "session.runtime.activate"
	MethodSessionRuntimeRelease                 = "session.runtime.release"
	MethodWorktreeList                          = "worktree.list"
	MethodWorktreeCreateTargetResolve           = "worktree.create_target.resolve"
	MethodWorktreeCreate                        = "worktree.create"
	MethodWorktreeSwitch                        = "worktree.switch"
	MethodWorktreeDelete                        = "worktree.delete"
	MethodRuntimeSetSessionName                 = "runtime.setSessionName"
	MethodRuntimeSetThinkingLevel               = "runtime.setThinkingLevel"
	MethodRuntimeSetFastModeEnabled             = "runtime.setFastModeEnabled"
	MethodRuntimeSetReviewerEnabled             = "runtime.setReviewerEnabled"
	MethodRuntimeSetAutoCompactionEnabled       = "runtime.setAutoCompactionEnabled"
	MethodRuntimeSetQuestionsEnabled            = "runtime.setQuestionsEnabled"
	MethodRuntimeAppendCommittedEntry           = "runtime.appendCommittedEntry"
	MethodRuntimeShouldCompactBeforeUserMessage = "runtime.shouldCompactBeforeUserMessage"
	MethodRuntimeSubmitUserTurn                 = "runtime.submitUserTurn"
	MethodRuntimeSubmitUserShellCommand         = "runtime.submitUserShellCommand"
	MethodRuntimeCompactContext                 = "runtime.compactContext"
	MethodRuntimeCompactContextForPreSubmit     = "runtime.compactContextForPreSubmit"
	MethodRuntimeHasQueuedUserWork              = "runtime.hasQueuedUserWork"
	MethodRuntimeSubmitQueuedUserMessages       = "runtime.submitQueuedUserMessages"
	MethodRuntimeInterrupt                      = "runtime.interrupt"
	MethodRuntimeQueueUserMessage               = "runtime.queueUserMessage"
	MethodRuntimeDiscardQueuedUserMessage       = "runtime.discardQueuedUserMessage"
	MethodRuntimeRecordPromptHistory            = "runtime.recordPromptHistory"
	MethodRuntimeGoalShow                       = "runtime.goal.show"
	MethodRuntimeGoalSet                        = "runtime.goal.set"
	MethodRuntimeGoalPause                      = "runtime.goal.pause"
	MethodRuntimeGoalResume                     = "runtime.goal.resume"
	MethodRuntimeGoalComplete                   = "runtime.goal.complete"
	MethodRuntimeGoalClear                      = "runtime.goal.clear"
	MethodProcessList                           = "process.list"
	MethodProcessGet                            = "process.get"
	MethodProcessKill                           = "process.kill"
	MethodProcessInlineOutput                   = "process.inlineOutput"
	MethodAskListPending                        = "ask.listPendingBySession"
	MethodAskAnswer                             = "ask.answer"
	MethodApprovalListPending                   = "approval.listPendingBySession"
	MethodApprovalAnswer                        = "approval.answer"
	MethodPromptSubscribeActivity               = "prompt.subscribeActivity"
	MethodPromptActivityEvent                   = "prompt.activity"
	MethodPromptActivityComplete                = "prompt.activity.complete"
	MethodRunPrompt                             = "run.prompt"
	MethodRunPromptProgress                     = "run.prompt.progress"
	MethodSessionSubscribeActivity              = "session.subscribeActivity"
	MethodSessionActivityEvent                  = "session.activity"
	MethodSessionActivityComplete               = "session.activity.complete"
	MethodProcessSubscribeOutput                = "process.subscribeOutput"
	MethodProcessOutputEvent                    = "process.output"
	MethodProcessOutputComplete                 = "process.output.complete"
)

type HandshakeRequest struct {
	ProtocolVersion string `json:"protocol_version"`
}

type HandshakeResponse struct {
	Identity ServerIdentity `json:"identity"`
}

type AttachProjectRequest struct {
	ProjectID     string `json:"project_id"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

type AttachSessionRequest struct {
	SessionID string `json:"session_id"`
}

type AttachResponse struct {
	Kind          string `json:"kind"`
	ProjectID     string `json:"project_id,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
}

type SubscribeResponse struct {
	Stream string `json:"stream"`
}

type SessionActivityEventParams struct {
	Event clientui.Event `json:"event"`
}

type ProcessOutputEventParams struct {
	Chunk clientui.ProcessOutputChunk `json:"chunk"`
}

type PromptActivityEventParams struct {
	Event clientui.PendingPromptEvent `json:"event"`
}

type WorkflowProjectEventParams struct {
	Event WorkflowProjectEvent `json:"event"`
}

type WorkflowProjectEvent struct {
	ProjectID        string   `json:"project_id,omitempty"`
	WorkflowID       string   `json:"workflow_id,omitempty"`
	Resource         string   `json:"resource"`
	Action           string   `json:"action"`
	ChangedIDs       []string `json:"changed_ids,omitempty"`
	OccurredAtUnixMs int64    `json:"occurred_at_unix_ms"`
}

type StreamCompleteParams struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (r HandshakeRequest) Validate() error {
	if strings.TrimSpace(r.ProtocolVersion) == "" {
		return errors.New("protocol_version is required")
	}
	return nil
}

func (r AttachProjectRequest) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if trimmed := strings.TrimSpace(r.WorkspaceID); r.WorkspaceID != "" && trimmed == "" {
		return errors.New("workspace_id must not be blank")
	}
	if r.WorkspaceRoot != "" && strings.TrimSpace(r.WorkspaceRoot) == "" {
		return errors.New("workspace_root must not be blank")
	}
	if strings.TrimSpace(r.WorkspaceID) != "" && strings.TrimSpace(r.WorkspaceRoot) != "" {
		return errors.New("workspace_id and workspace_root are mutually exclusive")
	}
	return nil
}

func (r AttachSessionRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("session_id is required")
	}
	return nil
}
