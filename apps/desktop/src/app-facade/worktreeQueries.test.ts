import { QueryClient } from "@tanstack/react-query";
import { newSetupOperationID } from "@/api";
import { worktreeQueryFixtureRoutes } from "@/test-support/api";
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
    const services = createTestServices(worktreeQueryFixtureRoutes());
    const queryClient = new QueryClient();
    const request = createWorktreeTargetResolutionRequest("session-1", " Feature ");
    const key = queryKeys.worktreeCreateTargetResolution(request.sessionID, request.target);

    expect(key).toEqual(["worktree", "session-1", "create-target-resolution", "Feature"]);
    await freshFetchWorktreeCreateTargetResolution(queryClient, services.api, request);
    await freshFetchWorktreeCreateTargetResolution(queryClient, services.api, request);
    expect(services.transport.descriptorCalls).toHaveLength(2);
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
    const services = createTestServices(worktreeQueryFixtureRoutes());
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
    const createTarget = await queryClient.fetchQuery(createOptions);
    const selector = await queryClient.fetchQuery(selectorOptions);
    const deletion = await queryClient.fetchQuery(deleteOptions);
    if (createTarget.resolution === undefined) {
      throw new Error("fixture omitted Create target resolution");
    }
    await services.api
      .createWorktree({
        sessionID: "session-1",
        setupOperationID: newSetupOperationID(),
        resolution: createTarget.resolution,
        baseRef: null,
      })
      .catch(() => undefined);
    const operation = selector.worktree?.projection?.switch;
    if (operation === undefined) throw new Error("fixture omitted Switch authority");
    await services.api.switchWorktree("session-1", operation).catch(() => undefined);
    await services.api.deleteWorktree("session-1", deletion, "confirm").catch(() => undefined);
    expect(services.transport.descriptorCalls.slice(-3).map(({ request }) => request)).toMatchObject([
      { spec: { baseRef: "refs/heads/feature-1" } },
      { selector: "feature-1" },
      { forceFolderRemoval: true },
    ]);
  });
});
