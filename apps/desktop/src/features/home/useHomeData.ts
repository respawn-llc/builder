import { useEffect } from "react";
import {
  infiniteQueryOptions,
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";

import { errorMessage } from "@/api";
import type { AppServices } from "@/app-facade";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";

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
    queryFn: async ({ pageParam }: Readonly<{ pageParam: string | null }>) => api.listProjects(pageParam),
    initialPageParam: null,
    getNextPageParam: (lastPage) => lastPage.nextPageToken ?? undefined,
    placeholderData: keepPreviousData,
  });
}

export function useGlobalAttentionPages() {
  const { api } = useAppServices();
  return useInfiniteQuery(globalAttentionQueryOptions(api));
}

export function useSidebarGlobalAttentionPages() {
  const { api } = useAppServices();
  return useInfiniteQuery({
    ...globalAttentionQueryOptions(api),
    refetchOnMount: (query) =>
      query.observers.filter((observer) => observer.getCurrentResult().isEnabled).length <= 1,
  });
}

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
    mutationFn: async (input: WorkspaceAttachInput) =>
      (await api.attachWorkspace(input.projectID, input.workspaceRoot)).binding,
    onSuccess: async () => {
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
