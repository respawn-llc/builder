package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

const (
	MethodHandshake                                     = "protocol.handshake"
	MethodServerReadinessGet                            = "server.readiness.get"
	MethodServerUpdateStatusGet                         = "server.updateStatus.get"
	MethodAuthGetBootstrapStatus                        = "auth.getBootstrapStatus"
	MethodAuthCompleteBootstrap                         = "auth.completeBootstrap"
	MethodAuthAcknowledgeNoAuth                         = "auth.acknowledgeNoAuth"
	MethodAuthGetStatus                                 = "auth.getStatus"
	MethodCapabilityFactsGet                            = "capability.facts.get"
	MethodPromptCommandCatalogGet                       = "promptCommands.catalog.get"
	MethodOnboardingFinalize                            = "onboarding.finalize"
	MethodAttachProject                                 = "project.attach"
	MethodAttachSession                                 = "session.attach"
	MethodProjectList                                   = "project.list"
	MethodProjectHomeList                               = "project.home.list"
	MethodProjectResolvePath                            = "project.resolvePath"
	MethodProjectPlanWorkspaceBinding                   = "project.planWorkspaceBinding"
	MethodProjectCreate                                 = "project.create"
	MethodProjectEditGet                                = "project.edit.get"
	MethodProjectUpdate                                 = "project.update"
	MethodProjectSetDefaultWorkspace                    = "project.defaultWorkspace.set"
	MethodProjectWorkspaceList                          = "project.workspace.list"
	MethodProjectUnlinkWorkspace                        = "project.unlinkWorkspace"
	MethodProjectDelete                                 = "project.delete"
	MethodProjectAttachWorkspace                        = "project.attachWorkspace"
	MethodProjectRebindWorkspace                        = "project.rebindWorkspace"
	MethodProjectGetOverview                            = "project.getOverview"
	MethodSessionPage                                   = "session.page"
	MethodWorkflowCreate                                = "workflow.create"
	MethodWorkflowCreateAndLinkProject                  = "workflow.createAndLinkProject"
	MethodWorkflowUpdate                                = "workflow.update"
	MethodWorkflowList                                  = "workflow.list"
	MethodWorkflowGet                                   = "workflow.get"
	MethodWorkflowNodeGroupAdd                          = "workflow.nodeGroup.add"
	MethodWorkflowNodeGroupUpdate                       = "workflow.nodeGroup.update"
	MethodWorkflowNodeGroupDelete                       = "workflow.nodeGroup.delete"
	MethodWorkflowAddNode                               = "workflow.addNode"
	MethodWorkflowUpdateNode                            = "workflow.updateNode"
	MethodWorkflowAddTransitionGroup                    = "workflow.addTransitionGroup"
	MethodWorkflowUpdateTransitionGroup                 = "workflow.updateTransitionGroup"
	MethodWorkflowAddEdge                               = "workflow.addEdge"
	MethodWorkflowUpdateEdge                            = "workflow.updateEdge"
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
	MethodSessionPlan                                   = "session.plan"
	MethodSessionWorkspaceChatDraft                     = "session.workspaceChatDraft"
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
	MethodProcessSubscribeOutput                        = "process.subscribeOutput"
	MethodProcessOutputEvent                            = "process.output"
	MethodProcessOutputComplete                         = "process.output.complete"
)

type HandshakeRequest struct {
	ProtocolVersion    string              `json:"protocol_version"`
	ClientCapabilities *ClientCapabilities `json:"client_capabilities,omitempty"`
}

type ClientCapabilities struct {
	TranscriptLiveRunFinished bool `json:"transcript_live_run_finished"`
}

type HandshakeResponse struct {
	Identity ServerIdentity `json:"identity"`
}

type AttachProjectWorkspaceKind string

const (
	AttachProjectWorkspaceKindID   AttachProjectWorkspaceKind = "workspace_id"
	AttachProjectWorkspaceKindRoot AttachProjectWorkspaceKind = "workspace_root"
)

type AttachProjectWorkspaceSelector struct {
	workspaceID   *string
	workspaceRoot *string
}

