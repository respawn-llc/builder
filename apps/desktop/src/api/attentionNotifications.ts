export type AttentionNotificationKind =
  "question" | "approval" | "workflow_approval" | "interrupted_current_node";

export type AttentionNotificationSource = "live" | "snapshot";

export type AttentionNotificationID = Readonly<{
  kind: AttentionNotificationKind;
  uuid: string;
}>;

export type AttentionNotificationTaskDetailFocus =
  | Readonly<{ kind: "question"; askIDs: readonly string[] }>
  | Readonly<{ kind: "approval"; approvalID: string }>
  | Readonly<{ kind: "interrupted_current_node" }>;

export type AttentionNotificationWorkflowTaskTarget = Readonly<{
  kind: "workflow_task";
  projectID?: string | undefined;
  workflowID: string;
  taskID: string;
  taskShortID?: string | undefined;
  taskTitle?: string | undefined;
  sessionID?: string | undefined;
  currentNodeID?: string | undefined;
  currentNodeBranchKey?: string | undefined;
  focus: AttentionNotificationTaskDetailFocus;
}>;

export type AttentionNotificationTarget =
  AttentionNotificationWorkflowTaskTarget | Readonly<{ kind: "session_prompt"; sessionID: string }>;

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
  message: string;
}>;

export type AttentionNotificationWorkflowApprovalState = Readonly<{
  approvalID: string;
  message?: string | undefined;
}>;

export type AttentionNotificationInterruptedCurrentNodeState = Readonly<{
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
  workflowApproval: AttentionNotificationWorkflowApprovalState | null;
  interruptedCurrentNode: AttentionNotificationInterruptedCurrentNodeState | null;
  target: AttentionNotificationTarget;
}>;

export type AttentionNotificationEvent =
  | Readonly<{
      type: "pending";
      sequence: number;
      source: AttentionNotificationSource;
      pending: AttentionNotification;
    }>
  | Readonly<{
      type: "resolved";
      sequence: number;
      source: AttentionNotificationSource;
      id: AttentionNotificationID;
      kind: AttentionNotificationKind;
      occurredAt: string;
    }>
  | Readonly<{ type: "snapshot_complete"; sequence: number; source: "snapshot"; sessionID: string }>;

export type AttentionNotificationEventParams = Readonly<{ event: AttentionNotificationEvent }>;

export type AttentionNotificationEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: AttentionNotificationEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;
