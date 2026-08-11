import { queryOptions, type QueryClient } from "@tanstack/react-query";

import type { ApiService } from "@/api";
import { queryKeys } from "./queryKeys";

export type WorktreeCreateTargetResolutionRequest = Readonly<{
  sessionID: string;
  target: string;
}>;
export type WorktreeSelectorRequest = Readonly<{ sessionID: string; selector: string }>;

export function createWorktreeTargetResolutionRequest(
  sessionID: string,
  rawTarget: string,
): WorktreeCreateTargetResolutionRequest {
  const request = Object.freeze({ sessionID, target: rawTarget.trim() });
  createKey(request);
  return request;
}

export function createWorktreeSelectorRequest(sessionID: string, selector: string): WorktreeSelectorRequest {
  const request = Object.freeze({ sessionID, selector });
  selectorKey(request);
  return request;
}

export async function invalidateWorktreeSessionReads(
  queryClient: QueryClient,
  sessionID: string,
): Promise<void> {
  await Promise.all(
    [queryKeys.worktreeStatus(sessionID), queryKeys.worktreeList(sessionID)].map(async (queryKey) =>
      queryClient.invalidateQueries({ queryKey, exact: true, refetchType: "active" }),
    ),
  );
}

export const worktreeStatusQueryOptions = (api: ApiService, sessionID: string) =>
  queryOptions({
    queryKey: queryKeys.worktreeStatus(sessionID),
    queryFn: async () => api.getWorktreeStatus(sessionID),
  });

export const worktreeListQueryOptions = (api: ApiService, sessionID: string) =>
  queryOptions({
    queryKey: queryKeys.worktreeList(sessionID),
    queryFn: async () => api.listWorktrees(sessionID),
  });

const createKey = (request: WorktreeCreateTargetResolutionRequest) =>
  queryKeys.worktreeCreateTargetResolution(request.sessionID, request.target);
const selectorKey = (request: WorktreeSelectorRequest) =>
  queryKeys.worktreeSelectorResolution(request.sessionID, request.selector);
const deleteKey = (request: WorktreeSelectorRequest) =>
  queryKeys.worktreeDeletePreview(request.sessionID, request.selector);

const createOperation = operation(createKey, async (api, request: WorktreeCreateTargetResolutionRequest) =>
  api.resolveWorktreeCreateTarget(request.sessionID, request.target),
);
const selectorOperation = operation(selectorKey, async (api, request: WorktreeSelectorRequest) =>
  api.resolveWorktreeSelector(request.sessionID, request.selector),
);
const deleteOperation = operation(deleteKey, async (api, request: WorktreeSelectorRequest) =>
  api.previewWorktreeDelete(request.sessionID, request.selector),
);
export const worktreeCreateTargetResolutionQueryOptions = createOperation.options;
export const freshFetchWorktreeCreateTargetResolution = createOperation.fresh;
export const disposeWorktreeCreateTargetResolution = createOperation.dispose;
export const worktreeSelectorResolutionQueryOptions = selectorOperation.options;
export const freshFetchWorktreeSelectorResolution = selectorOperation.fresh;
export const disposeWorktreeSelectorResolution = selectorOperation.dispose;
export const worktreeDeletePreviewQueryOptions = deleteOperation.options;
export const freshFetchWorktreeDeletePreview = deleteOperation.fresh;
export const disposeWorktreeDeletePreview = deleteOperation.dispose;

function operation<Request, Result extends NonNullable<unknown>>(
  key: (request: Request) => readonly unknown[],
  query: (api: ApiService, request: Request) => Promise<Result>,
) {
  const options = (api: ApiService, request: Request) =>
    transient(key(request), async () => query(api, request));
  return {
    options,
    fresh: async (queryClient: QueryClient, api: ApiService, request: Request) =>
      fresh(queryClient, options(api, request)),
    dispose: async (queryClient: QueryClient, request: Request) => dispose(queryClient, key(request)),
  };
}
const transient = <T>(queryKey: readonly unknown[], queryFn: () => Promise<T>) =>
  queryOptions({ queryKey, queryFn, refetchOnReconnect: false, retry: false });
async function fresh<T>(queryClient: QueryClient, options: ReturnType<typeof queryOptions<T>>) {
  await dispose(queryClient, options.queryKey);
  return queryClient.fetchQuery(options);
}
async function dispose(queryClient: QueryClient, queryKey: readonly unknown[]) {
  await queryClient.cancelQueries({ queryKey, exact: true }, { revert: true, silent: true });
  queryClient.removeQueries({ queryKey, exact: true });
}