type AttachProjectRequest struct {
	ProjectID string
	workspace *AttachProjectWorkspaceSelector
}

func AttachProjectRequestForDefaultWorkspace(projectID string) (AttachProjectRequest, error) {
	request := AttachProjectRequest{ProjectID: projectID}
	if err := request.Validate(); err != nil {
		return AttachProjectRequest{}, err
	}
	return request, nil
}

func AttachProjectRequestForWorkspaceID(projectID string, workspaceID string) (AttachProjectRequest, error) {
	selector, err := attachProjectWorkspaceIDSelector(workspaceID)
	if err != nil {
		return AttachProjectRequest{}, err
	}
	request := AttachProjectRequest{ProjectID: projectID, workspace: &selector}
	if err := request.Validate(); err != nil {
		return AttachProjectRequest{}, err
	}
	return request, nil
}

func AttachProjectRequestForWorkspaceRoot(projectID string, workspaceRoot string) (AttachProjectRequest, error) {
	selector, err := attachProjectWorkspaceRootSelector(workspaceRoot)
	if err != nil {
		return AttachProjectRequest{}, err
	}
	request := AttachProjectRequest{ProjectID: projectID, workspace: &selector}
	if err := request.Validate(); err != nil {
		return AttachProjectRequest{}, err
	}
	return request, nil
}

func attachProjectWorkspaceIDSelector(workspaceID string) (AttachProjectWorkspaceSelector, error) {
	if err := validateAttachField("workspace_id", workspaceID); err != nil {
		return AttachProjectWorkspaceSelector{}, err
	}
	return AttachProjectWorkspaceSelector{workspaceID: &workspaceID}, nil
}

func attachProjectWorkspaceRootSelector(workspaceRoot string) (AttachProjectWorkspaceSelector, error) {
	if err := validateAttachField("workspace_root", workspaceRoot); err != nil {
		return AttachProjectWorkspaceSelector{}, err
	}
	return AttachProjectWorkspaceSelector{workspaceRoot: &workspaceRoot}, nil
}

func (r AttachProjectRequest) Workspace() (AttachProjectWorkspaceSelector, bool) {
	if r.workspace == nil {
		return AttachProjectWorkspaceSelector{}, false
	}
	return *r.workspace, true
}

func (r AttachProjectRequest) Equal(other AttachProjectRequest) bool {
	if r.ProjectID != other.ProjectID {
		return false
	}
	left, leftPresent := r.Workspace()
	right, rightPresent := other.Workspace()
	return leftPresent == rightPresent && (!leftPresent || left.Equal(right))
}

func (s AttachProjectWorkspaceSelector) WorkspaceID() (string, bool) {
	if s.workspaceID == nil || s.workspaceRoot != nil {
		return "", false
	}
	return *s.workspaceID, true
}

func (s AttachProjectWorkspaceSelector) WorkspaceRoot() (string, bool) {
	if s.workspaceRoot == nil || s.workspaceID != nil {
		return "", false
	}
	return *s.workspaceRoot, true
}

func (s AttachProjectWorkspaceSelector) Equal(other AttachProjectWorkspaceSelector) bool {
	leftID, leftHasID := s.WorkspaceID()
	rightID, rightHasID := other.WorkspaceID()
	if leftHasID || rightHasID {
		return leftHasID && rightHasID && leftID == rightID
	}
	leftRoot, leftHasRoot := s.WorkspaceRoot()
	rightRoot, rightHasRoot := other.WorkspaceRoot()
	return leftHasRoot && rightHasRoot && leftRoot == rightRoot
}

func (s AttachProjectWorkspaceSelector) Validate() error {
	if workspaceID, present := s.WorkspaceID(); present {
		return validateAttachField("workspace_id", workspaceID)
	}
	if workspaceRoot, present := s.WorkspaceRoot(); present {
		return validateAttachField("workspace_root", workspaceRoot)
	}
	return errors.New("workspace selector must contain exactly one variant")
}

