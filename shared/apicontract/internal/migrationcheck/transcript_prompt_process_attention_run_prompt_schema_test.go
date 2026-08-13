package migrationcheck

import (
	"testing"
	"time"

	"core/shared/protoapi"
	attentionpb "core/shared/protoapi/gen/kent/api/attention"
	processpb "core/shared/protoapi/gen/kent/api/process"
	promptpb "core/shared/protoapi/gen/kent/api/prompt"
	runpromptpb "core/shared/protoapi/gen/kent/api/run_prompt"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	transcriptpb "core/shared/protoapi/gen/kent/api/transcript"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTranscriptPromptProcessAttentionAndRunPromptRoutesAreDescriptorOwned(t *testing.T) {
	for packageName, routes := range map[string][]string{
		"kent.api.transcript": {
			protocol.MethodSessionGetTranscriptPage,
			protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer,
			protocol.MethodSessionSubscribeTranscript,
			protocol.MethodSessionTranscriptEvent,
			protocol.MethodSessionTranscriptComplete,
			protocol.MethodRuntimeAppendCommittedEntry,
		},
		"kent.api.prompt": {
			protocol.MethodAskListPending,
			protocol.MethodApprovalListPending,
			protocol.MethodPromptAnswerBatch,
			protocol.MethodPromptFollowUpWatch,
			protocol.MethodPromptFollowUpEvent,
			protocol.MethodPromptFollowUpComplete,
			protocol.MethodRuntimeRecordPromptHistory,
			protocol.MethodRuntimeLiveWatch,
		},
		"kent.api.process": {
			protocol.MethodProcessList,
			protocol.MethodProcessGet,
			protocol.MethodProcessKill,
			protocol.MethodProcessInlineOutput,
		},
		"kent.api.attention": {
			protocol.MethodAttentionSessionNotificationSubscribe,
			protocol.MethodAttentionSessionNotificationEvent,
			protocol.MethodAttentionSessionNotificationComplete,
		},
		"kent.api.run_prompt": {
			protocol.MethodRunPrompt,
			protocol.MethodRunPromptProgress,
		},
	} {
		assertSchemaRoutes(t, protoreflect.FullName(packageName), routes)
	}
}

func TestNewSliceGeneratedValidationBoundaries(t *testing.T) {
	validUUID := "123e4567-e89b-42d3-a456-426614174000"
	validQuestion := &promptpb.Question{
		PromptId:               "prompt-1",
		SessionId:              "session-1",
		StepId:                 validUUID,
		Question:               "Choose",
		Suggestions:            []string{"first", "second"},
		RecommendedOptionIndex: int32Pointer(2),
		CreatedAt:              timestamppb.New(time.Unix(1_700_000_000, 0)),
	}
	if err := protoapi.ValidateGeneratedMessage(validQuestion); err != nil {
		t.Fatalf("valid question: %v", err)
	}
	validQuestion.RecommendedOptionIndex = int32Pointer(3)
	if err := protoapi.ValidateGeneratedMessage(validQuestion); err == nil {
		t.Fatal("out-of-range question recommendation was accepted")
	}

	queued := &transcriptpb.QueuedMessageState{
		QueueItemId: validUUID,
		Status:      transcriptpb.QueuedMessageStatus_QUEUED_MESSAGE_STATUS_ACCEPTED,
		Text:        stringPointer("queued"),
	}
	if err := protoapi.ValidateGeneratedMessage(queued); err != nil {
		t.Fatalf("valid queued message: %v", err)
	}
	queued.Text = nil
	if err := protoapi.ValidateGeneratedMessage(queued); err == nil {
		t.Fatal("accepted queued message without restore text")
	}

	message := &transcriptpb.Message{
		Sequence: 1,
		Payload: &transcriptpb.Message_RuntimeReadModelUpdate{
			RuntimeReadModelUpdate: &transcriptpb.RuntimeReadModelUpdate{},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(message); err == nil {
		t.Fatal("accepted live transcript event at hydration sequence")
	}

	attention := &attentionpb.Notification{
		Id: &attentionpb.NotificationID{
			Kind: attentionpb.Kind_ATTENTION_KIND_QUESTION,
			Uuid: validUUID,
		},
		OccurredAt: timestamppb.New(time.Unix(1_700_000_000, 0)),
		Source:     attentionpb.Source_ATTENTION_SOURCE_LIVE,
		Revision:   1,
		SessionId:  "session-1",
		State: &attentionpb.Notification_Approval{
			Approval: &attentionpb.ApprovalState{},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(attention); err == nil {
		t.Fatal("accepted attention id kind that mismatches its state")
	}

	runProgress := &runpromptpb.ProgressEvent{
		Payload: &runpromptpb.ProgressEvent_SessionStarted{
			SessionStarted: &runpromptpb.SessionStarted{SessionId: validUUID},
		},
	}
	if err := protoapi.ValidateGeneratedMessage(runProgress); err != nil {
		t.Fatalf("valid run prompt session start: %v", err)
	}
	runProgress.GetSessionStarted().SessionId = "not-a-uuid"
	if err := protoapi.ValidateGeneratedMessage(runProgress); err == nil {
		t.Fatal("accepted non-UUIDv4 run prompt session id")
	}

	notice := &transcriptpb.NoticeRow{
		Reason:   transcriptpb.NoticeReason_NOTICE_REASON_CACHE_WARNING,
		Severity: transcriptpb.NoticeSeverity_NOTICE_SEVERITY_WARNING,
		Diagnostic: &transcriptpb.Diagnostic{
			Code:   "runtime",
			Detail: "wrong payload",
		},
	}
	if err := protoapi.ValidateGeneratedMessage(notice); err == nil {
		t.Fatal("accepted notice reason with mismatched payload")
	}

	background := &transcriptpb.BackgroundActivity{
		ActivityId:        validUUID,
		ProcessId:         "process-1",
		OwnerRunId:        validUUID,
		OwnerStepId:       validUUID,
		Lifecycle:         transcriptpb.BackgroundLifecycle_BACKGROUND_LIFECYCLE_BACKGROUNDED,
		Command:           "go test",
		Workdir:           "/repo",
		UserRequestedKill: true,
	}
	if err := protoapi.ValidateGeneratedMessage(background); err == nil {
		t.Fatal("accepted backgrounded activity with terminal kill fact")
	}

	liveRun := &transcriptpb.LiveRunFinished{
		Status:     transcriptpb.LiveRunStatus_LIVE_RUN_STATUS_COMPLETED,
		ResultKind: transcriptpb.LiveRunResultKind_LIVE_RUN_RESULT_KIND_NO_FINAL_ANSWER,
		Failure:    stringPointer("failed"),
		StartedAt:  timestamppb.New(time.Unix(1_700_000_000, 0)),
		FinishedAt: timestamppb.New(time.Unix(1_700_000_001, 0)),
	}
	if err := protoapi.ValidateGeneratedMessage(liveRun); err == nil {
		t.Fatal("accepted completed live run with failure")
	}

	for _, diagnostic := range []*transcriptpb.OperationalDiagnostic{
		{
			Code:   transcriptpb.OperationalDiagnosticCode_OPERATIONAL_DIAGNOSTIC_CODE_SLEEP_GUARD_FAILED,
			Detail: "sleep prevention failed",
		},
		{
			Code: transcriptpb.OperationalDiagnosticCode_OPERATIONAL_DIAGNOSTIC_CODE_PROVIDER_TURN_STATE_INVALID,
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(diagnostic); err != nil {
			t.Errorf("valid operational diagnostic rejected: %v", err)
		}
	}
	for _, diagnostic := range []*transcriptpb.OperationalDiagnostic{
		{
			Code: transcriptpb.OperationalDiagnosticCode_OPERATIONAL_DIAGNOSTIC_CODE_SLEEP_GUARD_FAILED,
		},
		{
			Code:   transcriptpb.OperationalDiagnosticCode_OPERATIONAL_DIAGNOSTIC_CODE_PROVIDER_TURN_STATE_INVALID,
			Detail: "unexpected detail",
		},
	} {
		if err := protoapi.ValidateGeneratedMessage(diagnostic); err == nil {
			t.Errorf("invalid operational diagnostic accepted: %+v", diagnostic)
		}
	}
}

func TestWorkflowOwnedAttentionRoutesAndVariantsAreOwnedByWorkflowTaskSlice(t *testing.T) {
	for _, legacyName := range []string{
		protocol.MethodAttentionNotificationSubscribe,
		protocol.MethodAttentionNotificationEvent,
		protocol.MethodAttentionNotificationComplete,
	} {
		if operation := operationByLegacyName(t, legacyName); operation == nil {
			t.Errorf("workflow-owned attention route %s is missing from the Workflow Task slice", legacyName)
		}
	}
	assertExactEnumValues(t, attentionpb.Kind_ATTENTION_KIND_UNSPECIFIED.Descriptor(),
		"ATTENTION_KIND_UNSPECIFIED", "ATTENTION_KIND_QUESTION", "ATTENTION_KIND_APPROVAL")
}

func TestNewSliceRetainsDomainIdentityAndTypedUnions(t *testing.T) {
	assertMessageOneofFields(t, (&transcriptpb.Message{}).ProtoReflect().Descriptor(), "payload",
		"hydration", "committed_row", "assistant_delta", "assistant_stream_abort",
		"thinking_status_update", "reasoning_trace_update", "reasoning_trace_reset",
		"tool_start", "tool_abort", "user_message_flushed", "queued_message_state",
		"step_state", "reviewer_state", "runtime_read_model_update", "session_status",
		"session_identity", "compaction_status", "context_usage", "goal_status",
		"background_activity", "prompt", "worktree_transition_outcome",
		"operational_diagnostic", "live_run_finished")
	assertMessageOneofFields(t, (&promptpb.AnswerBatchEntry{}).ProtoReflect().Descriptor(), "answer",
		"question_answer", "approval_answer", "declined")
	assertMessageOneofFields(t, (&promptpb.LiveWatchOutcome{}).ProtoReflect().Descriptor(), "outcome",
		"question", "final_answer", "execution_error", "no_final_result", "interrupted")
	assertMessageOneofFields(t, (&attentionpb.NotificationEvent{}).ProtoReflect().Descriptor(), "payload",
		"pending", "resolved", "snapshot_complete")
	assertMessageOneofFields(t, (&runpromptpb.ProgressEvent{}).ProtoReflect().Descriptor(), "payload",
		"session_started", "assistant_message", "steered_message", "compaction_started",
		"compaction_failed", "run_logging_failed", "run_cleanup_failed")
}

func TestKENT345ProjectedRequestIdentityDoesNotReturnInNewSlice(t *testing.T) {
	for name, message := range map[string]protoreflect.MessageDescriptor{
		"process kill":              (&processpb.KillRequest{}).ProtoReflect().Descriptor(),
		"run prompt":                (&runpromptpb.Request{}).ProtoReflect().Descriptor(),
		"append committed entry":    (&transcriptpb.AppendCommittedEntryRequest{}).ProtoReflect().Descriptor(),
		"record prompt history":     (&promptpb.RecordHistoryRequest{}).ProtoReflect().Descriptor(),
		"queued transcript message": (&transcriptpb.QueuedMessageState{}).ProtoReflect().Descriptor(),
	} {
		if field := message.Fields().ByName("client_request_id"); field != nil {
			t.Errorf("%s restored projected client_request_id", name)
		}
	}
}

func TestNewSliceContainsNoForbiddenDynamicOrExternallyAuthoredPayloadFields(t *testing.T) {
	for _, packageName := range []protoreflect.FullName{
		"kent.api.transcript",
		"kent.api.prompt",
		"kent.api.process",
		"kent.api.attention",
		"kent.api.run_prompt",
	} {
		for _, operation := range mustOperationsInPackage(t, packageName) {
			for _, message := range []protoreflect.MessageDescriptor{
				operation.Descriptor.Input(),
				operation.Descriptor.Output(),
			} {
				walkMessageFields(message, func(field protoreflect.FieldDescriptor) {
					if field.IsMap() {
						t.Errorf("%s contains forbidden map field %s", field.ContainingMessage().FullName(), field.Name())
					}
					if field.Kind() == protoreflect.BytesKind {
						t.Errorf("%s contains forbidden opaque bytes field %s", field.ContainingMessage().FullName(), field.Name())
					}
					switch field.Name() {
					case "provider_payload", "raw_json", "detail_json", "presentation",
						"compact_text", "result_summary", "result":
						t.Errorf("%s contains forbidden externally-authored or dynamic field %s", field.ContainingMessage().FullName(), field.Name())
					}
				})
			}
		}
	}
}

func TestNewSliceReviewedValidatorSignoffsRemainMessageLocal(t *testing.T) {
	want := map[Identity]struct{}{
		typeMethodIdentity("core/shared/serverapi", "ApprovalListPendingBySessionRequest", "Validate"):               {},
		typeMethodIdentity("core/shared/serverapi", "AskListPendingBySessionRequest", "Validate"):                    {},
		typeMethodIdentity("core/shared/serverapi", "AttentionSessionNotificationSubscribeRequest", "Validate"):      {},
		typeMethodIdentity("core/shared/serverapi", "ObservationQuestion", "Validate"):                               {},
		typeMethodIdentity("core/shared/serverapi", "ProcessGetRequest", "Validate"):                                 {},
		typeMethodIdentity("core/shared/serverapi", "ProcessInlineOutputRequest", "Validate"):                        {},
		typeMethodIdentity("core/shared/serverapi", "ProcessKillRequest", "Validate"):                                {},
		typeMethodIdentity("core/shared/serverapi", "PromptAnswerBatchEntry", "Validate"):                            {},
		typeMethodIdentity("core/shared/serverapi", "PromptAnswerBatchRequest", "Validate"):                          {},
		typeMethodIdentity("core/shared/serverapi", "PromptAnswerBatchResponse", "Validate"):                         {},
		typeMethodIdentity("core/shared/serverapi", "PromptApprovalAnswer", "Validate"):                              {},
		typeMethodIdentity("core/shared/serverapi", "PromptFollowUpWatchRequest", "Validate"):                        {},
		typeMethodIdentity("core/shared/serverapi", "PromptQuestionAnswer", "Validate"):                              {},
		typeMethodIdentity("core/shared/serverapi", "RunPromptProgress", "Validate"):                                 {},
		typeMethodIdentity("core/shared/serverapi", "RunPromptRequest", "Validate"):                                  {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeAppendCommittedEntryRequest", "Validate"):                {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveWatchFailure", "Validate"):                           {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveWatchRequest", "Validate"):                           {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeLiveWatchResponse", "Validate"):                          {},
		typeMethodIdentity("core/shared/serverapi", "RuntimeRecordPromptHistoryRequest", "Validate"):                 {},
		typeMethodIdentity("core/shared/serverapi", "SessionLatestCommittedAssistantFinalAnswerRequest", "Validate"): {},
		typeMethodIdentity("core/shared/serverapi", "SessionTranscriptPageRequest", "Validate"):                      {},
		typeMethodIdentity("core/shared/serverapi", "SessionTranscriptPageResponse", "Validate"):                     {},
		typeMethodIdentity("core/shared/serverapi", "TranscriptSubscribeRequest", "Validate"):                        {},
	}
	for _, signoff := range ExecutionTargetDomainSignoffs() {
		if signoff.Domain != "interactive_session" {
			continue
		}
		for _, validator := range signoff.Classification.Validators {
			if _, expected := want[validator.Identity]; !expected {
				continue
			}
			if validator.Kind != ValidatorMessageLocal || validator.Owner != nil {
				t.Errorf("%s owner = %+v kind=%s", validator.Identity, validator.Owner, validator.Kind)
			}
			delete(want, validator.Identity)
		}
	}
	for identity := range want {
		t.Errorf("missing message-local schema-slice validator sign-off %s", identity)
	}
}

func TestNewSliceClosedScalarCoverageIsExact(t *testing.T) {
	for descriptor, values := range map[protoreflect.EnumDescriptor][]protoreflect.Name{
		promptpb.ApprovalDecision_APPROVAL_DECISION_UNSPECIFIED.Descriptor(): {
			"APPROVAL_DECISION_UNSPECIFIED", "APPROVAL_DECISION_ALLOW_ONCE",
			"APPROVAL_DECISION_ALLOW_SESSION", "APPROVAL_DECISION_DENY",
		},
		promptpb.AnswerBatchOutcome_ANSWER_BATCH_OUTCOME_UNSPECIFIED.Descriptor(): {
			"ANSWER_BATCH_OUTCOME_UNSPECIFIED", "ANSWER_BATCH_OUTCOME_RESOLVED", "ANSWER_BATCH_OUTCOME_SKIPPED",
		},
		promptpb.FollowUpKind_FOLLOW_UP_KIND_UNSPECIFIED.Descriptor(): {
			"FOLLOW_UP_KIND_UNSPECIFIED", "FOLLOW_UP_KIND_SUCCESSOR_READY",
			"FOLLOW_UP_KIND_NO_PREPARED_SUCCESSOR", "FOLLOW_UP_KIND_EXECUTION_CLOSED",
		},
		transcriptpb.QueuedMessageStatus_QUEUED_MESSAGE_STATUS_UNSPECIFIED.Descriptor(): {
			"QUEUED_MESSAGE_STATUS_UNSPECIFIED", "QUEUED_MESSAGE_STATUS_ACCEPTED",
			"QUEUED_MESSAGE_STATUS_SUBMITTED", "QUEUED_MESSAGE_STATUS_FAILED", "QUEUED_MESSAGE_STATUS_DISCARDED",
		},
		transcriptpb.QueuedMessageFailureReason_QUEUED_MESSAGE_FAILURE_REASON_UNSPECIFIED.Descriptor(): {
			"QUEUED_MESSAGE_FAILURE_REASON_UNSPECIFIED", "QUEUED_MESSAGE_FAILURE_REASON_CLOSING",
			"QUEUED_MESSAGE_FAILURE_REASON_TERMINAL_WORKFLOW_COMPLETION",
			"QUEUED_MESSAGE_FAILURE_REASON_RUNTIME_UNAVAILABLE", "QUEUED_MESSAGE_FAILURE_REASON_STOPPED",
		},
		transcriptpb.EntryVisibility_ENTRY_VISIBILITY_UNSPECIFIED.Descriptor(): {
			"ENTRY_VISIBILITY_UNSPECIFIED", "ENTRY_VISIBILITY_ONGOING", "ENTRY_VISIBILITY_ONGOING_COLLAPSED",
			"ENTRY_VISIBILITY_DETAIL", "ENTRY_VISIBILITY_HIDDEN",
		},
		transcriptpb.AppendVisibility_APPEND_VISIBILITY_UNSPECIFIED.Descriptor(): {
			"APPEND_VISIBILITY_UNSPECIFIED", "APPEND_VISIBILITY_AUTO", "APPEND_VISIBILITY_ONGOING",
			"APPEND_VISIBILITY_ONGOING_COLLAPSED", "APPEND_VISIBILITY_DETAIL", "APPEND_VISIBILITY_HIDDEN",
		},
		transcriptpb.RowIntegrity_ROW_INTEGRITY_UNSPECIFIED.Descriptor(): {
			"ROW_INTEGRITY_UNSPECIFIED", "ROW_INTEGRITY_VALID",
			"ROW_INTEGRITY_RECOVERABLE_MALFORMED", "ROW_INTEGRITY_UNRECOVERABLE_MALFORMED",
		},
		transcriptpb.AssistantPhase_ASSISTANT_PHASE_UNSPECIFIED.Descriptor(): {
			"ASSISTANT_PHASE_UNSPECIFIED", "ASSISTANT_PHASE_COMMENTARY", "ASSISTANT_PHASE_FINAL",
		},
		transcriptpb.AssistantAbortReason_ASSISTANT_ABORT_REASON_UNSPECIFIED.Descriptor(): {
			"ASSISTANT_ABORT_REASON_UNSPECIFIED", "ASSISTANT_ABORT_REASON_INTERRUPTED",
			"ASSISTANT_ABORT_REASON_FAILED", "ASSISTANT_ABORT_REASON_SUPERSEDED",
		},
		transcriptpb.ToolAbortReason_TOOL_ABORT_REASON_UNSPECIFIED.Descriptor(): {
			"TOOL_ABORT_REASON_UNSPECIFIED", "TOOL_ABORT_REASON_CANCELED", "TOOL_ABORT_REASON_FAILED",
		},
		transcriptpb.StepLifecycle_STEP_LIFECYCLE_UNSPECIFIED.Descriptor(): {
			"STEP_LIFECYCLE_UNSPECIFIED", "STEP_LIFECYCLE_STARTED", "STEP_LIFECYCLE_FINISHED",
		},
		transcriptpb.RunStatus_RUN_STATUS_UNSPECIFIED.Descriptor(): {
			"RUN_STATUS_UNSPECIFIED", "RUN_STATUS_RUNNING", "RUN_STATUS_COMPLETED",
			"RUN_STATUS_INTERRUPTED", "RUN_STATUS_FAILED",
		},
		transcriptpb.ReviewerState_REVIEWER_STATE_UNSPECIFIED.Descriptor(): {
			"REVIEWER_STATE_UNSPECIFIED", "REVIEWER_STATE_RUNNING", "REVIEWER_STATE_COMPLETED",
		},
		transcriptpb.CompactionState_COMPACTION_STATE_UNSPECIFIED.Descriptor(): {
			"COMPACTION_STATE_UNSPECIFIED", "COMPACTION_STATE_STARTED",
			"COMPACTION_STATE_COMPLETED", "COMPACTION_STATE_FAILED",
		},
		transcriptpb.BackgroundLifecycle_BACKGROUND_LIFECYCLE_UNSPECIFIED.Descriptor(): {
			"BACKGROUND_LIFECYCLE_UNSPECIFIED", "BACKGROUND_LIFECYCLE_BACKGROUNDED",
			"BACKGROUND_LIFECYCLE_COMPLETED", "BACKGROUND_LIFECYCLE_KILLED",
		},
		transcriptpb.PromptStatus_PROMPT_STATUS_UNSPECIFIED.Descriptor(): {
			"PROMPT_STATUS_UNSPECIFIED", "PROMPT_STATUS_PENDING", "PROMPT_STATUS_RESOLVED",
		},
		transcriptpb.OperationalDiagnosticCode_OPERATIONAL_DIAGNOSTIC_CODE_UNSPECIFIED.Descriptor(): {
			"OPERATIONAL_DIAGNOSTIC_CODE_UNSPECIFIED", "OPERATIONAL_DIAGNOSTIC_CODE_SLEEP_GUARD_FAILED",
			"OPERATIONAL_DIAGNOSTIC_CODE_PROMPT_HISTORY_PERSIST_FAILED",
			"OPERATIONAL_DIAGNOSTIC_CODE_IN_FLIGHT_CLEAR_FAILED",
			"OPERATIONAL_DIAGNOSTIC_CODE_PROVIDER_TURN_STATE_INVALID",
		},
		transcriptpb.LiveRunStatus_LIVE_RUN_STATUS_UNSPECIFIED.Descriptor(): {
			"LIVE_RUN_STATUS_UNSPECIFIED", "LIVE_RUN_STATUS_COMPLETED",
			"LIVE_RUN_STATUS_INTERRUPTED", "LIVE_RUN_STATUS_FAILED",
		},
		transcriptpb.LiveRunResultKind_LIVE_RUN_RESULT_KIND_UNSPECIFIED.Descriptor(): {
			"LIVE_RUN_RESULT_KIND_UNSPECIFIED", "LIVE_RUN_RESULT_KIND_ASSISTANT_FINAL_ANSWER",
			"LIVE_RUN_RESULT_KIND_NO_FINAL_ANSWER",
		},
		transcriptpb.NoticeReason_NOTICE_REASON_UNSPECIFIED.Descriptor(): {
			"NOTICE_REASON_UNSPECIFIED", "NOTICE_REASON_CACHE_WARNING", "NOTICE_REASON_COMPACTION",
			"NOTICE_REASON_LEGACY_UNTYPED_NOTICE", "NOTICE_REASON_RUNTIME_DIAGNOSTIC",
			"NOTICE_REASON_TOOL_OUTPUT_REPAIR",
		},
		transcriptpb.NoticeSeverity_NOTICE_SEVERITY_UNSPECIFIED.Descriptor(): {
			"NOTICE_SEVERITY_UNSPECIFIED", "NOTICE_SEVERITY_INFO",
			"NOTICE_SEVERITY_WARNING", "NOTICE_SEVERITY_ERROR",
		},
		runpromptpb.MessagePhase_MESSAGE_PHASE_UNSPECIFIED.Descriptor(): {
			"MESSAGE_PHASE_UNSPECIFIED", "MESSAGE_PHASE_COMMENTARY", "MESSAGE_PHASE_FINAL",
		},
		attentionpb.Kind_ATTENTION_KIND_UNSPECIFIED.Descriptor(): {
			"ATTENTION_KIND_UNSPECIFIED", "ATTENTION_KIND_QUESTION", "ATTENTION_KIND_APPROVAL",
		},
		attentionpb.Source_ATTENTION_SOURCE_UNSPECIFIED.Descriptor(): {
			"ATTENTION_SOURCE_UNSPECIFIED", "ATTENTION_SOURCE_LIVE", "ATTENTION_SOURCE_SNAPSHOT",
		},
		worktreepb.TransitionKind_TRANSITION_KIND_UNSPECIFIED.Descriptor(): {
			"TRANSITION_KIND_UNSPECIFIED", "TRANSITION_KIND_ENTER",
			"TRANSITION_KIND_LEAVE", "TRANSITION_KIND_DELETE",
		},
		worktreepb.TransitionState_TRANSITION_STATE_UNSPECIFIED.Descriptor(): {
			"TRANSITION_STATE_UNSPECIFIED", "TRANSITION_STATE_COMPLETED", "TRANSITION_STATE_FAILED",
		},
	} {
		assertExactEnumValues(t, descriptor, values...)
	}
}

func TestNewSliceIntentionalReshapesAreExact(t *testing.T) {
	assertExactFields(t, (&transcriptpb.RuntimeReadModelUpdate{}).ProtoReflect().Descriptor(), "version", "activity")
	assertExactFields(t, (&transcriptpb.UserMessageFlushed{}).ProtoReflect().Descriptor(), "step_id", "queue_item_ids")
	assertExactFields(t, (&transcriptpb.QueuedMessageState{}).ProtoReflect().Descriptor(),
		"queue_item_id", "status", "failure_reason", "text")
	assertExactFields(t, (&transcriptpb.AppendCommittedEntryRequest{}).ProtoReflect().Descriptor(),
		"session_id", "role", "text", "visibility", "notice_id")
	assertExactFields(t, (&transcriptpb.WorktreeTransitionOutcome{}).ProtoReflect().Descriptor(),
		"operation_id", "transition", "state", "failure", "delete_precondition")
	assertExactFields(t, (&runpromptpb.Success{}).ProtoReflect().Descriptor(),
		"session_id", "session_name", "duration", "warnings")
	assertExactFields(t, (&runpromptpb.AssistantMessage{}).ProtoReflect().Descriptor(), "phase")
	assertExactFields(t, (&sharedpb.StreamCompletion{}).ProtoReflect().Descriptor(),
		"code", "message", "transcript_close_reason")
}

func mustOperationsInPackage(t *testing.T, packageName protoreflect.FullName) []protoapi.Operation {
	t.Helper()
	operations, err := protoapi.Operations()
	if err != nil {
		t.Fatal(err)
	}
	result := make([]protoapi.Operation, 0)
	for _, operation := range operations {
		if operation.Descriptor.ParentFile().Package() == packageName {
			result = append(result, operation)
		}
	}
	return result
}
