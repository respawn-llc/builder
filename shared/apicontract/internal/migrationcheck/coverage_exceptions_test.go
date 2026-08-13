package migrationcheck

import (
	"reflect"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/rollbacktarget"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	WireExceptionCustomWire = iota
	WireExceptionOneofReshape
	WireExceptionFieldReshape
	WireExceptionCollectionReshape
	WireExceptionEmptyAcknowledment
)

func actualTargetWireExceptions() []WireException {
	return []WireException{
		wireException[serverapi.OnboardingImportSelection]("kent.api.onboarding.ImportSelection", WireExceptionCustomWire),
		wireException[serverapi.SessionLaunchIntent]("kent.api.session_launch.SessionLaunchIntent", WireExceptionCustomWire),
		wireException[serverapi.SessionExecutionEnvironment]("kent.api.session.ExecutionEnvironment", WireExceptionCustomWire),

		wireException[serverapi.AskListPendingBySessionResponse]("kent.api.prompt.ListQuestionsSuccess", WireExceptionFieldReshape),
		wireException[protocol.AttentionNotificationEventParams]("kent.api.attention.NotificationEvent", WireExceptionOneofReshape),
		wireException[protocol.AttentionNotificationEventParams]("kent.api.workflow_task.AttentionNotificationEvent", WireExceptionOneofReshape),
		wireException[protocol.SubscribeResponse]("google.protobuf.Empty", WireExceptionEmptyAcknowledment),
		wireException[serverapi.AuthStatusResolution]("kent.api.auth.StatusResolution", WireExceptionOneofReshape),
		wireException[clientui.BackgroundProcess]("kent.api.process.BackgroundProcess", WireExceptionFieldReshape),
		wireException[serverapi.ProcessInlineOutputResponse]("kent.api.process.InlineOutputSuccess", WireExceptionFieldReshape),
		wireException[protocol.ProcessOutputEventParams]("kent.api.process.OutputChunk", WireExceptionFieldReshape),
		wireException[protocol.AttachProjectRequest]("kent.api.connection.AttachProjectRequest", WireExceptionOneofReshape),
		wireException[protocol.AttachResponse]("kent.api.connection.AttachmentSuccess", WireExceptionOneofReshape),
		wireException[serverapi.ProjectBinding]("kent.api.project.ProjectMutationBinding", WireExceptionFieldReshape),
		wireException[serverapi.ProjectDefaultWorkspaceSetRequest]("kent.api.project.SetDefaultWorkspaceRequest", WireExceptionFieldReshape),
		wireException[serverapi.ProjectHomeSummary]("kent.api.project.ProjectHomeSummary", WireExceptionFieldReshape),
		wireException[serverapi.ProjectWorkspaceSummary]("kent.api.project.ProjectWorkspaceCatalogSummary", WireExceptionFieldReshape),
		wireException[serverapi.ProjectWorkspaceSummary]("kent.api.workflow_task.TaskSourceWorkspace", WireExceptionFieldReshape),
		wireException[serverapi.ProjectHomeListResponse]("kent.api.project.ProjectHomeListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.ProjectWorkspaceUnlinkRequest]("kent.api.project.UnlinkWorkspaceRequest", WireExceptionFieldReshape),
		wireException[protocol.PromptFollowUpEventParams]("kent.api.prompt.FollowUpEvent", WireExceptionFieldReshape),
		wireException[serverapi.RunPromptProgress]("kent.api.run_prompt.ProgressEvent", WireExceptionOneofReshape),
		wireException[serverapi.RunPromptResponse]("kent.api.run_prompt.Success", WireExceptionFieldReshape),
		wireException[serverapi.RuntimeLiveWaitResponse]("kent.api.runtime.LiveWaitSuccess", WireExceptionOneofReshape),
		wireException[serverapi.RuntimeLiveWatchOutcome]("kent.api.prompt.LiveWatchOutcome", WireExceptionOneofReshape),
		wireException[runtimeinput.Input]("kent.api.runtime.UserTurnInput", WireExceptionFieldReshape),
		wireException[serverapi.RuntimeSubmitUserTurnResponse]("kent.api.runtime.SubmitUserTurnSuccess", WireExceptionOneofReshape),
		wireException[serverapi.ServerReadinessResponse]("kent.api.server.GetReadinessSuccess", WireExceptionFieldReshape),
		wireException[serverapi.UpdateStatusResponse]("kent.api.server.GetUpdateStatusSuccess", WireExceptionFieldReshape),
		wireException[clientui.RuntimeContextUsage]("kent.api.runtime.ContextUsage", WireExceptionFieldReshape),
		wireException[clientui.TranscriptCommittedRow]("kent.api.transcript.CommittedRow", WireExceptionFieldReshape),
		wireException[rollbacktarget.CandidateLocator]("kent.api.transcript.RollbackCandidate", WireExceptionFieldReshape),
		wireException[config.Settings]("kent.api.session_launch.Settings", WireExceptionFieldReshape),
		wireException[config.SourceReport]("kent.api.session_launch.SourceReport", WireExceptionFieldReshape),
		wireException[serverapi.SessionDirective]("kent.api.session_launch.SessionDirective", WireExceptionOneofReshape),
		wireException[protocol.SessionTranscriptEventParams]("kent.api.transcript.Message", WireExceptionOneofReshape),
		wireException[serverapi.WorkspaceChatDraftRequest]("kent.api.session_launch.WorkspaceChatDraftRequest", WireExceptionOneofReshape),
		wireException[serverapi.WorkflowAttentionListResponse]("kent.api.workflow_task.AttentionListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskLabelFilter]("kent.api.workflow_task.LabelFilter", WireExceptionOneofReshape),
		wireException[serverapi.WorkflowBoard]("kent.api.workflow_task.Board", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowBoardNodeCardsListResponse]("kent.api.workflow_task.BoardNodeCardsListSuccess", WireExceptionFieldReshape),
		wireException[protocol.WorkflowProjectEventParams]("kent.api.workflow_definition.ProjectEvent", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskActivityListResponse]("kent.api.workflow_task.ActivityListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskApproveResponse]("kent.api.workflow_task.ApproveSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskAttentionListResponse]("kent.api.workflow_task.TaskAttentionListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskComment]("kent.api.workflow_task.Comment", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskCommentListResponse]("kent.api.workflow_task.CommentListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskSummary]("kent.api.workflow_task.TaskSummary", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskListResponse]("kent.api.workflow_task.ListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskMovePreviewResponse]("kent.api.workflow_task.MovePreviewSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskMoveResponse]("kent.api.workflow_task.MoveSuccess", WireExceptionOneofReshape),
		wireException[serverapi.WorkflowTaskObservationOutcome]("kent.api.workflow_task.ObserveOutcome", WireExceptionOneofReshape),
		wireException[serverapi.WorkflowTaskResumeResponse]("kent.api.workflow_task.ResumeSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskSessionListResponse]("kent.api.workflow_task.SessionListSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorkflowTaskStartResponse]("kent.api.workflow_task.StartSuccess", WireExceptionOneofReshape),
		wireException[serverapi.WorkflowGraphSaveResponse]("kent.api.workflow_definition.GraphSaveSuccess", WireExceptionCollectionReshape),
		wireException[serverapi.WorkflowGraphSavePreviewResponse]("kent.api.workflow_definition.GraphSavePreviewSuccess", WireExceptionCollectionReshape),
		wireException[serverapi.WorkflowGraphValidateDraftResponse]("kent.api.workflow_definition.GraphValidateDraftSuccess", WireExceptionCollectionReshape),
		wireException[serverapi.WorkflowTaskCompleteRequest]("kent.api.workflow_task.CompleteRequest", WireExceptionCollectionReshape),
		wireException[serverapi.WorkflowTaskMoveRequest]("kent.api.workflow_task.MoveRequest", WireExceptionCollectionReshape),
		wireException[serverapi.WorktreeTopologyEntry]("kent.api.worktree.TopologyEntry", WireExceptionOneofReshape),
		wireException[serverapi.WorktreeDeleteRequest]("kent.api.worktree.DeleteRequest", WireExceptionFieldReshape),
		wireException[serverapi.WorktreeDeleteResult]("kent.api.worktree.DeleteSuccess", WireExceptionFieldReshape),
		wireException[serverapi.WorktreeEnterRequest]("kent.api.worktree.EnterRequest", WireExceptionFieldReshape),
		wireException[serverapi.WorktreeLeaveRequest]("kent.api.worktree.LeaveRequest", WireExceptionFieldReshape),
		wireException[protocol.StreamCompleteParams]("kent.api.worktree.SetupCompletion", WireExceptionFieldReshape),
		wireException[protocol.WorktreeSetupEventParams]("kent.api.worktree.SetupEvent", WireExceptionOneofReshape),
	}
}

func actualTargetFieldRenames() []WireFieldRename {
	return []WireFieldRename{
		fieldRename[serverapi.AuthCompleteBootstrapRequest]("kent.api.auth.CompleteBootstrapRequest", "OAuthCodeVerifier", "oauth_code_verifier"),
		fieldRename[serverapi.AuthCompleteBootstrapRequest]("kent.api.auth.CompleteBootstrapRequest", "OAuthState", "oauth_state"),
		fieldRename[serverapi.AuthGetBootstrapStatusResponse]("kent.api.auth.BootstrapStatus", "OAuth", "oauth"),
		fieldRename[serverapi.AuthProviderSelection]("kent.api.auth.ProviderSelection", "OpenAIBaseURL", "openai_base_url"),
		fieldRename[serverapi.AuthProviderCapabilitySelection]("kent.api.auth.ProviderCapabilitySelection", "IsOpenAIFirstParty", "is_openai_first_party"),
		fieldRename[serverapi.AuthSubscriptionWindowFacts]("kent.api.auth.SubscriptionWindowFacts", "DurationSecs", "duration_seconds"),
		fieldRename[serverapi.CapabilityFactsRequest]("kent.api.capability.GetFactsRequest", "ExplicitLLMProviderIDs", "explicit_llm_provider_ids"),
		fieldRename[serverapi.LLMProviderCapabilityFact]("kent.api.capability.ProviderFact", "IsOpenAIFirstParty", "is_openai_first_party"),
		fieldRename[serverapi.OnboardingProviderChoice]("kent.api.onboarding.ProviderChoice", "OpenAIBaseURL", "openai_base_url"),
		fieldRename[serverapi.RunPromptOverrides]("kent.api.session_launch.RunPromptOverrides", "OpenAIBaseURL", "openai_base_url"),
		fieldRename[serverapi.SessionPlan]("kent.api.session_launch.SessionPlan", "EnabledToolIDs", "enabled_tool_ids"),
		fieldRename[serverapi.SessionRuntimeActivateRequest]("kent.api.session_launch.SessionRuntimeActivateRequest", "EnabledToolIDs", "enabled_tool_ids"),
		fieldRename[serverapi.WorkflowValidationError]("kent.api.workflow_definition.WorkflowValidationError", "RelatedIDs", "related_ids"),
		fieldRename[serverapi.WorkflowProjectLabelReorderRequest]("kent.api.workflow_definition.ProjectLabelReorderRequest", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskCreateRequest]("kent.api.workflow_task.CreateRequest", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskStatus]("kent.api.workflow_task.TaskStatus", "NodeIDs", "node_ids"),
		fieldRename[serverapi.WorkflowTaskDetail]("kent.api.workflow_task.TaskDetail", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskAssignedLabelIDs]("kent.api.workflow_task.AssignedLabelIds", "LabelIDs", "label_ids"),
		fieldRename[serverapi.WorkflowTaskLabelsUpdateRequest]("kent.api.workflow_task.LabelsUpdateRequest", "AddLabelIDs", "add_label_ids"),
		fieldRename[serverapi.WorkflowTaskLabelsUpdateRequest]("kent.api.workflow_task.LabelsUpdateRequest", "RemoveLabelIDs", "remove_label_ids"),
		fieldRename[serverapi.TaskSearchRequest]("kent.api.workflow_task.SearchRequest", "ProjectIDs", "project_ids"),
	}
}

func actualTargetScalarMappings() []WireScalarMapping {
	return []WireScalarMapping{
		scalarMapping("kent.api.shared.StreamCompletion", "code", protoreflect.Int32Kind),
		scalarMapping("kent.api.auth.SubscriptionWindowFacts", "duration_seconds", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ImportChoiceFact", "item_count", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ImportModeRecommendationFact", "item_count", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ModelFact", "context_window_tokens", protoreflect.Uint32Kind),
		scalarMapping("kent.api.capability.ModelLargeWindowFact", "tokens", protoreflect.Uint32Kind),
		scalarMapping("kent.api.onboarding.ContextWindowChoice", "tokens", protoreflect.Uint32Kind),
		scalarMapping("kent.api.onboarding.FinalizeRequest", "model_timeout_seconds", protoreflect.Uint32Kind),
		scalarMapping("kent.api.process.InlineOutputRequest", "max_chars", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectDeleteBlocker", "count", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectHomeListRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectSummary", "session_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectWorkspacePageRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.ProjectWorkspaceSummary", "session_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.SessionPageRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.SessionPageRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.SessionPageSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.project.WorkspaceUnlinkBlocker", "count", protoreflect.Int32Kind),
		scalarMapping("kent.api.prompt.QuestionAnswer", "selected_option_number", protoreflect.Int32Kind),
		scalarMapping("kent.api.connection.ServerIdentity", "pid", protoreflect.Int32Kind),
		scalarMapping("kent.api.session_launch.RunPromptOverrides", "model_timeout_seconds", protoreflect.Int32Kind),
		scalarMapping("kent.api.runtime.Status", "compaction_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.ListRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.ListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.ListSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.UnlinkProjectBlocker", "count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_definition.WorkflowNodeGroup", "sort_order", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.AttentionListRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.BoardNodeCardsListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.BoardNodeCardsListRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.BoardProject", "attached_workspace_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyAvailable", "remaining_capacity", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyDirectionProjection", "total_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyDirectionProjection", "unsatisfied_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyListDirection", "total_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.DependencyListDirection", "unsatisfied_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.ListRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.ListRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchGroup", "total_hit_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchHit", "ordinal", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchRequest", "context", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchRequest", "page_size", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.SearchSuccess", "next_offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDependencies", "blocker_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDependencies", "directly_blocked_task_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDependencies", "unsatisfied_blocker_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDetail", "attention_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskDetail", "retained_session_count", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskOffsetPageRequest", "limit", protoreflect.Int32Kind),
		scalarMapping("kent.api.workflow_task.TaskOffsetPageRequest", "offset", protoreflect.Int32Kind),
		scalarMapping("kent.api.worktree.DirtyState", "dirty_file_count", protoreflect.Int32Kind),
	}
}

func actualTargetPresenceMappings() []WirePresenceMapping {
	var result []WirePresenceMapping
	result = append(result, presenceMappings[protocol.StreamCompleteParams]("kent.api.shared.StreamCompletion", "code", "message", "transcript_close_reason")...)
	result = append(result, presenceMappings[protocol.ServerIdentity]("kent.api.connection.ServerIdentity", "persistence_root_id")...)
	result = append(result, presenceMappings[serverapi.AuthBootstrapOAuthConfig]("kent.api.auth.BootstrapOAuthConfig", "client_id", "issuer")...)
	result = append(result, presenceMappings[serverapi.AuthCompleteBootstrapRequest]("kent.api.auth.CompleteBootstrapRequest", "api_key", "callback_input", "redirect_uri", "oauth_state", "oauth_code_verifier", "device_authorization_code", "device_code_verifier")...)
	result = append(result, presenceMappings[serverapi.AuthCompleteBootstrapResponse]("kent.api.auth.BootstrapCompletion", "method_type", "account_id", "email")...)
	result = append(result, presenceMappingsAs[serverapi.AuthProviderSelection](false, "kent.api.auth.ProviderSelection", "provider_capabilities")...)
	result = append(result, presenceMappingsAs[serverapi.AuthStatusRequest](false, "kent.api.auth.GetStatusRequest", "provider")...)
	result = append(result, presenceMappingsAs[serverapi.AuthSubscriptionFacts](false, "kent.api.auth.SubscriptionFacts", "failure")...)
	result = append(result, presenceMappingsAs[serverapi.AuthSubscriptionWindowFacts](false, "kent.api.auth.SubscriptionWindowFacts", "reset_at")...)
	result = append(result, presenceMappingsAs[serverapi.CapabilityDefaultFacts](false, "kent.api.capability.DefaultFacts", "verbosity")...)
	result = append(result, presenceMappingsAs[serverapi.ImportRecommendationFacts](false, "kent.api.capability.ImportRecommendationFacts", "commands", "skills")...)
	result = append(result, presenceMappingsAs[serverapi.ModelCapabilityFact](false, "kent.api.capability.ModelFact", "large_window")...)
	result = append(result, presenceMappingsAs[serverapi.ProviderCapabilityFacts](false, "kent.api.capability.ProviderFacts", "current_effective")...)
	result = append(result, presenceMappings[serverapi.OnboardingContextWindowChoice]("kent.api.onboarding.ContextWindowChoice", "tokens")...)
	result = append(result, presenceMappingsAs[serverapi.OnboardingFinalizeRequest](false, "kent.api.onboarding.FinalizeRequest", "commands_import", "context_window", "main_provider", "model", "skills_import", "supervisor", "thinking")...)
	result = append(result, presenceMappings[serverapi.OnboardingModelChoice]("kent.api.onboarding.ModelChoice", "alias", "model_id")...)
	result = append(result, presenceMappingsAs[serverapi.OnboardingSupervisorChoice](false, "kent.api.onboarding.SupervisorChoice", "model", "thinking")...)
	result = append(result, presenceMappings[serverapi.OnboardingThinkingChoice]("kent.api.onboarding.ThinkingChoice", "level", "value")...)
	result = append(result, presenceMappings[serverapi.ProjectCreateRequest]("kent.api.project.CreateProjectRequest", "project_key")...)
	result = append(result, presenceMappings[serverapi.ProjectEditGetRequest]("kent.api.project.ProjectWorkspacePageRequest", "page_size", "page_token")...)
	result = append(result, presenceMappings[serverapi.ProjectEditGetResponse]("kent.api.project.GetProjectEditSuccess", "next_page_token")...)
	result = append(result, presenceMappings[serverapi.ProjectHomeListRequest]("kent.api.project.ProjectHomeListRequest", "page_size", "page_token")...)
	result = append(result, presenceMappings[serverapi.ProjectUpdateRequest]("kent.api.project.UpdateProjectRequest", "project_key")...)
	result = append(result, presenceMappings[serverapi.ProjectWorkspaceListRequest]("kent.api.project.ProjectWorkspacePageRequest", "page_size", "page_token")...)
	result = append(result, presenceMappings[serverapi.ProjectWorkspaceListResponse]("kent.api.project.ListProjectWorkspacesSuccess", "next_page_token")...)
	result = append(result, presenceMappings[serverapi.ProjectWorkspaceUnlinkBlocker]("kent.api.project.WorkspaceUnlinkBlocker", "count")...)
	result = append(result, presenceMappings[serverapi.RuntimeAppendCommittedEntryRequest]("kent.api.transcript.AppendCommittedEntryRequest", "notice_id", "visibility")...)
	result = append(result, presenceMappings[serverapi.RuntimeGoalSetRequest]("kent.api.runtime.GoalSetRequest", "run_id", "step_id")...)
	result = append(result, presenceMappings[serverapi.RuntimeGoalStatusRequest]("kent.api.runtime.GoalMutationRequest", "run_id", "step_id")...)
	result = append(result, presenceMappings[clientui.SessionSummary]("kent.api.project.SessionSummary", "first_prompt_preview", "name")...)
	result = append(result, presenceMappings[serverapi.SessionInitialInputRequest]("kent.api.session_launch.SessionInitialInputRequest", "session_id")...)
	result = append(result, presenceMappings[serverapi.SessionPlan]("kent.api.session_launch.SessionPlan", "configured_model_name")...)
	result = append(result, presenceMappings[serverapi.SessionResolveTransitionRequest]("kent.api.session_launch.SessionResolveTransitionRequest", "session_id")...)
	result = append(result, presenceMappings[serverapi.SessionRuntimeReleaseRequest]("kent.api.session_launch.SessionRuntimeReleaseRequest", "close_policy")...)
	result = append(result, presenceMappings[serverapi.WorkflowAttentionListRequest]("kent.api.workflow_task.AttentionListRequest", "page_token")...)
	result = append(result, presenceMappings[serverapi.WorkflowContextSource]("kent.api.workflow_definition.ContextSource", "node_key")...)
	result = append(result, presenceMappings[serverapi.WorkflowCreateAndLinkProjectRequest]("kent.api.workflow_definition.CreateAndLinkProjectRequest", "default_policy")...)
	result = append(result, presenceMappings[serverapi.WorkflowGraphDraftNode]("kent.api.workflow_definition.GraphDraftNode", "completion_mode")...)
	result = append(result, presenceMappings[serverapi.WorkflowLinkProjectRequest]("kent.api.workflow_definition.LinkProjectRequest", "default_policy")...)
	result = append(result, presenceMappings[serverapi.WorkflowNode]("kent.api.workflow_definition.WorkflowNode", "completion_mode")...)
	result = append(result, presenceMappings[serverapi.WorkflowProjectSubscribeRequest]("kent.api.workflow_definition.ProjectSubscribeRequest", "project_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskCommentAddRequest]("kent.api.workflow_task.CommentAddRequest", "author_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskCreateRequest]("kent.api.workflow_task.CreateRequest", "body", "source_url", "source_workspace_id")...)
	result = append(result, presenceMappingsAs[serverapi.WorkflowTaskDependencyDirectionProjection](false, "kent.api.workflow_task.DependencyDirectionProjection", "add_availability")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskDetail]("kent.api.workflow_task.TaskDetail", "source_url")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskGetRequest]("kent.api.workflow_task.GetRequest", "project_id", "short_id", "task_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskInterruptRequest]("kent.api.workflow_task.InterruptRequest", "reason", "session_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowTaskUpdateRequest]("kent.api.workflow_task.UpdateRequest", "source_workspace_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowUnlinkProjectRequest]("kent.api.workflow_definition.UnlinkProjectRequest", "replacement_default_link_id")...)
	result = append(result, presenceMappings[serverapi.WorkflowValidateRequest]("kent.api.workflow_definition.ValidateRequest", "mode")...)
	result = append(result, presenceMappings[serverapi.WorktreeCreateRequest]("kent.api.worktree.CreateRequest", "base_ref", "branch_name", "root_path")...)
	result = append(result, presenceMappings[serverapi.WorktreeCreateTargetResolution]("kent.api.worktree.CreateTargetResolution", "resolved_ref")...)
	return result
}

func wireException[T any](
	message protoreflect.FullName,
	_ ...any,
) WireException {
	return WireException{
		LegacyType: reflect.TypeFor[T](),
		Message:    message,
	}
}

func fieldRename[T any](
	message protoreflect.FullName,
	legacyField string,
	descriptorField protoreflect.Name,
) WireFieldRename {
	return WireFieldRename{
		LegacyType:      reflect.TypeFor[T](),
		Message:         message,
		LegacyField:     legacyField,
		DescriptorField: descriptorField,
	}
}

func scalarMapping(
	message protoreflect.FullName,
	field protoreflect.Name,
	kind protoreflect.Kind,
) WireScalarMapping {
	return WireScalarMapping{Message: message, Field: field, Kind: kind}
}

func presenceMappings[T any](
	message protoreflect.FullName,
	fields ...protoreflect.Name,
) []WirePresenceMapping {
	return presenceMappingsAs[T](true, message, fields...)
}

func presenceMappingsAs[T any](
	optional bool,
	message protoreflect.FullName,
	fields ...protoreflect.Name,
) []WirePresenceMapping {
	result := make([]WirePresenceMapping, 0, len(fields))
	for _, field := range fields {
		result = append(result, WirePresenceMapping{
			LegacyType: reflect.TypeFor[T](),
			Message:    message,
			Field:      field,
			Optional:   optional,
		})
	}
	return result
}
