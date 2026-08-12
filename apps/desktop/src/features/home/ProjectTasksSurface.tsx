import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowPickerItem } from "@/api";
import { canonicalBoardFilter, errorMessage } from "@/api";
import {
  queryKeys,
  useAppNavigation,
  useAppServices,
  useOwnedSidebarRoots,
  type SidebarMode,
} from "@/app-facade";
import { directionalBoundary, InfiniteListBoundary, InteractiveChip } from "@/ui";
import { useProjectTaskListData, useProjectTaskListEvents } from "./projectTaskListData";

export function ProjectTasksSurface({
  projectID,
  sidebarMode,
}: Readonly<{
  projectID: string;
  sidebarMode: SidebarMode;
}>) {
  const { t } = useTranslation();
  const { api } = useAppServices();
  const navigation = useAppNavigation();
  const { open } = useOwnedSidebarRoots();
  const [expanded] = useState({ active: true, backlog: true, done: false });
  const [anchors] = useState({ active: 0, backlog: 0, done: 0 });
  const query = useQuery({
    queryKey: queryKeys.board(projectID, undefined, canonicalBoardFilter({ kind: "none" })),
    queryFn: async () => api.getBoard(projectID, undefined, canonicalBoardFilter({ kind: "none" })),
  });
  useProjectTaskListData({
    anchors,
    expanded,
    gateReady: query.isSuccess && query.data.workflows.length > 0,
    projectID,
  });
  useProjectTaskListEvents({
    enabled: query.isSuccess,
    labelEditorTaskID: null,
    projectID,
  });
  const boundary = directionalBoundary({
    failed: query.isError,
    loading: query.isPending,
    loadingLabel: t("states.loading"),
    message: query.isError ? errorMessage(query.error) : "",
    onRetry: () => {
      void query.refetch();
    },
    retryLabel: t("app.retry"),
  });
  if (boundary !== undefined) {
    return <InfiniteListBoundary direction="initial" state={boundary} />;
  }
  const workflows = query.data?.workflows ?? [];
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 gap-[var(--space-2)] overflow-x-auto px-[var(--space-4)] py-[var(--space-3)] hide-scrollbar">
        {workflows.map((workflow) => (
          <WorkflowBoardChip
            key={workflow.id}
            onClick={() => {
              void navigation.openProject(projectID, workflow.id);
            }}
            workflow={workflow}
          />
        ))}
        <InteractiveChip
          className="shrink-0"
          onClick={() => {
            open({ kind: "linkWorkflow", mode: sidebarMode, projectID });
          }}
        >
          <Plus aria-hidden="true" size={14} strokeWidth={1.8} />
          {t("workflowLibrary.linkWorkflow")}
        </InteractiveChip>
      </div>
      <div
        className="m-[var(--space-4)] mt-0 grid min-h-0 flex-1 place-items-center rounded-[var(--radius-l)] border border-dashed border-[var(--color-outline)] p-[var(--space-5)] text-center text-[var(--color-muted)]"
        data-testid="project-task-list-slot"
      >
        <div className="grid gap-[var(--space-2)]">
          <strong className="text-[var(--color-on-island)]">{t("home.prototype.taskListStubTitle")}</strong>
          <p className="m-0 text-sm">{t("home.prototype.taskListStubBody")}</p>
        </div>
      </div>
    </div>
  );
}

function WorkflowBoardChip({
  onClick,
  workflow,
}: Readonly<{ onClick: () => void; workflow: WorkflowPickerItem }>) {
  return (
    <InteractiveChip className="shrink-0" onClick={onClick} title={workflow.description}>
      {workflow.name}
    </InteractiveChip>
  );
}
