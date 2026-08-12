import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import type { ProjectWorkspaceAttachResponse } from "@/api";
import { errorMessage } from "@/api";
import { invalidateProjectBoardQueries, invalidateProjectDeleteQueries } from "@/app-facade";
import type { AppServices } from "@/app-facade";
import { queryKeys } from "@/app-facade";
import { useAppServices } from "@/app-facade";

type NativeBridge = AppServices["nativeBridge"];
type ProjectWorkspaceBridge = NativeBridge["projectWorkspace"];
type NativeProjectWorkspaceChanged = Parameters<Parameters<ProjectWorkspaceBridge["onChanged"]>[0]>[0];
type NativeWorkspaceUnlinkTarget = Parameters<Parameters<ProjectWorkspaceBridge["onUnlinkRequested"]>[0]>[0];

export function useProjectEdit(projectID: string) {
  const { api } = useAppServices();
  return useQuery({
    queryKey: queryKeys.projectEdit(projectID),
    queryFn: async () => api.getProjectEdit(projectID),
    enabled: projectID.length > 0,
  });
}

export function useProjectSave(projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: Readonly<{ displayName: string; projectKey: string }>) =>
      api.updateProject(projectID, input.displayName, input.projectKey),
    onSuccess: async () => {
      await invalidateProjectEditQueries(queryClient, projectID);
    },
  });
}

export function useProjectDefaultWorkspaceSave(projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (workspaceID: string) => api.setDefaultWorkspace(projectID, workspaceID),
    onSuccess: async () => {
      await invalidateProjectWorkspaceOwners(queryClient, projectID);
    },
  });
}

export function useProjectWorkspaceAttach(projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (workspaceRoot: string): Promise<ProjectWorkspaceAttachResponse> =>
      api.attachWorkspace(projectID, workspaceRoot),
    onSuccess: async () => {
      await invalidateProjectWorkspaceOwners(queryClient, projectID);
    },
  });
}

export function useProjectWorkspaceUnlink(projectID: string) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (workspaceID: string) => api.unlinkWorkspace(projectID, workspaceID),
    onSuccess: async () => {
      await invalidateProjectWorkspaceOwners(queryClient, projectID);
    },
  });
}

export function useProjectDelete(
  projectID: string,
  options: Readonly<{ invalidateOnDeleted?: boolean | undefined }> = {},
) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
  const invalidateOnDeleted = options.invalidateOnDeleted ?? true;
  return useMutation({
    mutationFn: async () => api.deleteProject(projectID),
    onSuccess: async (response) => {
      if (!response.deleted) {
        await invalidateProjectEditQueries(queryClient, projectID);
        return;
      }
      if (invalidateOnDeleted) {
        await invalidateProjectDeleteQueries(queryClient, projectID);
      }
    },
  });
}

export function useProjectWorkspaceUnlinkRequests(
  nativeBridge: NativeBridge,
  handler: (target: NativeWorkspaceUnlinkTarget) => void,
) {
  const { logger } = useAppServices();
  useEffect(() => {
    let active = true;
    let unlisten: (() => void) | null = null;
    void nativeBridge.projectWorkspace
      .onUnlinkRequested(handler)
      .then((nextUnlisten) => {
        if (active) {
          unlisten = nextUnlisten;
          return;
        }
        nextUnlisten();
      })
      .catch((error: unknown) => {
        void logger.append("warn", "Workspace unlink event listener failed.", { error: errorMessage(error) });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [handler, logger, nativeBridge.projectWorkspace]);
}

export function useProjectWorkspaceChangedEvents(nativeBridge: NativeBridge, projectID: string) {
  const { logger } = useAppServices();
  const queryClient = useQueryClient();
  useEffect(() => {
    let active = true;
    let unlisten: (() => void) | null = null;
    const handler = (event: NativeProjectWorkspaceChanged) => {
      if (active && event.projectID === projectID) {
        void invalidateProjectWorkspaceOwners(queryClient, projectID);
      }
    };
    void nativeBridge.projectWorkspace
      .onChanged(handler)
      .then((nextUnlisten) => {
        if (active) {
          unlisten = nextUnlisten;
          return;
        }
        nextUnlisten();
      })
      .catch((error: unknown) => {
        void logger.append("warn", "Project workspace change listener failed.", {
          error: errorMessage(error),
        });
      });
    return () => {
      active = false;
      unlisten?.();
    };
  }, [logger, nativeBridge.projectWorkspace, projectID, queryClient]);
}

async function invalidateProjectEditQueries(
  queryClient: ReturnType<typeof useQueryClient>,
  projectID: string,
): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.projects }),
    queryClient.invalidateQueries({ queryKey: queryKeys.projectEdit(projectID) }),
  ]);
}

async function invalidateProjectWorkspaceOwners(
  queryClient: ReturnType<typeof useQueryClient>,
  projectID: string,
): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.projects }),
    queryClient.invalidateQueries({ queryKey: queryKeys.projectEdit(projectID) }),
    invalidateProjectBoardQueries(queryClient, projectID),
  ]);
}
