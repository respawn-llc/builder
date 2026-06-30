export type AttentionNotificationKind = "question" | "approval";

export type AttentionNotificationSource = "live" | "snapshot";

export type AttentionNotificationQuestionFocus = Readonly<{
  kind: "question";
  askIDs: readonly string[];
}>;

export type AttentionNotificationApprovalFocus = Readonly<{
  kind: "approval";
  taskTransitionID: string;
}>;

export type AttentionNotificationTaskDetailFocus =
  | AttentionNotificationQuestionFocus
  | AttentionNotificationApprovalFocus;

export type AttentionNotificationTaskDetailTarget = Readonly<{
  kind: "task_detail";
  projectID: string;
  workflowID: string;
  taskID: string;
  taskShortID: string;
  taskTitle: string;
  sessionID: string;
  runID: string;
  focus: AttentionNotificationTaskDetailFocus;
}>;

export type AttentionNotificationSessionPromptTarget = Readonly<{
  kind: "session_prompt";
  sessionID: string;
}>;

export type AttentionNotificationTarget =
  | AttentionNotificationTaskDetailTarget
  | AttentionNotificationSessionPromptTarget;

export type AttentionNotificationPresentation = Readonly<{
  title: string;
  body: string;
  preview: string;
  fallbackBody: string;
  count: number;
  summary: string;
}>;

export type AttentionNotificationQuestionState = Readonly<{
  preparedAskIDs: readonly string[];
  materializedAskIDs: readonly string[];
  currentUnresolvedAskIDs: readonly string[];
  skippedAskIDs: readonly string[];
  displayCount: number;
  materializedCount: number;
}>;

export type AttentionNotification = Readonly<{
  id: string;
  kind: AttentionNotificationKind;
  occurredAt: string;
  revision: number;
  question: AttentionNotificationQuestionState | null;
  target: AttentionNotificationTarget;
  presentation: AttentionNotificationPresentation;
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
  id: string;
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
