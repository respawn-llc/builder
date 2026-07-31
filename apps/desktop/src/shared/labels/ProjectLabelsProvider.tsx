import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { queryKeys, useAppServices, useConnectionSnapshot } from "@/app-facade";
import { showStatusToast, useStableCallback } from "@/ui";
import { createProjectLabelEffects, type LabelMembershipRefreshEffect } from "./labelEventEffects";
import { ProjectLabelDataContext } from "./projectLabelContext";
import { projectCatalogAuthorityRegistryFor } from "./projectCatalogAuthorityRegistry";
import { useManagedProjectLabelFilter } from "./projectLabelFilter";

export function ProjectLabelsProvider({
  children,
  onBackgroundError,
  onMembershipRefresh,
  subscribeToProject = true,
  projectID,
}: Readonly<{
  children: ReactNode;
  onBackgroundError?: ((error: unknown) => void) | undefined;
  onMembershipRefresh?: ((effect: LabelMembershipRefreshEffect) => Promise<void> | void) | undefined;
  subscribeToProject?: boolean | undefined;
  projectID: string;
}>) {
  const { t } = useTranslation();
  const { api, logger } = useAppServices();
  const queryClient = useQueryClient();
  const connection = useConnectionSnapshot();
  const authorityLease = useMemo(
    () =>
      projectCatalogAuthorityRegistryFor(queryClient).prepare(
        projectID,
        async () => api.listProjectLabels(projectID),
        async (labelIDs) => api.reorderProjectLabels(projectID, labelIDs),
        () => {
          showStatusToast({
            id: `project-label-reorder-failure-${projectID}`,
            tone: "danger",
            title: t("labels.reorderFailed"),
          });
        },
      ),
    [api, projectID, queryClient, t],
  );
  const authority = authorityLease.authority;
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
  useEffect(() => authorityLease.retain(filter.dispatch), [authorityLease, filter.dispatch]);
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
  const reportedPersistenceError = useRef<unknown>(null);
  useEffect(() => {
    if (filter.persistence.status !== "error") {
      reportedPersistenceError.current = null;
      return;
    }
    if (reportedPersistenceError.current === filter.persistence.error) {
      return;
    }
    reportedPersistenceError.current = filter.persistence.error;
    reportBackgroundError(filter.persistence.error);
  }, [filter.persistence, reportBackgroundError]);
  const effects = useMemo(
    () =>
      createProjectLabelEffects({
        authority,
        onFilterAction: authorityLease.dispatchFilterAction,
        onMembershipRefresh: notifyMembershipRefresh,
        projectID,
        queryClient,
      }),
    [authority, authorityLease.dispatchFilterAction, notifyMembershipRefresh, projectID, queryClient],
  );
  useEffect(() => {
    if (!subscribeToProject || projectID.length === 0 || connection.phase !== "connected") {
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
  }, [
    api,
    connection.generation,
    connection.phase,
    effects,
    projectID,
    reportBackgroundError,
    subscribeToProject,
  ]);
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