func (s AttachProjectWorkspaceSelector) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if workspaceID, present := s.WorkspaceID(); present {
		return json.Marshal(struct {
			Kind        AttachProjectWorkspaceKind `json:"kind"`
			WorkspaceID string                     `json:"workspace_id"`
		}{
			Kind:        AttachProjectWorkspaceKindID,
			WorkspaceID: workspaceID,
		})
	}
	workspaceRoot, _ := s.WorkspaceRoot()
	return json.Marshal(struct {
		Kind          AttachProjectWorkspaceKind `json:"kind"`
		WorkspaceRoot string                     `json:"workspace_root"`
	}{
		Kind:          AttachProjectWorkspaceKindRoot,
		WorkspaceRoot: workspaceRoot,
	})
}

func (s *AttachProjectWorkspaceSelector) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("workspace selector is required")
	}
	var wire struct {
		Kind          *AttachProjectWorkspaceKind `json:"kind"`
		WorkspaceID   *string                     `json:"workspace_id"`
		WorkspaceRoot *string                     `json:"workspace_root"`
	}
	if err := DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire.Kind == nil {
		return errors.New("workspace selector kind is required")
	}
	switch *wire.Kind {
	case AttachProjectWorkspaceKindID:
		if wire.WorkspaceID == nil || wire.WorkspaceRoot != nil {
			return errors.New("workspace_id selector must contain only workspace_id")
		}
		value, err := attachProjectWorkspaceIDSelector(*wire.WorkspaceID)
		if err != nil {
			return err
		}
		*s = value
		return nil
	case AttachProjectWorkspaceKindRoot:
		if wire.WorkspaceRoot == nil || wire.WorkspaceID != nil {
			return errors.New("workspace_root selector must contain only workspace_root")
		}
		value, err := attachProjectWorkspaceRootSelector(*wire.WorkspaceRoot)
		if err != nil {
			return err
		}
		*s = value
		return nil
	default:
		return errors.New("workspace selector kind is invalid")
	}
}

func (r AttachProjectRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ProjectID string                          `json:"project_id"`
		Workspace *AttachProjectWorkspaceSelector `json:"workspace"`
	}{
		ProjectID: r.ProjectID,
		Workspace: r.workspace,
	})
}

func (r *AttachProjectRequest) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("attach project request is required")
	}
	var wire struct {
		ProjectID *string         `json:"project_id"`
		Workspace json.RawMessage `json:"workspace"`
	}
	if err := DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire.ProjectID == nil {
		return errors.New("project_id is required")
	}
	if wire.Workspace == nil {
		return errors.New("workspace selection is required")
	}
	var selector *AttachProjectWorkspaceSelector
	if err := json.Unmarshal(wire.Workspace, &selector); err != nil {
		return err
	}
	value := AttachProjectRequest{ProjectID: *wire.ProjectID, workspace: selector}
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

type AttachSessionRequest struct {
	SessionID string `json:"session_id"`
}

type AttachKind string

const (
	AttachKindProject AttachKind = "project"
	AttachKindSession AttachKind = "session"
)

type ProjectAttachment struct {
	ProjectID     string
	WorkspaceID   string
	WorkspaceRoot string
	selection     *ProjectAttachmentWorkspaceSelection
}

type SessionAttachment struct {
	ProjectID     string
	WorkspaceID   string
	WorkspaceRoot string
	SessionID     string
}

type ProjectAttachmentWorkspaceSelection struct {
	workspaceID   *string
	requestedRoot *string
	canonicalRoot *string
}

type AttachResponse struct {
	project *ProjectAttachment
	session *SessionAttachment
}

func ProjectAttachResponse(projectID string, workspaceID string, workspaceRoot string) (AttachResponse, error) {
	request, err := AttachProjectRequestForDefaultWorkspace(projectID)
	if err != nil {
		return AttachResponse{}, err
	}
	return ProjectAttachResponseForRequest(request, workspaceID, workspaceRoot)
}

