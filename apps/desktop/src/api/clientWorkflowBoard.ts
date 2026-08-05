import type { BoardNodeCardsInput } from "./clientInputs";
import { parseRpcResponse as parse } from "./clientParse";
import { compactJsonObject } from "./json";
import { taskLabelFilterPayload } from "./clientWorkflowLabels";
import { boardNodeCardsPageSize, type BoardNodeCardsPage, type WorkflowBoard } from "./models";
import { boardNodeCardsPageSchema, workflowBoardSchema } from "./schemas/workflowBoard";
import { workflowIDSchema } from "./schemas/workflowID";
import type { RpcTransport } from "./transport";
import type { BoardFilter } from "./workflowBoardFilters";

export async function getBoard(
  transport: RpcTransport,
  projectID: string,
  workflowID: string | undefined,
  filter: BoardFilter,
): Promise<WorkflowBoard> {
  return parse(
    "workflow.board.get",
    workflowBoardSchema,
    await transport.call(
      "workflow.board.get",
      compactJsonObject({
        project_id: projectID,
        workflow_id: workflowID === undefined ? undefined : workflowIDSchema.parse(workflowID),
        label_filter: taskLabelFilterPayload(filter.labelFilter),
        dependency_filter: filter.dependencyFilter,
      }),
    ),
  );
}

export async function listBoardNodeCards(
  transport: RpcTransport,
  input: BoardNodeCardsInput,
): Promise<BoardNodeCardsPage> {
  return parse(
    "workflow.board.nodeCards.list",
    boardNodeCardsPageSchema,
    await transport.call(
      "workflow.board.nodeCards.list",
      compactJsonObject({
        project_id: input.projectID,
        workflow_id: workflowIDSchema.parse(input.workflowID),
        node_id: input.nodeID,
        label_filter: taskLabelFilterPayload(input.filter.labelFilter),
        dependency_filter: input.filter.dependencyFilter,
        page_size: boardNodeCardsPageSize,
        sort: input.sort,
        offset: input.offset ?? 0,
      }),
    ),
  );
}
