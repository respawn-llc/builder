import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";

import { queryKeys, useAppServices } from "@/app-facade";
import { ProjectLabelDataContext } from "./projectLabelContext";
import { createProjectCatalogAuthority } from "./projectCatalogAuthority";
import { useManagedProjectLabelFilter } from "./projectLabelFilter";

export function ProjectLabelsProvider({
  children,
  projectID,
}: Readonly<{
  children: ReactNode;
  projectID: string;
}>) {
  const { api } = useAppServices();
  const queryClient = useQueryClient();
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
  const value = useMemo(
    () => ({
      authority,
      catalog,
      filter,
      projectID,
    }),
    [authority, catalog, filter, projectID],
  );
  return <ProjectLabelDataContext.Provider value={value}>{children}</ProjectLabelDataContext.Provider>;
}
