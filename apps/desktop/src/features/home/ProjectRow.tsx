import { useTranslation } from "react-i18next";
import { Check, Pencil } from "lucide-react";

import type { ProjectSummary } from "@/api";
import { formatHomeRelativePath } from "@/app-facade";
import { useAppNavigation } from "@/app-facade";
import { useOwnedSidebarRoots } from "@/app-facade";
import { useAppServices } from "@/app-facade";
import { cx } from "@/ui";

export function ProjectRow({
  onSelect,
  project,
  selected = false,
}: Readonly<{
  onSelect?: (() => void) | undefined;
  project: ProjectSummary;
  selected?: boolean | undefined;
}>) {
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const { homePath, nativeBridge } = useAppServices();
  const editLabel = useProjectEditLabel(project.name);
  const workspacePathLabel = formatHomeRelativePath(
    project.primaryWorkspace.rootPath,
    homePath,
    nativeBridge.capabilities.platform,
  );
  const selectProject = () => {
    if (onSelect !== undefined) {
      onSelect();
      return;
    }
    void navigation.openProject(project.id);
  };

  return (
    <div
      className={cx(
        "group flex min-w-0 flex-col gap-[var(--space-1)] rounded-[var(--radius-m)] px-[calc(var(--space-3)/2)] py-[var(--space-1)] text-[var(--color-on-island)] transition-colors",
        selected
          ? "bg-[color-mix(in_srgb,var(--color-on-island)_12%,transparent)]"
          : "hover:bg-[color-mix(in_srgb,var(--color-on-island)_4%,transparent)]",
      )}
    >
      <div className="relative min-w-0 pr-10">
        <button className="flex min-w-0 items-center text-left" onClick={selectProject} type="button">
          <span
            className={cx(
              "grid shrink-0 overflow-hidden place-items-center [transition:width_var(--motion-fast),opacity_var(--motion-fast)]",
              selected ? "mr-[var(--space-1)] w-[14px] opacity-100" : "mr-0 w-0 opacity-0",
            )}
          >
            <Check
              aria-hidden="true"
              size={14}
              strokeWidth={2}
            />
          </span>
          <strong className="min-w-0 truncate">{project.name}</strong>
        </button>
        <button
          aria-label={editLabel}
          className="absolute right-0 top-1/2 grid h-10 w-10 -translate-y-1/2 place-items-center justify-items-end rounded-full text-[var(--color-muted)] hover:text-[var(--color-on-island)]"
          onClick={() => {
            open({ kind: "projectEdit", mode: "overlay", projectID: project.id });
          }}
          type="button"
        >
          <Pencil aria-hidden="true" size={14} strokeWidth={1.5} />
        </button>
      </div>
      <button
        aria-label={`${project.name} ${workspacePathLabel}`}
        className="flex min-w-0 items-center gap-[var(--space-2)] text-left"
        onClick={selectProject}
        title={project.primaryWorkspace.rootPath}
        type="button"
      >
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--color-muted)]">
          {workspacePathLabel}
        </span>
        <span className="shrink-0 font-mono text-[0.78rem] text-[var(--color-muted)]">
          {project.key}
        </span>
      </button>
    </div>
  );
}

function useProjectEditLabel(projectName: string): string {
  const { t } = useTranslation();
  return t("home.editProject", { name: projectName });
}