func ProjectAttachResponseForRequest(request AttachProjectRequest, workspaceID string, workspaceRoot string) (AttachResponse, error) {
	if err := request.Validate(); err != nil {
		return AttachResponse{}, err
	}
	project := ProjectAttachment{
		ProjectID:     request.ProjectID,
		WorkspaceID:   workspaceID,
		WorkspaceRoot: workspaceRoot,
	}
	if selector, present := request.Workspace(); present {
		if selectedWorkspaceID, selectedByID := selector.WorkspaceID(); selectedByID {
			project.selection = &ProjectAttachmentWorkspaceSelection{workspaceID: &selectedWorkspaceID}
		} else if requestedRoot, selectedByRoot := selector.WorkspaceRoot(); selectedByRoot {
			project.selection = &ProjectAttachmentWorkspaceSelection{
				requestedRoot: &requestedRoot,
				canonicalRoot: &workspaceRoot,
			}
		}
	}
	if err := project.Validate(); err != nil {
		return AttachResponse{}, err
	}
	return AttachResponse{project: &project}, nil
}

func SessionAttachResponse(projectID string, workspaceID string, workspaceRoot string, sessionID string) (AttachResponse, error) {
	session := SessionAttachment{
		ProjectID:     projectID,
		WorkspaceID:   workspaceID,
		WorkspaceRoot: workspaceRoot,
		SessionID:     sessionID,
	}
	if err := session.Validate(); err != nil {
		return AttachResponse{}, err
	}
	return AttachResponse{session: &session}, nil
}

func (r AttachResponse) Project() (ProjectAttachment, bool) {
	if r.project == nil || r.session != nil {
		return ProjectAttachment{}, false
	}
	return *r.project, true
}

func (r AttachResponse) Session() (SessionAttachment, bool) {
	if r.session == nil || r.project != nil {
		return SessionAttachment{}, false
	}
	return *r.session, true
}

func (r AttachResponse) Validate() error {
	switch {
	case r.project != nil && r.session == nil:
		return r.project.Validate()
	case r.session != nil && r.project == nil:
		return r.session.Validate()
	default:
		return errors.New("attachment response must contain exactly one variant")
	}
}

func (r AttachResponse) Equal(other AttachResponse) bool {
	leftProject, leftHasProject := r.Project()
	rightProject, rightHasProject := other.Project()
	if leftHasProject || rightHasProject {
		return leftHasProject && rightHasProject && leftProject.Equal(rightProject)
	}
	leftSession, leftHasSession := r.Session()
	rightSession, rightHasSession := other.Session()
	return leftHasSession && rightHasSession && leftSession == rightSession
}

func (a ProjectAttachment) Equal(other ProjectAttachment) bool {
	if a.ProjectID != other.ProjectID || a.WorkspaceID != other.WorkspaceID || a.WorkspaceRoot != other.WorkspaceRoot {
		return false
	}
	left, leftPresent := a.WorkspaceSelection()
	right, rightPresent := other.WorkspaceSelection()
	if leftPresent != rightPresent {
		return false
	}
	if !leftPresent {
		return true
	}
	leftID, leftHasID := left.WorkspaceID()
	rightID, rightHasID := right.WorkspaceID()
	if leftHasID || rightHasID {
		return leftHasID && rightHasID && leftID == rightID
	}
	leftRequested, leftCanonical, leftHasRoot := left.WorkspaceRoot()
	rightRequested, rightCanonical, rightHasRoot := right.WorkspaceRoot()
	return leftHasRoot && rightHasRoot && leftRequested == rightRequested && leftCanonical == rightCanonical
}

func (a ProjectAttachment) Validate() error {
	if err := validateAttachField("project attachment project_id", a.ProjectID); err != nil {
		return err
	}
	if err := validateAttachField("project attachment workspace_id", a.WorkspaceID); err != nil {
		return err
	}
	if err := validateAttachField("project attachment workspace_root", a.WorkspaceRoot); err != nil {
		return err
	}
	if a.selection != nil {
		if err := a.selection.Validate(); err != nil {
			return err
		}
		if workspaceID, selectedByID := a.selection.WorkspaceID(); selectedByID && workspaceID != a.WorkspaceID {
			return errors.New("project attachment workspace selection does not match workspace_id")
		}
		if _, canonicalRoot, selectedByRoot := a.selection.WorkspaceRoot(); selectedByRoot && canonicalRoot != a.WorkspaceRoot {
			return errors.New("project attachment workspace selection does not match workspace_root")
		}
	}
	return nil
}

