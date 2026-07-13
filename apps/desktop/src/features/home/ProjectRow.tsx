import { useTranslation } from "react-i18next";
import { Pencil } from "lucide-react";

import type { ProjectSummary } from "../../api";
import { formatHomeRelativePath } from "../../app/formatters";
import { useAppNavigation } from "../../app/navigation";
import { useSidebar } from "../../app/sidebarContext";
import { useAppServices } from "../../app/useAppServices";
import { HomeListCard } from "../../ui";

export function ProjectRow({ project }: Readonly<{ project: ProjectSummary }>) {
  const navigation = useAppNavigation();
  const { openSidebar } = useSidebar();
  const { homePath, nativeBridge } = useAppServices();
  const editLabel = useProjectEditLabel(project.name);
  const workspacePathLabel = formatHomeRelativePath(
    project.primaryWorkspace.rootPath,
    homePath,
    nativeBridge.capabilities.platform,
  );

  return (
    <HomeListCard
      action={
        <button
          aria-label={editLabel}
          className="absolute top-[var(--space-3)] right-[var(--space-3)] grid h-9 w-9 place-items-center rounded-full border border-[var(--color-outline)] bg-[var(--color-island-1)] text-[var(--color-on-island)]"
          onClick={() => {
            void openSidebar({ kind: "projectEdit", mode: "overlay", projectID: project.id });
          }}
          type="button"
        >
          <Pencil aria-hidden="true" size={16} strokeWidth={1.5} />
        </button>
      }
      ariaLabel={`${project.name} ${workspacePathLabel}`}
      onClick={() => {
        void navigation.openProject(project.id);
      }}
      title={project.primaryWorkspace.rootPath}
    >
      <span className="font-mono text-[0.78rem] text-[var(--color-muted)]">{project.key}</span>
      <strong>{project.name}</strong>
      <span className="truncate font-mono text-sm text-[var(--color-muted)]">{workspacePathLabel}</span>
    </HomeListCard>
  );
}

function useProjectEditLabel(projectName: string): string {
  const { t } = useTranslation();
  return t("home.editProject", { name: projectName });
}
