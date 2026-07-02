export type AttentionNotificationKind = "question" | "approval" | "interrupted_run";

export type AttentionNotificationSource = "live" | "snapshot";

export type AttentionNotificationID = Readonly<{
  kind: AttentionNotificationKind;
  uuid: string;
}>;

export type AttentionNotificationQuestionFocus = Readonly<{
  kind: "question";
  askIDs: readonly string[];
}>;

export type AttentionNotificationApprovalFocus = Readonly<{
  kind: "approval";
  taskTransitionID: string;
}>;

export type AttentionNotificationInterruptedRunFocus = Readonly<{
  kind: "interrupted_run";
  runID: string;
}>;

export type AttentionNotificationTaskDetailFocus =
  | AttentionNotificationQuestionFocus
  | AttentionNotificationApprovalFocus
  | AttentionNotificationInterruptedRunFocus;

export type AttentionNotificationWorkflowTaskTarget = Readonly<{
  kind: "workflow_task";
  projectID?: string | undefined;
  workflowID?: string | undefined;
  taskID: string;
  taskShortID?: string | undefined;
  taskTitle?: string | undefined;
  sessionID?: string | undefined;
  runID?: string | undefined;
  focus: AttentionNotificationTaskDetailFocus;
}>;

export type AttentionNotificationSessionPromptTarget = Readonly<{
  kind: "session_prompt";
  sessionID: string;
}>;

export type AttentionNotificationTarget =
  | AttentionNotificationWorkflowTaskTarget
  | AttentionNotificationSessionPromptTarget;

export type AttentionNotificationQuestionState = Readonly<{
  preparedAskIDs: readonly string[];
  materializedAskIDs: readonly string[];
  currentUnresolvedAskIDs: readonly string[];
  skippedAskIDs: readonly string[];
  preview?: string | undefined;
  displayCount: number;
  materializedCount: number;
}>;

export type AttentionNotificationApprovalState = Readonly<{
  taskTransitionID?: string | undefined;
  message?: string | undefined;
}>;

export type AttentionNotificationInterruptedRunState = Readonly<{
  runID: string;
  message?: string | undefined;
  reason?: string | undefined;
  detailJSON?: string | undefined;
}>;

export type AttentionNotification = Readonly<{
  id: AttentionNotificationID;
  kind: AttentionNotificationKind;
  occurredAt: string;
  revision: number;
  question: AttentionNotificationQuestionState | null;
  approval: AttentionNotificationApprovalState | null;
  interruptedRun: AttentionNotificationInterruptedRunState | null;
  target: AttentionNotificationTarget;
}>;

export type AttentionNotificationPendingEvent = Readonly<{
  type: "pending";
  sequence: number;
  source: AttentionNotificationSource;
  pending: AttentionNotification;
}>;

export type AttentionNotificationResolvedEvent = Readonly<{
  type: "resolved";
  sequence: number;
  source: AttentionNotificationSource;
  id: AttentionNotificationID;
  kind: AttentionNotificationKind;
  occurredAt: string;
}>;

export type AttentionNotificationSnapshotCompleteEvent = Readonly<{
  type: "snapshot_complete";
  sequence: number;
  source: "snapshot";
  sessionID: string;
}>;

export type AttentionNotificationEvent =
  | AttentionNotificationPendingEvent
  | AttentionNotificationResolvedEvent
  | AttentionNotificationSnapshotCompleteEvent;

export type AttentionNotificationEventParams = Readonly<{
  event: AttentionNotificationEvent;
}>;

export type AttentionNotificationEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: AttentionNotificationEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;
