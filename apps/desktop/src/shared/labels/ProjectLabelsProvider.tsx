import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, type ReactNode } from "react";

import { errorMessage } from "@/api";
import {
  queryKeys,
  reportNonCancelledError,
  useAppServices,
  useConnectionSnapshot,
} from "@/app-facade";
import { useStableCallback } from "@/ui";
import { createProjectLabelEffects } from "./labelEventEffects";
import { ProjectLabelDataContext } from "./projectLabelContext";
import { useManagedProjectLabelFilter } from "./projectLabelFilter";

export function ProjectLabelsProvider({
  children,
  onBackgroundError,
  subscribeToProject = true,
  projectID,
}: Readonly<{
  children: ReactNode;
  onBackgroundError?: ((error: unknown) => void) | undefined;
  subscribeToProject?: boolean | undefined;
  projectID: string;
}>) {
  const { api, logger } = useAppServices();
  const queryClient = useQueryClient();
  const connection = useConnectionSnapshot();
  const catalog = useQuery({
    queryKey: queryKeys.projectLabels(projectID),
    queryFn: async () => api.listProjectLabels(projectID),
    retry: false,
  });
  const catalogLabelIDs = useMemo(
    () => catalog.data?.labels.map((label) => label.id) ?? null,
    [catalog.data],
  );
  const filter = useManagedProjectLabelFilter(projectID, catalogLabelIDs);
  const reportBackgroundError = useStableCallback((error: unknown) => {
    reportNonCancelledError(error, (failure) => {
      onBackgroundError?.(failure);
      void logger.append("warn", "Project label refresh failed.", {
        error: errorMessage(failure),
        projectID,
      });
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
        onFilterAction: filter.dispatch,
        onBackgroundError: reportBackgroundError,
        projectID,
        queryClient,
      }),
    [filter.dispatch, projectID, queryClient, reportBackgroundError],
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
      catalog,
      effects,
      filter,
      projectID,
    }),
    [catalog, effects, filter, projectID],
  );
  return <ProjectLabelDataContext.Provider value={value}>{children}</ProjectLabelDataContext.Provider>;
}
