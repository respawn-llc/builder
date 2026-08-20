import type {
  ApprovalDecision,
  WorkflowExecutionTargetSelection,
  WorkflowGraphMetadata,
  WorkflowGraphSaveConfirmation,
  TaskStatusKind,
  WorkflowValidationMode,
} from "./models";
import type { WorkflowGraphDraft } from "./workflowGraphModels";
import type { BoardNodeCardsSort, WorkflowTaskListSort } from "./boardNodeCardsSorting";
import type { ProjectTaskGroup, TaskLabelFilter } from "./workflowLabels";
import type { BoardFilter } from "./workflowBoardFilters";
import type { SetupOperationID } from "./setupOperationID";

type TaskMutationFields = Readonly<{
  projectID: string;
  title: string;
  body: string;
  sourceWorkspaceID: string;
  labelIDs: readonly string[];
  dependencyIntents: readonly TaskDependencyCreateIntent[];
}>;

export type TaskDependencyCreateIntent = Readonly<{
  relatedTaskID: string;
  newTaskRole: "blocker" | "blocked";
}>;

export type TaskMutationInput =
  | (TaskMutationFields &
      Readonly<{
        workflowID: string;
      }>)
  | (TaskMutationFields &
      Readonly<{
        workflowID?: undefined;
      }>);

export type TaskListInput = Readonly<{
  projectID: string;
  workflowID?: string | undefined;
  group?: ProjectTaskGroup | undefined;
  columnKeys?: readonly string[] | undefined;
  statusKinds?: readonly TaskStatusKind[] | undefined;
  attentionKinds?: readonly ("question" | "approval" | "interrupted")[] | undefined;
  labelFilter: TaskLabelFilter;
  sort?: readonly WorkflowTaskListSort[] | undefined;
  offset?: number | undefined;
  limit?: number | undefined;
}>;

export type ProjectTaskGroupCountsInput = Readonly<{
  projectID: string;
}>;

export type BoardNodeCardsInput = Readonly<{
  projectID: string;
  workflowID: string;
  nodeID: string;
  filter: BoardFilter;
  sort?: BoardNodeCardsSort | undefined;
  offset?: number | undefined;
}>;

export const workflowPageSize = 40;

export type WorkflowListInput = Readonly<{
  offset?: number | undefined;
  limit?: number | undefined;
  projectID?: string | undefined;
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
  transitionKey?: string | undefined;
  values?: Readonly<Record<string, Readonly<Record<string, string>>>>;
  commentary?: string | undefined;
  executionTarget?: WorkflowExecutionTargetSelection | undefined;
  proceedDespiteDependencies?: boolean | undefined;
}>;

export type TaskStartInput = Readonly<{
  taskID: string;
  setupOperationID?: SetupOperationID | undefined;
  executionTarget?: WorkflowExecutionTargetSelection | undefined;
  proceedDespiteDependencies?: boolean | undefined;
}>;

export type TaskResumeInput = Readonly<{
  taskID: string;
  setupOperationID?: SetupOperationID | undefined;
  executionTarget?: WorkflowExecutionTargetSelection | undefined;
}>;

export type OrdinaryQuestionAnswerInput = Readonly<{
  kind: "ordinary";
  promptID: string;
  sessionID: string;
  stepID: string;
  selectedOptionNumber: number | null;
  freeformAnswer: string;
}>;

export type ApprovalQuestionAnswerInput = Readonly<{
  kind: "approval";
  promptID: string;
  sessionID: string;
  stepID: string;
  decision: ApprovalDecision;
  commentary: string;
}>;

export type QuestionAnswerInput = OrdinaryQuestionAnswerInput | ApprovalQuestionAnswerInput;

export type PromptAnswerBatchEntryInput =
  | Readonly<{
      kind: "question";
      promptID: string;
      selectedOptionNumber: number | null;
      freeform: string | null;
    }>
  | Readonly<{
      kind: "approval";
      promptID: string;
      decision: ApprovalDecision;
      commentary: string | null;
    }>
  | Readonly<{
      kind: "declined";
      promptID: string;
    }>;

export type PromptAnswerBatchInput = Readonly<{
  sessionID: string;
  stepID: string;
  entries: readonly PromptAnswerBatchEntryInput[];
}>;

export type PromptAnswerBatchResponse = Readonly<{
  results: readonly Readonly<{
    promptID: string;
    outcome: "resolved" | "skipped";
  }>[];
}>;
