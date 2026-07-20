import type {
  ApprovalDecision,
  WorkflowExecutionTargetSelection,
  WorkflowGraphDraft,
  WorkflowGraphMetadata,
  WorkflowGraphSaveConfirmation,
  TaskStatusKind,
  WorkflowValidationMode,
} from "./models";
import type { TaskLabelFilter } from "./workflowLabels";
import type { SetupOperationID } from "./setupOperationID";

export type TaskMutationInput = Readonly<{
  projectID: string;
  workflowID: string;
  title: string;
  body: string;
  sourceWorkspaceID: string;
  labelIDs: readonly string[];
}>;

export type TaskListInput = Readonly<{
  projectID?: string | undefined;
  workflowID?: string | undefined;
  columnKeys?: readonly string[] | undefined;
  statusKinds?: readonly TaskStatusKind[] | undefined;
  attentionKinds?: readonly ("question" | "approval" | "interrupted")[] | undefined;
  labelFilter: TaskLabelFilter;
  sort?:
    | readonly Readonly<{
        field: "created" | "updated" | "status" | "column" | "run_count" | "title";
        direction: "asc" | "desc";
      }>[]
    | undefined;
  pageSize: number;
  pageToken?: string | undefined;
}>;

export type BoardNodeCardsInput = Readonly<{
  projectID: string;
  workflowID: string;
  nodeID: string;
  labelFilter: TaskLabelFilter;
  pageToken?: string | null | undefined;
}>;

export type WorkflowListInput = Readonly<{
  pageSize?: number | undefined;
  pageToken?: string | undefined;
  query?: string | undefined;
}>;

export type WorkflowCreateInput = Readonly<{
  name: string;
  description: string;
}>;

export type WorkflowCreateAndLinkInput = WorkflowCreateInput &
  Readonly<{
    projectID: string;
  }>;

export type WorkflowProjectLinkInput = Readonly<{
  projectID: string;
  workflowID: string;
}>;

export type WorkflowDeleteInput = Readonly<{
  workflowID: string;
  confirmed: boolean;
  expectedVersion: number;
  expectedProjectCount: number;
  expectedLinkCount: number;
  expectedTaskCount: number;
  cleanupArtifacts?: boolean;
}>;

export type WorkflowGraphValidateDraftInput = Readonly<{
  workflowID: string;
  metadata?: WorkflowGraphMetadata | undefined;
  graph: WorkflowGraphDraft;
  modes: readonly WorkflowValidationMode[];
}>;

export type WorkflowScriptPathValidateInput = Readonly<{
  workflowID: string;
  nodeID: string;
  scriptPath: string;
}>;

export type WorkflowGraphDeriveWiringInput = Readonly<{
  workflowID: string;
  graph: WorkflowGraphDraft;
}>;

export type WorkflowGraphSavePreviewInput = Readonly<{
  workflowID: string;
  expectedVersion: number;
  metadata?: WorkflowGraphMetadata | undefined;
  graph: WorkflowGraphDraft;
}>;

export type WorkflowGraphSaveInput = WorkflowGraphSavePreviewInput &
  Readonly<{
    confirmation?: WorkflowGraphSaveConfirmation | undefined;
  }>;

export type TaskEditInput = Readonly<{
  taskID: string;
  title: string;
  body: string;
  sourceWorkspaceID?: string | undefined;
}>;

export type TaskMoveInput = Readonly<{
  taskID: string;
  targetNodeID: string;
  outputValues?: Readonly<Record<string, string>>;
  allowMissingEdge?: boolean;
  autoApprove?: boolean;
  setupOperationID?: SetupOperationID | undefined;
  executionTarget?: WorkflowExecutionTargetSelection | undefined;
}>;

export type OrdinaryQuestionAnswerInput = Readonly<{
  kind: "ordinary";
  clientRequestID: string;
  taskID: string;
  runID: string;
  askID: string;
  selectedOptionNumber: number | null;
  freeformAnswer: string;
}>;

export type ApprovalQuestionAnswerInput = Readonly<{
  kind: "approval";
  clientRequestID: string;
  taskID: string;
  runID: string;
  askID: string;
  decision: ApprovalDecision;
  commentary: string;
}>;

export type QuestionAnswerInput = OrdinaryQuestionAnswerInput | ApprovalQuestionAnswerInput;