func (a ProjectAttachment) WorkspaceSelection() (ProjectAttachmentWorkspaceSelection, bool) {
	if a.selection == nil {
		return ProjectAttachmentWorkspaceSelection{}, false
	}
	return *a.selection, true
}

func (s ProjectAttachmentWorkspaceSelection) WorkspaceID() (string, bool) {
	if s.workspaceID == nil || s.requestedRoot != nil || s.canonicalRoot != nil {
		return "", false
	}
	return *s.workspaceID, true
}

func (s ProjectAttachmentWorkspaceSelection) WorkspaceRoot() (string, string, bool) {
	if s.workspaceID != nil || s.requestedRoot == nil || s.canonicalRoot == nil {
		return "", "", false
	}
	return *s.requestedRoot, *s.canonicalRoot, true
}

func (s ProjectAttachmentWorkspaceSelection) Validate() error {
	if workspaceID, present := s.WorkspaceID(); present {
		return validateAttachField("project attachment selected workspace_id", workspaceID)
	}
	if requestedRoot, canonicalRoot, present := s.WorkspaceRoot(); present {
		if err := validateAttachField("project attachment requested workspace_root", requestedRoot); err != nil {
			return err
		}
		return validateAttachField("project attachment canonical workspace_root", canonicalRoot)
	}
	return errors.New("project attachment workspace selection must contain exactly one variant")
}

func (s ProjectAttachmentWorkspaceSelection) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if workspaceID, present := s.WorkspaceID(); present {
		return json.Marshal(struct {
			Kind        AttachProjectWorkspaceKind `json:"kind"`
			WorkspaceID string                     `json:"workspace_id"`
		}{
			Kind:        AttachProjectWorkspaceKindID,
			WorkspaceID: workspaceID,
		})
	}
	requestedRoot, canonicalRoot, _ := s.WorkspaceRoot()
	return json.Marshal(struct {
		Kind          AttachProjectWorkspaceKind `json:"kind"`
		RequestedRoot string                     `json:"requested_root"`
		CanonicalRoot string                     `json:"canonical_root"`
	}{
		Kind:          AttachProjectWorkspaceKindRoot,
		RequestedRoot: requestedRoot,
		CanonicalRoot: canonicalRoot,
	})
}

func (s *ProjectAttachmentWorkspaceSelection) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("project attachment workspace selection is required")
	}
	var wire struct {
		Kind          *AttachProjectWorkspaceKind `json:"kind"`
		WorkspaceID   *string                     `json:"workspace_id"`
		RequestedRoot *string                     `json:"requested_root"`
		CanonicalRoot *string                     `json:"canonical_root"`
	}
	if err := DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire.Kind == nil {
		return errors.New("project attachment workspace selection kind is required")
	}
	switch *wire.Kind {
	case AttachProjectWorkspaceKindID:
		if wire.WorkspaceID == nil || wire.RequestedRoot != nil || wire.CanonicalRoot != nil {
			return errors.New("project attachment workspace_id selection must contain only workspace_id")
		}
		value := ProjectAttachmentWorkspaceSelection{workspaceID: wire.WorkspaceID}
		if err := value.Validate(); err != nil {
			return err
		}
		*s = value
		return nil
	case AttachProjectWorkspaceKindRoot:
		if wire.WorkspaceID != nil || wire.RequestedRoot == nil || wire.CanonicalRoot == nil {
			return errors.New("project attachment workspace_root selection must contain requested_root and canonical_root")
		}
		value := ProjectAttachmentWorkspaceSelection{
			requestedRoot: wire.RequestedRoot,
			canonicalRoot: wire.CanonicalRoot,
		}
		if err := value.Validate(); err != nil {
			return err
		}
		*s = value
		return nil
	default:
		return errors.New("project attachment workspace selection kind is invalid")
	}
}

