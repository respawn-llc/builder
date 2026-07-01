import type {
  WorkflowGraphDraft,
  WorkflowGraphMetadata,
  WorkflowGraphSaveConfirmation,
  WorkflowValidationMode,
} from "./models";

export type TaskMutationInput = Readonly<{
  projectID: string;
  workflowID: string;
  title: string;
  body: string;
  sourceWorkspaceID: string;
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
}>;

export type QuestionAnswerInput = Readonly<{
  clientRequestID: string;
  taskID: string;
  runID: string;
  askID: string;
  selectedOptionNumber: number;
  freeformAnswer: string;
}>;
