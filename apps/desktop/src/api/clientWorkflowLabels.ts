import type { TaskListInput, TaskMutationInput } from "./clientInputs";
import { parseRpcResponse as parse } from "./clientParse";
import { compactJsonObject, type JsonObject } from "./json";
import {
  projectLabelCatalogSchema,
  projectLabelDeleteSchema,
  projectLabelMutationSchema,
  taskLabelAssignmentSchema,
} from "./schemas/workflowLabels";
import { taskCreateResponseSchema, taskListPageSchema } from "./schemas/workflowBoard";
import type { RpcTransport } from "./transport";
import type {
  ProjectLabel,
  ProjectLabelCatalog,
  TaskLabelAssignment,
  TaskLabelFilter,
  TaskListPage,
} from "./workflowLabels";

export function taskLabelFilterPayload(filter: TaskLabelFilter): JsonObject {
  switch (filter.kind) {
    case "none":
    case "unlabeled":
      return { kind: filter.kind };
    case "named":
      return {
        kind: filter.kind,
        named: {
          mode: filter.mode,
          label_ids: filter.labelIDs,
        },
      };
  }
}

export async function listProjectLabels(
  transport: RpcTransport,
  projectID: string,
): Promise<ProjectLabelCatalog> {
  return parse(
    "workflow.project.label.list",
    projectLabelCatalogSchema,
    await transport.call("workflow.project.label.list", { project_id: projectID }),
  );
}

export async function createProjectLabel(
  transport: RpcTransport,
  projectID: string,
  name: string,
): Promise<ProjectLabel> {
  return parse(
    "workflow.project.label.create",
    projectLabelMutationSchema,
    await transport.call("workflow.project.label.create", { project_id: projectID, name }),
  );
}

export async function renameProjectLabel(
  transport: RpcTransport,
  projectID: string,
  labelID: string,
  name: string,
): Promise<ProjectLabel> {
  return parse(
    "workflow.project.label.rename",
    projectLabelMutationSchema,
    await transport.call("workflow.project.label.rename", {
      project_id: projectID,
      label_id: labelID,
      name,
    }),
  );
}

export async function deleteProjectLabel(
  transport: RpcTransport,
  projectID: string,
  labelID: string,
): Promise<string> {
  return parse(
    "workflow.project.label.delete",
    projectLabelDeleteSchema,
    await transport.call("workflow.project.label.delete", {
      project_id: projectID,
      label_id: labelID,
    }),
  );
}

export async function getTaskLabels(transport: RpcTransport, taskID: string): Promise<TaskLabelAssignment> {
  return parse(
    "workflow.task.labels.get",
    taskLabelAssignmentSchema,
    await transport.call("workflow.task.labels.get", { task_id: taskID }),
  );
}

export async function updateTaskLabels(
  transport: RpcTransport,
  taskID: string,
  addLabelIDs: readonly string[],
  removeLabelIDs: readonly string[],
): Promise<TaskLabelAssignment> {
  return parse(
    "workflow.task.labels.update",
    taskLabelAssignmentSchema,
    await transport.call("workflow.task.labels.update", {
      task_id: taskID,
      add_label_ids: addLabelIDs,
      remove_label_ids: removeLabelIDs,
    }),
  );
}

export async function createTask(transport: RpcTransport, input: TaskMutationInput): Promise<string> {
  const response = parse(
    "workflow.task.create",
    taskCreateResponseSchema,
    await transport.call(
      "workflow.task.create",
      compactJsonObject({
        project_id: input.projectID,
        workflow_id: input.workflowID,
        title: input.title,
        body: input.body,
        source_workspace_id: input.sourceWorkspaceID,
        label_ids: input.labelIDs,
      }),
    ),
  );
  return response.task.id;
}

export async function listTasks(transport: RpcTransport, input: TaskListInput): Promise<TaskListPage> {
  return parse(
    "workflow.task.list",
    taskListPageSchema,
    await transport.call(
      "workflow.task.list",
      compactJsonObject({
        project_id: input.projectID,
        workflow_id: input.workflowID,
        column_keys: input.columnKeys ?? [],
        status_kinds: input.statusKinds ?? [],
        attention_kinds: input.attentionKinds ?? [],
        label_filter: taskLabelFilterPayload(input.labelFilter),
        sort: input.sort ?? [],
        page_size: input.pageSize,
        page_token: input.pageToken,
      }),
    ),
  );
}