func (a SessionAttachment) Validate() error {
	if err := validateAttachField("session attachment project_id", a.ProjectID); err != nil {
		return err
	}
	if err := validateAttachField("session attachment workspace_id", a.WorkspaceID); err != nil {
		return err
	}
	if err := validateAttachField("session attachment workspace_root", a.WorkspaceRoot); err != nil {
		return err
	}
	if err := validateAttachField("session attachment session_id", a.SessionID); err != nil {
		return err
	}
	return nil
}

func validateAttachField(name string, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if trimmed != value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", name)
	}
	return nil
}

func (r AttachResponse) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if project, present := r.Project(); present {
		return json.Marshal(struct {
			Kind               AttachKind                           `json:"kind"`
			ProjectID          string                               `json:"project_id"`
			WorkspaceID        string                               `json:"workspace_id"`
			WorkspaceRoot      string                               `json:"workspace_root"`
			WorkspaceSelection *ProjectAttachmentWorkspaceSelection `json:"workspace_selection"`
		}{
			Kind:               AttachKindProject,
			ProjectID:          project.ProjectID,
			WorkspaceID:        project.WorkspaceID,
			WorkspaceRoot:      project.WorkspaceRoot,
			WorkspaceSelection: project.selection,
		})
	}
	session, _ := r.Session()
	return json.Marshal(struct {
		Kind          AttachKind `json:"kind"`
		ProjectID     string     `json:"project_id"`
		WorkspaceID   string     `json:"workspace_id"`
		WorkspaceRoot string     `json:"workspace_root"`
		SessionID     string     `json:"session_id"`
	}{
		Kind:          AttachKindSession,
		ProjectID:     session.ProjectID,
		WorkspaceID:   session.WorkspaceID,
		WorkspaceRoot: session.WorkspaceRoot,
		SessionID:     session.SessionID,
	})
}

func (r *AttachResponse) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("attachment response is required")
	}
	var wire struct {
		Kind          *AttachKind     `json:"kind"`
		ProjectID     *string         `json:"project_id"`
		WorkspaceID   *string         `json:"workspace_id"`
		WorkspaceRoot *string         `json:"workspace_root"`
		SessionID     *string         `json:"session_id"`
		Selection     json.RawMessage `json:"workspace_selection"`
	}
	if err := DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire.Kind == nil {
		return errors.New("attachment response kind is required")
	}
	if wire.ProjectID == nil || wire.WorkspaceID == nil || wire.WorkspaceRoot == nil {
		return errors.New("attachment response project_id, workspace_id, and workspace_root are required")
	}
	switch *wire.Kind {
	case AttachKindProject:
		if wire.SessionID != nil {
			return errors.New("project attachment must not contain session_id")
		}
		if wire.Selection == nil {
			return errors.New("project attachment workspace_selection is required")
		}
		project := ProjectAttachment{
			ProjectID:     *wire.ProjectID,
			WorkspaceID:   *wire.WorkspaceID,
			WorkspaceRoot: *wire.WorkspaceRoot,
		}
		var selection *ProjectAttachmentWorkspaceSelection
		if err := json.Unmarshal(wire.Selection, &selection); err != nil {
			return err
		}
		project.selection = selection
		if err := project.Validate(); err != nil {
			return err
		}
		*r = AttachResponse{project: &project}
		return nil
	case AttachKindSession:
		if wire.SessionID == nil {
			return errors.New("session attachment session_id is required")
		}
		if wire.Selection != nil {
			return errors.New("session attachment must not contain workspace_selection")
		}
		value, err := SessionAttachResponse(*wire.ProjectID, *wire.WorkspaceID, *wire.WorkspaceRoot, *wire.SessionID)
		if err != nil {
			return err
		}
		*r = value
		return nil
	default:
		return errors.New("attachment response kind is invalid")
	}
}

type SubscribeResponse struct {
	Stream string `json:"stream"`
}

type SessionTranscriptEventParams struct {
	Message clientui.TranscriptMessage `json:"message"`
}

