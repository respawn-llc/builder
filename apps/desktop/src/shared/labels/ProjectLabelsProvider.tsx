import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, type ReactNode } from "react";

import { errorMessage } from "@/api";
import { queryKeys, useAppServices, useConnectionSnapshot } from "@/app-facade";
import { useStableCallback } from "@/ui";
import { createProjectLabelEffects, type LabelMembershipRefreshEffect } from "./labelEventEffects";
import { ProjectLabelDataContext } from "./projectLabelContext";
import { createProjectCatalogAuthority } from "./projectCatalogAuthority";
import { useManagedProjectLabelFilter } from "./projectLabelFilter";

export function ProjectLabelsProvider({
  children,
  onBackgroundError,
  onMembershipRefresh,
  projectID,
}: Readonly<{
  children: ReactNode;
  onBackgroundError?: ((error: unknown) => void) | undefined;
  onMembershipRefresh?: ((effect: LabelMembershipRefreshEffect) => Promise<void> | void) | undefined;
  projectID: string;
}>) {
  const { api, logger } = useAppServices();
  const queryClient = useQueryClient();
  const connection = useConnectionSnapshot();
  const authority = useMemo(
    () =>
      createProjectCatalogAuthority({
        projectID,
        queryClient,
        listCatalog: async () => api.listProjectLabels(projectID),
      }),
    [api, projectID, queryClient],
  );
  const catalog = useQuery({
    queryKey: queryKeys.projectLabels(projectID),
    queryFn: async ({ signal }) => authority.read(signal),
    retry: false,
  });
  const catalogLabelIDs = useMemo(
    () => catalog.data?.labels.map((label) => label.id) ?? null,
    [catalog.data],
  );
  const filter = useManagedProjectLabelFilter(projectID, catalogLabelIDs);
  const notifyMembershipRefresh = useStableCallback(async (effect: LabelMembershipRefreshEffect) => {
    await onMembershipRefresh?.(effect);
  });
  const reportBackgroundError = useStableCallback((error: unknown) => {
    onBackgroundError?.(error);
    void logger.append("warn", "Project label refresh failed.", {
      error: errorMessage(error),
      projectID,
    });
  });
  const effects = useMemo(
    () =>
      createProjectLabelEffects({
        authority,
        onFilterAction: filter.dispatch,
        onMembershipRefresh: notifyMembershipRefresh,
        projectID,
        queryClient,
      }),
    [authority, filter.dispatch, notifyMembershipRefresh, projectID, queryClient],
  );
  useEffect(() => {
    if (projectID.length === 0 || connection.phase !== "connected") {
      return;
    }
    const run = (operation: Promise<void>): void => {
      void operation.catch(reportBackgroundError);
    };
    const subscription = api.subscribeProject(projectID, {
      onOpen() {
        run(effects.refreshAfterSubscriptionBoundary());
      },
      onEvent(event) {
        run(effects.consumeProjectEvent(event));
      },
      onComplete() {
        run(effects.refreshAfterSubscriptionBoundary());
      },
      onError(error) {
        reportBackgroundError(error);
        run(effects.refreshAfterSubscriptionBoundary());
      },
    });
    return () => {
      subscription.close();
    };
  }, [api, connection.generation, connection.phase, effects, projectID, reportBackgroundError]);
  const value = useMemo(
    () => ({
      authority,
      catalog,
      effects,
      filter,
      projectID,
    }),
    [authority, catalog, effects, filter, projectID],
  );
  return <ProjectLabelDataContext.Provider value={value}>{children}</ProjectLabelDataContext.Provider>;
}
