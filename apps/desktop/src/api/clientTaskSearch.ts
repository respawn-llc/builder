import { parseRpcResponse } from "./clientParse";
import { decodeTaskSearchError } from "./errors";
import { compactJsonObject } from "./json";
import { taskSearchResponseSchema } from "./schemas/taskSearch";
import type { TaskSearchInput, TaskSearchResponse } from "./taskSearch";
import type { RpcTransport } from "./transport";

export async function searchTasks(
  transport: RpcTransport,
  input: TaskSearchInput,
  signal?: AbortSignal,
): Promise<TaskSearchResponse> {
  try {
    return parseRpcResponse(
      "workflow.task.search",
      taskSearchResponseSchema,
      await transport.callDedicated(
        "workflow.task.search",
        compactJsonObject({
          mode: input.mode,
          query: input.query,
          context: input.context,
          case_sensitive: input.caseSensitive,
          include_comments: input.includeComments,
          project_ids: input.projectIDs,
          status_kinds: input.statusKinds,
          page_size: input.pageSize,
          offset: input.offset,
        }),
        signal === undefined ? undefined : { signal },
      ),
    );
  } catch (error) {
    const taskSearchError = decodeTaskSearchError(error);
    if (taskSearchError !== null) {
      throw taskSearchError;
    }
    throw error;
  }
}
