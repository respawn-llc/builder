import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { WorkflowPickerItem } from "@/api";
import { desktopChatEnabled } from "@/shared/feature-flags";
import { InteractiveChip } from "@/ui";
import { BoardWorkflowPicker } from "./BoardWorkflowPicker";
import { BoardFilterChrome } from "./BoardLabelFilter";
import { TaskSearchProjectTrigger } from "./TaskSearchChrome";

export function BoardFilterRow({
  activeWorkflow,
  canCreateTask,
  onLinkWorkflow,
  onNewTask,
  onOpenTask,
  onOpenTasks,
  onSelectWorkflow,
  projectID,
  workflows,
}: Readonly<{
  activeWorkflow: WorkflowPickerItem;
  canCreateTask: boolean;
  onLinkWorkflow(): void;
  onNewTask(): void;
  onOpenTask(taskID: string): void;
  onOpenTasks(): void;
  onSelectWorkflow(workflowID: string): void;
  projectID: string;
  workflows: readonly WorkflowPickerItem[];
}>) {
  const { t } = useTranslation();
  return (
    <>
      {desktopChatEnabled ? (
        <>
          <InteractiveChip disabled={!canCreateTask} onClick={onNewTask} tone="primary">
            <Plus aria-hidden="true" size={16} strokeWidth={1.8} />
            {t("board.newTask")}
          </InteractiveChip>
          <BoardWorkflowPicker
            activeWorkflow={activeWorkflow}
            onLinkWorkflow={onLinkWorkflow}
            onOpenTasks={onOpenTasks}
            onSelectWorkflow={onSelectWorkflow}
            workflows={workflows}
          />
        </>
      ) : null}
      <BoardFilterChrome />
      <TaskSearchProjectTrigger onOpenTask={onOpenTask} projectID={projectID} />
    </>
  );
}
