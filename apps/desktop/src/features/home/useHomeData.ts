import { useEffect, useState } from "react";
import {
  infiniteQueryOptions,
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";

import { ContractError, errorMessage } from "@/api";
import type { ProjectBinding, WorkflowProjectEvent } from "@/api";
import type { AppServices } from "@/app-facade";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { useConnectionSnapshot } from "@/app-facade";
import { workflowProjectEventCanChangeAttention, workflowProjectQuestionTaskID } from "@/app-facade";

type NativeProjectBinding = Parameters<
  Parameters<AppServices["nativeBridge"]["projectCreation"]["onCreated"]>[0]
>[0];

export function useProjectPages() {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  useEffect(
    () => () => {
      queryClient.removeQueries({ queryKey: queryKeys.projects, exact: true });
    },
    [queryClient],
  );
  return useInfiniteQuery({
    queryKey: queryKeys.projects,
    queryFn: async ({ pageParam }) => api.listProjects(pageParam),
    initialPageParam: "",
    getNextPageParam: (lastPage) => (lastPage.nextPageToken.length > 0 ? lastPage.nextPageToken : undefined),
    placeholderData: keepPreviousData,
  });
}

export function useGlobalAttentionPages(enabled = true) {
  const { api } = useAppServices();
  return useInfiniteQuery({
    ...globalAttentionQueryOptions(api),
    enabled,
  });
}

export function useSidebarGlobalAttentionPages() {
  const { api } = useAppServices();
  return useInfiniteQuery({
    ...globalAttentionQueryOptions(api),
    refetchOnMount: (query) =>
      query.observers.filter((observer) => observer.getCurrentResult().isEnabled).length <= 1,
  });
}

export function useGlobalAttentionEvents() {
  const { api, logger } = useAppServices();
  const connection = useConnectionSnapshot();
  const queryClient = useQueryClient();
  const [openGeneration, setOpenGeneration] = useState<number | null>(null);

  useEffect(() => {
    if (connection.phase !== "connected") {
      return;
    }
    const subscriptionGeneration = connection.generation;
    let refreshFrame: number | null = null;
    let subscriptionLifecycle: GlobalAttentionSubscriptionLifecycle = "initial";
    const refreshAttention = () => {
      if (refreshFrame !== null) {
        return;
      }
      refreshFrame = window.requestAnimationFrame(() => {
        refreshFrame = null;
        void queryClient.invalidateQueries({ queryKey: queryKeys.allAttention, refetchType: "active" });
      });
    };
    const refreshQuestionTask = (event: WorkflowProjectEvent) => {
      const taskID = workflowProjectQuestionTaskID(event);
      if (taskID === null) {
        return;
      }
      void queryClient.invalidateQueries({ queryKey: queryKeys.task(taskID), refetchType: "active" });
      void queryClient.invalidateQueries({
        queryKey: queryKeys.taskAttention(taskID),
        refetchType: "active",
      });
      void queryClient.invalidateQueries({ queryKey: queryKeys.activity(taskID), refetchType: "active" });
      void queryClient.invalidateQueries({ queryKey: queryKeys.allPendingAsks, refetchType: "active" });
    };
    const logSubscriptionWarning = (message: string, error: Error) => {
      void logger.append("warn", message, { error: errorMessage(error) });
    };
    const subscription = api.subscribeProject("", {
      onOpen() {
        const shouldReconcile = subscriptionLifecycle !== "initial";
        subscriptionLifecycle = "open";
        setOpenGeneration(subscriptionGeneration);
        if (shouldReconcile) {
          refreshAttention();
        }
      },
      onEvent(event) {
        refreshQuestionTask(event);
        if (workflowProjectEventCanChangeAttention(event)) {
          refreshAttention();
        }
      },
      onComplete() {
        return;
      },
      onError(error) {
        if (error instanceof ContractError) {
          refreshAttention();
          logSubscriptionWarning("Global attention event payload failed.", error);
          return;
        }
        subscriptionLifecycle = "recovery-pending";
        logSubscriptionWarning("Global attention subscription failed.", error);
      },
    });
    return () => {
      if (refreshFrame !== null) {
        window.cancelAnimationFrame(refreshFrame);
      }
      subscription.close();
    };
  }, [api, connection.generation, connection.phase, logger, queryClient]);
  return connection.phase === "connected" && openGeneration === connection.generation;
}

type GlobalAttentionSubscriptionLifecycle = "initial" | "open" | "recovery-pending";

export function useProjectCreationEvents(onCreated: (binding: NativeProjectBinding) => void) {
  const { logger, nativeBridge } = useAppServices();
  useEffect(() => {
    if (!nativeBridge.capabilities.projectCreationWindow) {
      return undefined;
    }
    let active = true;
    let unlisten: (() => void) | undefined;
    void nativeBridge.projectCreation
      .onCreated(onCreated)
      .then((nextUnlisten) => {
        if (!active) {
          nextUnlisten();
          return;
        }
        unlisten = nextUnlisten;
      })
      .catch((error: unknown) => {
        void logger.append("warn", "Project creation event listener failed.", { error: errorMessage(error) });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [logger, nativeBridge, onCreated]);
}

export function useProjectCreation() {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: ProjectCreateInput) =>
      api.createProject(input.name, input.key, input.workspaceRoot),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.projects });
    },
  });
}

export function useWorkspaceAttach() {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: WorkspaceAttachInput): Promise<ProjectBinding> =>
      api.attachWorkspace(input.projectID, input.workspaceRoot),
    onSuccess: async (_binding, input) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.workspaces(input.projectID) });
      await queryClient.invalidateQueries({ queryKey: queryKeys.projects });
    },
  });
}

export type ProjectCreateInput = Readonly<{
  name: string;
  key: string;
  workspaceRoot: string;
}>;

export type WorkspaceAttachInput = Readonly<{
  projectID: string;
  workspaceRoot: string;
}>;

function globalAttentionQueryOptions(api: AppServices["api"]) {
  return infiniteQueryOptions({
    queryKey: queryKeys.attention,
    queryFn: async ({ pageParam }) => api.listAttention(pageParam),
    initialPageParam: "",
    getNextPageParam: (lastPage) => (lastPage.nextPageToken.length > 0 ? lastPage.nextPageToken : undefined),
    placeholderData: keepPreviousData,
  });
}
