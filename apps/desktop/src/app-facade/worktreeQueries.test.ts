import { QueryClient } from "@tanstack/react-query";
import { newSetupOperationID } from "@/api";
import { createTestServices } from "@/test-support/app-services";
import { queryKeys } from "./queryKeys";
import {
  createWorktreeSelectorRequest,
  createWorktreeTargetResolutionRequest,
  disposeWorktreeCreateTargetResolution,
  freshFetchWorktreeCreateTargetResolution,
  invalidateWorktreeSessionReads,
  worktreeCreateTargetResolutionQueryOptions,
  worktreeDeletePreviewQueryOptions,
  worktreeSelectorResolutionQueryOptions,
} from "./worktreeQueries";
describe("Worktree query ownership", () => {
  it("uses exact keys, fresh transient fetches, and durable-only invalidation", async () => {
    let calls = 0;
    const services = createTestServices([
      {
        method: "worktree.create_target.resolve",
        handler: async () => ({
          resolution: {
            kind: "existing_branch",
            input: "Feature",
            resolved_ref: `refs/heads/Feature-${String(++calls)}`,
          },
        }),
      },
    ]);
    const queryClient = new QueryClient();
    const request = createWorktreeTargetResolutionRequest("session-1", " Feature ");
    const key = queryKeys.worktreeCreateTargetResolution(request.sessionID, request.target);
    expect(key).toEqual(["worktree", "session-1", "create-target-resolution", "Feature"]);
    await freshFetchWorktreeCreateTargetResolution(queryClient, services.api, request);
    await freshFetchWorktreeCreateTargetResolution(queryClient, services.api, request);
    expect(calls).toBe(2);
    await disposeWorktreeCreateTargetResolution(queryClient, request);
    expect(queryClient.getQueryState(key)).toBeUndefined();
    const status = queryKeys.worktreeStatus("session-1");
    const list = queryKeys.worktreeList("session-1");
    const transient = queryKeys.worktreeDeletePreview("session-1", "feature");
    queryClient.setQueryData(status, {});
    queryClient.setQueryData(list, {});
    queryClient.setQueryData(transient, {});
    await invalidateWorktreeSessionReads(queryClient, "session-1");
    expect([status, list, transient].map((value) => queryClient.getQueryState(value)?.isInvalidated)).toEqual(
      [true, true, false],
    );
  });
  it("preserves changed authorities through QueryClient replacement", async () => {
    const git = {
      canonical_root: "/repo/feature",
      head_object: "abc123",
      branch_ref: null,
      branch_name: "feature",
      detached: false,
      bare: false,
      locked_reason: null,
      prunable_reason: null,
      is_main: false,
      path_available: true,
    };
    const topology = { variant: "external", external: { git } };
    const projected = (selector: string) => ({
      topology,
      projection: {
        selector,
        is_current: false,
        switch: { kind: "enter", selector },
      },
    });
    const services = createTestServices([
      {
        method: "worktree.create_target.resolve",
        handler: (_params, index) => ({
          resolution: {
            kind: "existing_branch",
            input: "feature",
            resolved_ref: `refs/heads/feature-${String(index)}`,
          },
        }),
      },
      {
        method: "worktree.selector.resolve",
        handler: (_params, index) => ({ worktree: projected(`feature-${String(index)}`) }),
      },
      {
        method: "worktree.deletePreview",
        handler: (_params, index) => ({
          worktree: topology,
          deletion_selector: "/repo/feature",
          cleanliness: index === 0 ? { kind: "clean" } : { kind: "dirty", dirty_file_count: index },
        }),
      },
      { method: "worktree.create", result: {} },
      { method: "worktree.enter", result: {} },
      { method: "worktree.delete", result: {} },
    ]);
    const queryClient = new QueryClient();
    const createOptions = worktreeCreateTargetResolutionQueryOptions(
      services.api,
      createWorktreeTargetResolutionRequest("session-1", "feature"),
    );
    const selectorOptions = worktreeSelectorResolutionQueryOptions(
      services.api,
      createWorktreeSelectorRequest("session-1", "feature"),
    );
    const deleteOptions = worktreeDeletePreviewQueryOptions(
      services.api,
      createWorktreeSelectorRequest("session-1", "feature"),
    );
    await queryClient.fetchQuery(createOptions);
    await queryClient.fetchQuery(selectorOptions);
    await queryClient.fetchQuery(deleteOptions);
    const create = await queryClient.fetchQuery(createOptions);
    const selector = await queryClient.fetchQuery(selectorOptions);
    const deletion = await queryClient.fetchQuery(deleteOptions);
    await services.api
      .createWorktree({
        sessionID: "session-1",
        setupOperationID: newSetupOperationID(),
        resolution: create.resolution,
        baseRef: null,
      })
      .catch(() => undefined);
    if (selector.worktree.switchOperation === null) throw new Error("fixture omitted Switch authority");
    await services.api.switchWorktree("session-1", selector.worktree.switchOperation).catch(() => undefined);
    await services.api.deleteWorktree("session-1", deletion, "confirm").catch(() => undefined);
    expect(services.transport.calls.slice(-3).map(({ params }) => params)).toMatchObject([
      { base_ref: "refs/heads/feature-1" },
      { selector: "feature-1" },
      { force_folder_removal: true },
    ]);
  });
});