type ProcessOutputEventParams struct {
	Chunk clientui.ProcessOutputChunk `json:"chunk"`
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
	WorkflowProjectEventActionCreated                WorkflowProjectEventAction = "created"
	WorkflowProjectEventActionUpdated                WorkflowProjectEventAction = "updated"
	WorkflowProjectEventActionRenamed                WorkflowProjectEventAction = "renamed"
	WorkflowProjectEventActionReordered              WorkflowProjectEventAction = "reordered"
	WorkflowProjectEventActionDeleted                WorkflowProjectEventAction = "deleted"
	WorkflowProjectEventActionNodeAdded              WorkflowProjectEventAction = "node_added"
	WorkflowProjectEventActionNodeUpdated            WorkflowProjectEventAction = "node_updated"
	WorkflowProjectEventActionNodeGroupAdded         WorkflowProjectEventAction = "node_group_added"
	WorkflowProjectEventActionNodeGroupUpdated       WorkflowProjectEventAction = "node_group_updated"
	WorkflowProjectEventActionNodeGroupDeleted       WorkflowProjectEventAction = "node_group_deleted"
	WorkflowProjectEventActionTransitionGroupAdded   WorkflowProjectEventAction = "transition_group_added"
	WorkflowProjectEventActionTransitionGroupUpdated WorkflowProjectEventAction = "transition_group_updated"
	WorkflowProjectEventActionEdgeAdded              WorkflowProjectEventAction = "edge_added"
	WorkflowProjectEventActionEdgeUpdated            WorkflowProjectEventAction = "edge_updated"
	WorkflowProjectEventActionGraphSaved             WorkflowProjectEventAction = "graph_saved"
	WorkflowProjectEventActionLinked                 WorkflowProjectEventAction = "linked"
	WorkflowProjectEventActionDefaultChanged         WorkflowProjectEventAction = "default_changed"
	WorkflowProjectEventActionUnlinked               WorkflowProjectEventAction = "unlinked"
	WorkflowProjectEventActionStarted                WorkflowProjectEventAction = "started"
	WorkflowProjectEventActionInterrupted            WorkflowProjectEventAction = "interrupted"
	WorkflowProjectEventActionResumed                WorkflowProjectEventAction = "resumed"
	WorkflowProjectEventActionApproved               WorkflowProjectEventAction = "approved"
	WorkflowProjectEventActionMoved                  WorkflowProjectEventAction = "moved"
	WorkflowProjectEventActionCanceled               WorkflowProjectEventAction = "canceled"
	WorkflowProjectEventActionCompleted              WorkflowProjectEventAction = "completed"
	WorkflowProjectEventActionCommentAdded           WorkflowProjectEventAction = "comment_added"
	WorkflowProjectEventActionCommentUpdated         WorkflowProjectEventAction = "comment_updated"
	WorkflowProjectEventActionCommentDeleted         WorkflowProjectEventAction = "comment_deleted"
	WorkflowProjectEventActionQuestionWaiting        WorkflowProjectEventAction = "question_waiting"
	WorkflowProjectEventActionQuestionCleared        WorkflowProjectEventAction = "question_cleared"
	WorkflowProjectEventActionLabelsChanged          WorkflowProjectEventAction = "labels_changed"
	WorkflowProjectEventActionDependenciesChanged    WorkflowProjectEventAction = "dependencies_changed"
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

func (r HandshakeRequest) Validate() error {
	if strings.TrimSpace(r.ProtocolVersion) == "" {
		return errors.New("protocol_version is required")
	}
	if r.ClientCapabilities != nil && !r.ClientCapabilities.TranscriptLiveRunFinished {
		return errors.New("client_capabilities must advertise at least one supported capability")
	}
	return nil
}

func (r AttachProjectRequest) Validate() error {
	if err := validateAttachField("project_id", r.ProjectID); err != nil {
		return err
	}
	if r.workspace != nil {
		return r.workspace.Validate()
	}
	return nil
}

func (r AttachSessionRequest) Validate() error {
	return validateAttachField("session_id", r.SessionID)
}
